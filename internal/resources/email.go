package resources

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"mime"
	"mime/quotedprintable"
	"net"
	"net/mail"
	"net/smtp"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/damhau/awxnext/internal/api"
	"github.com/damhau/awxnext/internal/crypto"
)

// Email notifications, as AWX's email backend: SMTP to host:port, in clear,
// with STARTTLS (use_tls) or over TLS from the start (use_ssl), logging in
// when a username and password are set.

// emailFields are AWX's email notification_configuration fields, with their
// types and defaults; fields without a default are required.
var emailFields = []struct {
	name, typ string
	def       any
}{
	{"host", "string", nil},
	{"port", "int", nil},
	{"username", "string", ""},
	{"password", "password", ""},
	{"use_tls", "bool", false},
	{"use_ssl", "bool", false},
	{"sender", "string", nil},
	{"recipients", "list", nil},
	{"timeout", "int", float64(30)},
}

// AWX's default email messages: the subject is the webhook's message, the
// body adds the job's metadata.
const (
	emailDefaultBody = "{{ job_friendly_name }} #{{ job.id }} had status {{ job.status }}, view details at {{ url }}\n\n{{ job_metadata }}"
	emailTestBody    = "Ansible Tower Test Notification %d %s"
)

var approvalEmailBodies = map[string]string{
	"running":   `The approval node "{{ approval_node_name }}" needs review. This approval node can be viewed at: {{ workflow_url }}` + "\n\n{{ job_metadata }}",
	"approved":  `The approval node "{{ approval_node_name }}" was approved. {{ workflow_url }}` + "\n\n{{ job_metadata }}",
	"denied":    `The approval node "{{ approval_node_name }}" was denied. {{ workflow_url }}` + "\n\n{{ job_metadata }}",
	"timed_out": `The approval node "{{ approval_node_name }}" has timed out. {{ workflow_url }}` + "\n\n{{ job_metadata }}",
}

// SMTPRootCAs verifies SMTP servers' certificates; nil uses the system's.
// Tests set it to trust their own server.
var SMTPRootCAs *x509.CertPool

// validateEmailConfig checks an email configuration as AWX does, fills its
// defaults and encrypts its password.
func validateEmailConfig(c *Ctx, cfg, old map[string]any) (map[string]any, error) {
	known := map[string]bool{}
	for _, f := range emailFields {
		known[f.name] = true
	}
	for k := range cfg {
		if !known[k] {
			return nil, api.FieldError("notification_configuration", fmt.Sprintf("Unexpected field %q for email notifications.", k))
		}
	}
	out := map[string]any{}
	var missing []string
	for _, f := range emailFields {
		v, ok := cfg[f.name]
		if !ok || v == nil {
			if f.def == nil {
				missing = append(missing, "'"+f.name+"'")
			} else {
				out[f.name] = f.def
			}
			continue
		}
		bad := false
		switch f.typ {
		case "string", "password":
			_, ok := v.(string)
			bad = !ok
		case "int":
			n, ok := num(v)
			bad = !ok || n != math.Trunc(n)
		case "bool":
			_, ok := v.(bool)
			bad = !ok
		case "list":
			l, ok := v.([]any)
			bad = !ok
			for _, x := range l {
				if _, ok := x.(string); !ok {
					bad = true
				}
			}
		}
		if bad {
			return nil, api.FieldError("notification_configuration", fmt.Sprintf("Configuration field '%s' incorrect type, expected %s.", f.name, f.typ))
		}
		out[f.name] = v
	}
	if len(missing) > 0 {
		return nil, api.FieldError("notification_configuration", fmt.Sprintf("Missing required fields for Notification Configuration: [%s].", strings.Join(missing, ", ")))
	}
	if strings.TrimSpace(out["host"].(string)) == "" {
		return nil, api.FieldError("notification_configuration", "host must not be empty.")
	}
	if p, _ := num(out["port"]); p < 1 || p > 65535 {
		return nil, api.FieldError("notification_configuration", "port must be between 1 and 65535.")
	}
	if t, _ := num(out["timeout"]); t < 1 {
		return nil, api.FieldError("notification_configuration", "timeout must be at least 1 second.")
	}
	if boolv(out["use_tls"]) && boolv(out["use_ssl"]) {
		return nil, api.FieldError("notification_configuration", "use_tls and use_ssl are mutually exclusive: set at most one.")
	}
	if _, err := mail.ParseAddress(out["sender"].(string)); err != nil {
		return nil, api.FieldError("notification_configuration", fmt.Sprintf("sender is not a valid email address: %q.", out["sender"]))
	}
	recipients := out["recipients"].([]any)
	if len(recipients) == 0 {
		return nil, api.FieldError("notification_configuration", "No values specified for field 'recipients'")
	}
	for _, r := range recipients {
		if _, err := mail.ParseAddress(r.(string)); err != nil {
			return nil, api.FieldError("notification_configuration", fmt.Sprintf("%q is not a valid email address.", r))
		}
	}
	if s := out["password"].(string); s != "" {
		switch {
		case s == crypto.Placeholder:
			out["password"], _ = old["password"].(string)
		case !crypto.IsEncrypted(s):
			enc, err := c.App.Crypto.Encrypt(s)
			if err != nil {
				return nil, err
			}
			out["password"] = enc
		}
	}
	return out, nil
}

