package test

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"io"
	"math/big"
	"mime"
	"mime/quotedprintable"
	"net"
	"net/mail"
	"net/textproto"
	"strings"
	"testing"
	"time"
)

// smtpServer is a small SMTP server for tests: it offers STARTTLS when it has
// a certificate (or speaks TLS from the start when implicit), requires a
// login when it offers AUTH mechanisms, and hands each message to received.
type smtpServer struct {
	tls      *tls.Config
	implicit bool
	auth     []string // offered mechanisms (PLAIN, LOGIN); login required when set
	user, pw string
	silent   bool // accepts connections and never answers

	port     int
	received chan smtpMsg
}

type smtpMsg struct {
	From, User, Mech string
	To               []string
	TLS              bool
	Header           mail.Header
	Body             string // decoded, with \n line ends
}

func startSMTP(t *testing.T, s *smtpServer) *smtpServer {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s.port = ln.Addr().(*net.TCPAddr).Port
	if s.implicit {
		ln = tls.NewListener(ln, s.tls)
	}
	s.received = make(chan smtpMsg, 16)
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go s.serve(c)
		}
	}()
	return s
}

// next waits for the next message.
func (s *smtpServer) next(t *testing.T) smtpMsg {
	t.Helper()
	select {
	case m := <-s.received:
		return m
	case <-time.After(30 * time.Second):
		t.Fatal("no email received")
	}
	return smtpMsg{}
}

func (s *smtpServer) serve(conn net.Conn) {
	defer conn.Close()
	if s.silent {
		io.Copy(io.Discard, conn)
		return
	}
	tp := textproto.NewConn(conn)
	secure := s.implicit
	var user, mech string
	var msg smtpMsg
	b64 := base64.StdEncoding
	tp.PrintfLine("220 test.example ESMTP")
	for {
		line, err := tp.ReadLine()
		if err != nil {
			return
		}
		cmd, arg, _ := strings.Cut(line, " ")
		switch strings.ToUpper(cmd) {
		case "EHLO", "HELO":
			lines := []string{"test.example"}
			if s.tls != nil && !secure {
				lines = append(lines, "STARTTLS")
			}
			if len(s.auth) > 0 {
				lines = append(lines, "AUTH "+strings.Join(s.auth, " "))
			}
			for i, l := range lines {
				sep := "-"
				if i == len(lines)-1 {
					sep = " "
				}
				tp.PrintfLine("250%s%s", sep, l)
			}
		case "STARTTLS":
			tp.PrintfLine("220 Ready to start TLS")
			tc := tls.Server(conn, s.tls)
			if tc.Handshake() != nil {
				return
			}
			conn, tp, secure = tc, textproto.NewConn(tc), true
		case "AUTH":
			m, initial, _ := strings.Cut(arg, " ")
			var u, p string
			switch strings.ToUpper(m) {
			case "PLAIN":
				if initial == "" {
					tp.PrintfLine("334 ")
					initial, _ = tp.ReadLine()
				}
				raw, _ := b64.DecodeString(initial)
				if parts := strings.Split(string(raw), "\x00"); len(parts) == 3 {
					u, p = parts[1], parts[2]
				}
			case "LOGIN":
				tp.PrintfLine("334 %s", b64.EncodeToString([]byte("Username:")))
				l, _ := tp.ReadLine()
				raw, _ := b64.DecodeString(l)
				u = string(raw)
				tp.PrintfLine("334 %s", b64.EncodeToString([]byte("Password:")))
				l, _ = tp.ReadLine()
				raw, _ = b64.DecodeString(l)
				p = string(raw)
			default:
				tp.PrintfLine("504 Unrecognized authentication type")
				continue
			}
			if u == s.user && p == s.pw {
				user, mech = u, strings.ToUpper(m)
				tp.PrintfLine("235 2.7.0 Authentication successful")
			} else {
				tp.PrintfLine("535 5.7.8 Authentication credentials invalid")
			}
		case "MAIL":
			if len(s.auth) > 0 && user == "" {
				tp.PrintfLine("530 5.7.0 Authentication required")
				continue
			}
			msg = smtpMsg{From: angle(arg), User: user, Mech: mech, TLS: secure}
			tp.PrintfLine("250 OK")
		case "RCPT":
			msg.To = append(msg.To, angle(arg))
			tp.PrintfLine("250 OK")
		case "DATA":
			tp.PrintfLine("354 End data with <CR><LF>.<CR><LF>")
			data, err := tp.ReadDotBytes()
			if err != nil {
				return
			}
			m, err := mail.ReadMessage(strings.NewReader(string(data)))
			if err == nil {
				msg.Header = m.Header
				var body []byte
				if strings.EqualFold(m.Header.Get("Content-Transfer-Encoding"), "quoted-printable") {
					body, _ = io.ReadAll(quotedprintable.NewReader(m.Body))
				} else {
					body, _ = io.ReadAll(m.Body)
				}
				// The data ends with the CRLF of its last line.
				msg.Body = strings.TrimSuffix(strings.ReplaceAll(string(body), "\r\n", "\n"), "\n")
			}
			s.received <- msg
			tp.PrintfLine("250 OK queued")
		case "RSET", "NOOP":
			tp.PrintfLine("250 OK")
		case "QUIT":
			tp.PrintfLine("221 Bye")
			return
		default:
			tp.PrintfLine("502 Command not implemented")
		}
	}
}

func angle(arg string) string {
	_, rest, _ := strings.Cut(arg, "<")
	addr, _, _ := strings.Cut(rest, ">")
	return addr
}

// subject decodes a message's subject.
func (m smtpMsg) subject() string {
	s, err := new(mime.WordDecoder).DecodeHeader(m.Header.Get("Subject"))
	if err != nil {
		return m.Header.Get("Subject")
	}
	return s
}

// selfSignedTLS returns a server configuration for 127.0.0.1 and the pool
// that trusts it.
func selfSignedTLS(t *testing.T) (*tls.Config, *x509.CertPool) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "test.example"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, DNSNames: []string{"localhost"},
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IsCA: true, BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, _ := x509.ParseCertificate(der)
	pool := x509.NewCertPool()
	pool.AddCert(cert)
	return &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}}, pool
}
