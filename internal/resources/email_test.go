package resources

import (
	"mime"
	"net/mail"
	"strings"
	"testing"
)

func TestEmailMessageFoldsHeaders(t *testing.T) {
	subject := "Tâche « déploiement » — 日本語のテスト: " + strings.Repeat("éàü ", 40) + "fin"
	to := []string{"ops@example.com", "dev@example.com", strings.Repeat("x", 70) + "@example.com"}
	raw := string(emailMessage("AWX <awx@example.com>", to, subject, "body", "host"))
	lines := strings.Split(raw, "\r\n")
	for _, l := range lines {
		// Only a single word passes 78: an encoded word or an address.
		words := strings.Fields(l)
		if len(words) > 0 && strings.HasSuffix(words[0], ":") {
			words = words[1:]
		}
		if len(l) > 998 || len(l) > 78 && len(words) > 1 {
			t.Errorf("line of %d bytes: %.80q", len(l), l)
		}
	}
	if n := strings.Count(raw, "?=\r\n =?"); n < 10 {
		t.Errorf("subject folded %d times: %q", n, raw)
	}
	m, err := mail.ReadMessage(strings.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	got, err := new(mime.WordDecoder).DecodeHeader(m.Header.Get("Subject"))
	if err != nil || got != subject {
		t.Errorf("subject %q (%v), want %q", got, err, subject)
	}
	if got := m.Header.Get("To"); got != strings.Join(to, ", ") {
		t.Errorf("to %q", got)
	}
	if got := m.Header.Get("Subject"); !strings.Contains(raw, "Subject: =?utf-8?q?") || got == "" {
		t.Errorf("subject header %q", got)
	}
	short := string(emailMessage("AWX <awx@example.com>", to[:1], "Job #1 'site' successful", "body", "host"))
	if !strings.Contains(short, "\r\nSubject: Job #1 'site' successful\r\n") {
		t.Errorf("short subject folded: %q", short)
	}
}