// emailRecipients lists an email configuration's recipients as AWX shows
// them on a notification: ['a@example.com', 'b@example.com'].
func emailRecipients(cfg map[string]any) string {
	l, _ := cfg["recipients"].([]any)
	parts := make([]string, len(l))
	for i, r := range l {
		parts[i] = "'" + str(r) + "'"
	}
	return "[" + strings.Join(parts, ", ") + "]"
}

// sendEmail sends one message to the configuration's recipients. The timeout
// covers the whole exchange.
func sendEmail(ctx context.Context, app *api.App, cfg map[string]any, subject, body string) error {
	host := str(cfg["host"])
	port, _ := num(cfg["port"])
	secs, ok := num(cfg["timeout"])
	if !ok || secs < 1 {
		secs = 30
	}
	timeout := time.Duration(secs * float64(time.Second))
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	addr := net.JoinHostPort(host, strconv.Itoa(int(port)))
	tlsConfig := &tls.Config{ServerName: host, RootCAs: SMTPRootCAs, MinVersion: tls.VersionTLS12}
	dialer := &net.Dialer{}
	var conn net.Conn
	var err error
	if boolv(cfg["use_ssl"]) {
		conn, err = (&tls.Dialer{NetDialer: dialer, Config: tlsConfig}).DialContext(ctx, "tcp", addr)
	} else {
		conn, err = dialer.DialContext(ctx, "tcp", addr)
	}
	if err != nil {
		return fmt.Errorf("Error sending notification email: %v", err)
	}
	deadline, _ := ctx.Deadline()
	conn.SetDeadline(deadline)
	c, err := smtp.NewClient(conn, host)
	if err != nil {
		conn.Close()
		return fmt.Errorf("Error sending notification email: %v", err)
	}
	defer c.Close()
	if err := smtpSend(c, app, cfg, tlsConfig, subject, body); err != nil {
		return fmt.Errorf("Error sending notification email: %v", err)
	}
	return nil
}

