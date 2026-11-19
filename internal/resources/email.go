package resources

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"fmt"
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

// emailFields are AWX's email notification_configuration fields.
var emailFields = []configField{
	{name: "host", typ: "string"},
	{name: "port", typ: "int"},
	{name: "username", typ: "string", def: ""},
	{name: "password", typ: "password", def: ""},
	{name: "use_tls", typ: "bool", def: false},
	{name: "use_ssl", typ: "bool", def: false},
	{name: "sender", typ: "string"},
	{name: "recipients", typ: "list"},
	{name: "timeout", typ: "int", def: float64(30)},
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
	out, err := checkConfigFields("email", emailFields, cfg)
	if err != nil {
		return nil, err
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
	if err := sealSecret(c, out, old, "password"); err != nil {
		return nil, err
	}
	return out, nil
}

// listRecipients lists a configuration's recipients as AWX shows them on a
// notification: ['a@example.com', 'b@example.com'].
func listRecipients(cfg map[string]any, key string) string {
	l, _ := cfg[key].([]any)
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
	foldHeader(&b, "From", from)
	foldHeader(&b, "To", strings.Join(to, ", "))
	foldHeader(&b, "Subject", mime.QEncoding.Encode("utf-8", subject))
	fmt.Fprintf(&b, "Date: %s\r\n", now.Format(time.RFC1123Z))
	fmt.Fprintf(&b, "Message-ID: <%d.%s@%s>\r\n", now.UnixNano(), hex.EncodeToString(id), host)
	b.WriteString("MIME-Version: 1.0\r\nContent-Type: text/plain; charset=\"utf-8\"\r\nContent-Transfer-Encoding: quoted-printable\r\n\r\n")
	qp := quotedprintable.NewWriter(&b)
	qp.Write([]byte(body))
	qp.Close()
	return []byte(b.String()) // the DATA writer ends the last line
}

// foldHeader writes a header field folded at its spaces, as Python's email
// package does for AWX, so that a line passes 78 characters only where no
// space allows a break. mime puts all the encoded words of a long non-ASCII
// subject on one line, past the 998 that RFC 5322 allows.
func foldHeader(b *strings.Builder, name, value string) {
	line := name + ":"
	for i, w := range strings.Split(value, " ") {
		if i > 0 && len(line)+1+len(w) > 78 && strings.TrimSpace(line) != "" {
			b.WriteString(line + "\r\n")
			line = ""
		}
		line += " " + w
	}
	b.WriteString(line + "\r\n")
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
