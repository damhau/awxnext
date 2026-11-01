// Package netguard keeps requests to addresses a user chooses, such as the
// URLs of external credential lookups, away from the server's own network:
// loopback, link-local and unspecified addresses and cloud metadata
// services are refused unless an administrator allows them. It also matches
// URLs against an administrator's list of hosts.
package netguard

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// metadataAddrs are cloud metadata services outside the refused ranges:
// AWS's IPv6 endpoint and Alibaba Cloud's. The others (AWS, Azure, Google,
// Oracle) are at the link-local 169.254.169.254.
var metadataAddrs = []netip.Addr{netip.MustParseAddr("fd00:ec2::254"), netip.MustParseAddr("100.100.100.200")}

// Refused reports whether a is a loopback, link-local, unspecified (0.0.0.0/8,
// ::) or cloud metadata address.
func Refused(a netip.Addr) bool {
	a = a.Unmap().WithZone("")
	if a.IsLoopback() || a.IsLinkLocalUnicast() || a.IsLinkLocalMulticast() || a.IsInterfaceLocalMulticast() || a.IsUnspecified() {
		return true
	}
	if a.Is4() && a.As4()[0] == 0 {
		return true
	}
	for _, m := range metadataAddrs {
		if a == m {
			return true
		}
	}
	return false
}

// Allowed lists refused addresses an administrator allows. Each entry is an
// address (127.0.0.1, ::1), a network (127.0.0.0/8) or an address and a
// port (127.0.0.1:8200, [::1]:8200).
type Allowed []allowRule

type allowRule struct {
	prefix netip.Prefix
	port   uint16 // 0: any
}

// ParseAllowed parses the entries of an Allowed list. It returns the valid
// ones, and an error naming the first that is not.
func ParseAllowed(entries []string) (Allowed, error) {
	var out Allowed
	var first error
	for _, e := range entries {
		r, err := parseAllowRule(strings.TrimSpace(e))
		if err != nil {
			if first == nil {
				first = err
			}
			continue
		}
		out = append(out, r)
	}
	return out, first
}

func parseAllowRule(e string) (allowRule, error) {
	if p, err := netip.ParsePrefix(e); err == nil {
		return allowRule{prefix: p.Masked()}, nil
	}
	if a, err := netip.ParseAddr(e); err == nil {
		a = a.Unmap()
		return allowRule{prefix: netip.PrefixFrom(a, a.BitLen())}, nil
	}
	if ap, err := netip.ParseAddrPort(e); err == nil && ap.Port() != 0 {
		a := ap.Addr().Unmap()
		return allowRule{prefix: netip.PrefixFrom(a, a.BitLen()), port: ap.Port()}, nil
	}
	return allowRule{}, fmt.Errorf("%q is not an address (127.0.0.1), a network (127.0.0.0/8) or an address and port (127.0.0.1:8200)", e)
}

func (l Allowed) allows(ap netip.AddrPort) bool {
	a := ap.Addr().Unmap().WithZone("")
	for _, r := range l {
		if r.prefix.Contains(a) && (r.port == 0 || r.port == ap.Port()) {
			return true
		}
	}
	return false
}

// RefusedError is a connection Guard refused.
type RefusedError struct {
	Addr    netip.AddrPort
	Setting string
}

func (e *RefusedError) Error() string {
	msg := fmt.Sprintf("%s is a loopback, link-local or cloud metadata address, which is refused", e.Addr.Addr())
	if e.Setting != "" {
		msg += " unless an administrator allows it in " + e.Setting
	}
	return msg
}

// RetryableError tells clients that retry, such as the AWS SDK's, that
// trying again will not help.
func (e *RefusedError) RetryableError() bool { return false }

// Guard checks the addresses a client connects to.
type Guard struct {
	Allowed Allowed
	// Setting names the setting that allows more, for error messages.
	Setting string
}

// Check refuses a connection to ap when its address is refused and not
// allowed.
func (g Guard) Check(ap netip.AddrPort) error {
	if !Refused(ap.Addr()) || g.Allowed.allows(ap) {
		return nil
	}
	return &RefusedError{Addr: ap, Setting: g.Setting}
}

// DialContext connects as a net.Dialer does, refusing what Check refuses.
// The check is made on the address being connected to, after name
// resolution, so a name cannot pass with one address and connect to another.
func (g Guard) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	d := net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second, Control: func(_, address string, _ syscall.RawConn) error {
		ap, err := netip.ParseAddrPort(address)
		if err != nil {
			return err
		}
		return g.Check(ap)
	}}
	return d.DialContext(ctx, network, address)
}