func smtpSend(c *smtp.Client, app *api.App, cfg map[string]any, tlsConfig *tls.Config, subject, body string) error {
	local, err := os.Hostname()
	if err != nil || local == "" {
		local = "localhost"
	}
	if err := c.Hello(local); err != nil {
		return err
	}
	if boolv(cfg["use_tls"]) {
		if ok, _ := c.Extension("STARTTLS"); !ok {
			return errors.New("STARTTLS extension not supported by server.")
		}
		if err := c.StartTLS(tlsConfig); err != nil {
			return err
		}
	}
	user, pw := str(cfg["username"]), str(cfg["password"])
	if crypto.IsEncrypted(pw) {
		if pw, err = app.Crypto.Decrypt(pw); err != nil {
			return fmt.Errorf("decrypting password: %w", err)
		}
	}
	if user != "" && pw != "" {
		ok, mechs := c.Extension("AUTH")
		if !ok {
			return errors.New("SMTP AUTH extension not supported by server.")
		}
		auth := pickSMTPAuth(strings.Fields(mechs), user, pw)
		if auth == nil {
			return errors.New("No suitable authentication method found.")
		}
		if err := c.Auth(auth); err != nil {
			return err
		}
	}
	sender := str(cfg["sender"])
	from, err := mail.ParseAddress(sender)
	if err != nil {
		return fmt.Errorf("invalid sender %q", sender)
	}
	if err := c.Mail(from.Address); err != nil {
		return err
	}
	l, _ := cfg["recipients"].([]any)
	var to []string
	for _, r := range l {
		a, err := mail.ParseAddress(str(r))
		if err != nil {
			return fmt.Errorf("invalid recipient %q", r)
		}
		if err := c.Rcpt(a.Address); err != nil {
			return err
		}
		to = append(to, str(r))
	}
	w, err := c.Data()
	if err != nil {
		return err
	}
	if _, err := w.Write(emailMessage(sender, to, subject, body, local)); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	return c.Quit()
}

// emailMessage builds a UTF-8 plain-text message.
func emailMessage(from string, to []string, subject, body, host string) []byte {
	var b strings.Builder
	id := make([]byte, 8)
	rand.Read(id)
	now := time.Now()
	fmt.Fprintf(&b, "From: %s\r\n", from)
	fmt.Fprintf(&b, "To: %s\r\n", strings.Join(to, ", "))
	fmt.Fprintf(&b, "Subject: %s\r\n", mime.QEncoding.Encode("utf-8", subject))
	fmt.Fprintf(&b, "Date: %s\r\n", now.Format(time.RFC1123Z))
	fmt.Fprintf(&b, "Message-ID: <%d.%s@%s>\r\n", now.UnixNano(), hex.EncodeToString(id), host)
	b.WriteString("MIME-Version: 1.0\r\nContent-Type: text/plain; charset=\"utf-8\"\r\nContent-Transfer-Encoding: quoted-printable\r\n\r\n")
	qp := quotedprintable.NewWriter(&b)
	qp.Write([]byte(body))
	qp.Close()
	return []byte(b.String()) // the DATA writer ends the last line
}

// pickSMTPAuth picks the login mechanism Python's smtplib would among those
// the server offers: CRAM-MD5, then PLAIN, then LOGIN. As in AWX, PLAIN and
// LOGIN are used without TLS when the template does not ask for it.
func pickSMTPAuth(offered []string, user, pw string) smtp.Auth {
	has := map[string]bool{}
	for _, m := range offered {
		has[strings.ToUpper(m)] = true
	}
	switch {
	case has["CRAM-MD5"]:
		return smtp.CRAMMD5Auth(user, pw)
	case has["PLAIN"]:
		return plainAuth{user, pw}
	case has["LOGIN"]:
		return &loginAuth{user: user, pw: pw}
	}
	return nil
}

type plainAuth struct{ user, pw string }

func (a plainAuth) Start(*smtp.ServerInfo) (string, []byte, error) {
	return "PLAIN", []byte("\x00" + a.user + "\x00" + a.pw), nil
}

func (a plainAuth) Next(_ []byte, more bool) ([]byte, error) {
	if more {
		return nil, errors.New("unexpected server challenge")
	}
	return nil, nil
}

type loginAuth struct {
	user, pw string
	step     int
}

func (a *loginAuth) Start(*smtp.ServerInfo) (string, []byte, error) {
	return "LOGIN", nil, nil
}

func (a *loginAuth) Next(_ []byte, more bool) ([]byte, error) {
	if !more {
		return nil, nil
	}
	a.step++
	switch a.step {
	case 1:
		return []byte(a.user), nil
	case 2:
		return []byte(a.pw), nil
	}
	return nil, errors.New("unexpected server challenge")
}
