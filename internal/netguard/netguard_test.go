package netguard

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strings"
	"testing"
)

func TestRefused(t *testing.T) {
	for _, a := range []string{"127.0.0.1", "127.8.9.10", "::1", "169.254.169.254", "169.254.1.1", "fe80::1", "fe80::1%eth0",
		"0.0.0.0", "0.1.2.3", "::", "::ffff:127.0.0.1", "::ffff:169.254.169.254", "fd00:ec2::254", "100.100.100.200", "ff02::1"} {
		if !Refused(netip.MustParseAddr(a)) {
			t.Errorf("%s should be refused", a)
		}
	}
	for _, a := range []string{"10.0.0.1", "172.16.0.1", "192.168.1.1", "8.8.8.8", "100.64.0.1", "2001:db8::1", "fd00::1", "::ffff:10.0.0.1"} {
		if Refused(netip.MustParseAddr(a)) {
			t.Errorf("%s should not be refused", a)
		}
	}
}

func TestAllowed(t *testing.T) {
	l, err := ParseAllowed([]string{" 127.0.0.1 ", "169.254.0.0/16", "[::1]:8200", "::ffff:127.0.0.2"})
	if err != nil || len(l) != 4 {
		t.Fatalf("parse: %v %v", l, err)
	}
	g := Guard{Allowed: l, Setting: "THE_SETTING"}
	for _, ap := range []string{"127.0.0.1:1", "169.254.169.254:80", "[::1]:8200", "127.0.0.2:5", "[::ffff:127.0.0.1]:9", "10.0.0.1:22"} {
		if err := g.Check(netip.MustParseAddrPort(ap)); err != nil {
			t.Errorf("%s: %v", ap, err)
		}
	}
	for _, ap := range []string{"127.0.0.3:1", "[::1]:8201", "[fe80::1]:80"} {
		err := g.Check(netip.MustParseAddrPort(ap))
		var re *RefusedError
		if !errors.As(err, &re) || !strings.Contains(err.Error(), "unless an administrator allows it in THE_SETTING") {
			t.Errorf("%s: %v", ap, err)
		}
	}
	for _, bad := range []string{"localhost", "127.0.0.1:0", "127.0.0.1/33", "*", "127.0.0.1:8200:1"} {
		if valid, err := ParseAllowed([]string{"127.0.0.1", bad}); err == nil || !strings.Contains(err.Error(), bad) || len(valid) != 1 {
			t.Errorf("%q: %v %v", bad, valid, err)
		}
	}
}

func TestCheckURL(t *testing.T) {
	g := Guard{}
	for _, u := range []string{"http://127.0.0.1:8200/v1", "https://LOCALHOST", "http://api.localhost./", "http://[::1]/", "http://169.254.169.254/metadata", "http://[::ffff:7f00:1]/", "http://0.0.0.0:80"} {
		if err := g.CheckURL(mustURL(u)); err == nil {
			t.Errorf("%s should be refused", u)
		}
	}
	for _, u := range []string{"https://vault.example.com", "http://10.0.0.5:8200", "https://[2001:db8::1]/"} {
		if err := g.CheckURL(mustURL(u)); err != nil {
			t.Errorf("%s: %v", u, err)
		}
	}
	// The port counts: 80 and 443 when the URL gives none.
	g = Guard{Allowed: mustAllowed(t, "127.0.0.1:80")}
	if g.CheckURL(mustURL("http://127.0.0.1/")) != nil || g.CheckURL(mustURL("https://127.0.0.1/")) == nil {
		t.Error("default ports")
	}
}