// CheckURL refuses a URL whose host is a refused address written as such,
// or localhost. DialContext checks every connection; this also covers
// requests sent through a proxy, which connects to the host itself.
func (g Guard) CheckURL(u *url.URL) error {
	host := strings.TrimSuffix(strings.ToLower(u.Hostname()), ".")
	var a netip.Addr
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		a = netip.AddrFrom4([4]byte{127, 0, 0, 1})
	} else if p, err := netip.ParseAddr(host); err == nil {
		a = p
	} else {
		return nil
	}
	return g.Check(netip.AddrPortFrom(a, port(u)))
}

// Transport makes t connect through DialContext, and returns a round
// tripper that checks each request's URL, those of redirects included, with
// CheckURL and then check, when not nil, before t sends it.
func (g Guard) Transport(t *http.Transport, check func(*url.URL) error) http.RoundTripper {
	t.DialContext = g.DialContext
	return checked{t, func(u *url.URL) error {
		if err := g.CheckURL(u); err != nil {
			return err
		}
		if check != nil {
			return check(u)
		}
		return nil
	}}
}

type checked struct {
	next  http.RoundTripper
	check func(*url.URL) error
}

func (c checked) RoundTrip(req *http.Request) (*http.Response, error) {
	if err := c.check(req.URL); err != nil {
		if req.Body != nil {
			req.Body.Close()
		}
		return nil, err
	}
	return c.next.RoundTrip(req)
}

// port is u's port, or its scheme's default.
func port(u *url.URL) uint16 {
	if p, err := strconv.ParseUint(u.Port(), 10, 16); err == nil {
		return uint16(p)
	}
	if strings.EqualFold(u.Scheme, "http") {
		return 80
	}
	return 443
}

// Hosts is an administrator's list of hosts. Each entry is a name or an
// address, or *.example.com for the names under example.com, with an
// optional port and an optional scheme: vault.example.com,
// vault.example.com:8200, http://vault.vault.svc:8200, *.vault.azure.net.
// Without a scheme an entry matches https only; without a port, any port.
type Hosts []hostRule

type hostRule struct {
	scheme string
	host   string // lower case; for a wildcard, the domain under which names match
	any    bool   // *.host
	port   string // "": any
}

// ParseHosts parses the entries of a Hosts list. It returns the valid ones,
// and an error naming the first that is not.
func ParseHosts(entries []string) (Hosts, error) {
	var out Hosts
	var first error
	for _, e := range entries {
		r, err := parseHostRule(strings.TrimSpace(e))
		if err != nil {
			if first == nil {
				first = err
			}
			continue
		}
		out = append(out, r)
	}
	return out, first
}

func parseHostRule(e string) (hostRule, error) {
	bad := fmt.Errorf("%q is not a host (vault.example.com), a host and port (vault.example.com:8200) or *.example.com, with http:// before it for plain HTTP", e)
	r := hostRule{scheme: "https"}
	rest := strings.ToLower(e)
	if i := strings.Index(rest, "://"); i >= 0 {
		r.scheme, rest = rest[:i], rest[i+3:]
		if r.scheme != "https" && r.scheme != "http" {
			return r, bad
		}
	}
	rest = strings.TrimSuffix(rest, "/")
	if rest == "" || strings.ContainsAny(rest, "/?#@ ") {
		return r, bad
	}
	host := rest
	if h, p, err := net.SplitHostPort(rest); err == nil {
		n, err := strconv.ParseUint(p, 10, 16)
		if err != nil || n == 0 {
			return r, bad
		}
		host, r.port = h, strconv.FormatUint(n, 10)
	} else if strings.HasPrefix(rest, "[") && strings.HasSuffix(rest, "]") {
		host = rest[1 : len(rest)-1]
	}
	if a, err := netip.ParseAddr(host); err == nil {
		r.host = a.Unmap().String()
		return r, nil
	}
	host = strings.TrimSuffix(host, ".")
	if strings.HasPrefix(host, "*.") {
		r.any, host = true, host[2:]
	}
	if host == "" || strings.ContainsAny(host, "*:[]") {
		return r, bad
	}
	r.host = host
	return r, nil
}

// Match reports whether u's scheme, host and port match an entry.
func (l Hosts) Match(u *url.URL) bool {
	scheme := strings.ToLower(u.Scheme)
	host := strings.TrimSuffix(strings.ToLower(u.Hostname()), ".")
	if a, err := netip.ParseAddr(host); err == nil {
		host = a.Unmap().String()
	}
	p := strconv.Itoa(int(port(u)))
	for _, r := range l {
		if r.scheme != scheme || (r.port != "" && r.port != p) {
			continue
		}
		if (!r.any && host == r.host) || (r.any && strings.HasSuffix(host, "."+r.host)) {
			return true
		}
	}
	return false
}

// Origin is u's scheme, host and port, as an administrator would list them.
func Origin(u *url.URL) string {
	return strings.ToLower(u.Scheme) + "://" + strings.ToLower(u.Host)
}
