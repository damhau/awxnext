package auth

import (
	"crypto/x509"
	"crypto/x509/pkix"
	"testing"
	"time"
)

// A certificate is checked against now, and warned about 30 days before it
// expires.
func TestCertStatus(t *testing.T) {
	now := time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)
	cert := func(from, to time.Time) *x509.Certificate {
		return &x509.Certificate{Subject: pkix.Name{CommonName: "idp"}, NotBefore: from, NotAfter: to}
	}
	for _, c := range []struct {
		cert         *x509.Certificate
		status, text string
	}{
		{cert(now.AddDate(0, 0, -1), now.AddDate(1, 0, 0)), "ok", "CN=idp, valid until 2027-10-31."},
		{cert(now.AddDate(0, 0, -1), now.AddDate(0, 0, 10)), "warning", "CN=idp expires on 2026-11-10, in 10 days."},
		{cert(now.AddDate(0, 0, -1), now.Add(3*time.Hour)), "warning", "CN=idp expires on 2026-10-31, in less than a day."},
		{cert(now.AddDate(-1, 0, 0), now.AddDate(0, 0, -2)), "failed", "CN=idp expired on 2026-10-29."},
		{cert(now.AddDate(0, 0, 3), now.AddDate(1, 0, 0)), "failed", "CN=idp is not valid before 2026-11-03."},
		{&x509.Certificate{NotBefore: now.AddDate(0, 0, -1), NotAfter: now.AddDate(1, 0, 0)}, "ok", "The certificate, valid until 2027-10-31."},
	} {
		if status, text := certStatus(c.cert, now); status != c.status || text != c.text {
			t.Errorf("certStatus: %s %q, want %s %q", status, text, c.status, c.text)
		}
	}
}