// The check is made where the connection is made: on the address a name
// resolves to, and with a proxy, on the URL.
func TestTransport(t *testing.T) {
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits++ }))
	defer srv.Close()
	_, port, _ := net.SplitHostPort(srv.Listener.Addr().String())

	// An address, or a name that resolves to it, is refused when dialled.
	var re *RefusedError
	for _, addr := range []string{srv.Listener.Addr().String(), net.JoinHostPort("localhost", port)} {
		if _, err := (Guard{}).DialContext(context.Background(), "tcp", addr); !errors.As(err, &re) {
			t.Fatalf("dial %s: %v", addr, err)
		}
	}
	client := func(g Guard, check func(*url.URL) error) *http.Client {
		return &http.Client{Transport: g.Transport(&http.Transport{}, check)}
	}
	// Before any connection, with or without a proxy.
	if _, err := client(Guard{}, nil).Get("http://localhost.:" + port); !errors.As(err, &re) {
		t.Fatalf("name: %v", err)
	}
	if _, err := client(Guard{Allowed: mustAllowed(t, "127.0.0.1", "::1")}, nil).Get(srv.URL); err != nil || hits != 1 {
		t.Fatalf("allowed: %v %d", err, hits)
	}
	// The extra check sees every URL and stops the request before it is sent.
	no := errors.New("not this one")
	if _, err := client(Guard{Allowed: mustAllowed(t, "127.0.0.1")}, func(*url.URL) error { return no }).Get(srv.URL); !errors.Is(err, no) || hits != 1 {
		t.Fatalf("check: %v %d", err, hits)
	}
}

func TestHosts(t *testing.T) {
	l, err := ParseHosts([]string{"vault.example.com", "Vault2.Example.com:8200", "*.vault.azure.net", "http://vault.vault.svc:8200/", "10.0.0.5", "[2001:db8::1]:8200", "http://[::1]"})
	if err != nil || len(l) != 7 {
		t.Fatalf("parse: %v %v", l, err)
	}
	for u, want := range map[string]bool{
		"https://vault.example.com/v1/x":           true,
		"https://VAULT.example.com.:443":           true,
		"https://vault.example.com:8200":           true,
		"http://vault.example.com":                 false, // https only without a scheme
		"https://a.vault.example.com":              false,
		"https://vault2.example.com:8200":          true,
		"https://vault2.example.com":               false,
		"https://kv1.vault.azure.net/secrets/x":    true,
		"https://a.b.vault.azure.net":              true,
		"https://vault.azure.net":                  false, // only the names under it
		"https://evilvault.azure.net":              false,
		"https://kv1.vault.azure.net.evil.com":     false,
		"https://kv1.vault.azure.net@evil.com/":    false,
		"http://vault.vault.svc:8200/v1/auth":      true,
		"https://vault.vault.svc:8200":             false,
		"http://vault.vault.svc":                   false,
		"https://10.0.0.5:8200":                    true,
		"https://[::ffff:10.0.0.5]":                true,
		"https://[2001:db8::1]:8200":               true,
		"https://[2001:db8::1]":                    false,
		"http://[::1]:9":                           true,
		"ftp://vault.example.com":                  false,
		"https://vault.example.com.evil.com":       false,
		"https://user:pass@vault.example.com/path": true,
	} {
		if got := l.Match(mustURL(u)); got != want {
			t.Errorf("%s: %v, want %v", u, got, want)
		}
	}
	for _, bad := range []string{"", "ftp://vault", "vault.example.com/v1", "*", "*.", "a.*.com", "vault:0", "vault:x", "user@vault", "https://"} {
		if valid, err := ParseHosts([]string{"ok.example.com", bad}); err == nil || len(valid) != 1 {
			t.Errorf("%q: %v %v", bad, valid, err)
		}
	}
	if Origin(mustURL("HTTPS://Vault.Example.com:8200/v1")) != "https://vault.example.com:8200" {
		t.Error("origin")
	}
}

func mustURL(s string) *url.URL {
	u, err := url.Parse(s)
	if err != nil {
		panic(err)
	}
	return u
}

func mustAllowed(t *testing.T, entries ...string) Allowed {
	t.Helper()
	l, err := ParseAllowed(entries)
	if err != nil {
		t.Fatal(err)
	}
	return l
}
