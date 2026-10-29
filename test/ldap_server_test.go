package test

import (
	"crypto/tls"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	ber "github.com/go-asn1-ber/asn1-ber"
	"github.com/go-ldap/ldap/v3"
	"github.com/jimlambrt/gldap"
)

// ldapDir is an LDAP directory for tests: entries with attributes and
// passwords, searched with real filters and scopes (gldap's own test
// directory only looks for parts of the filter in DNs).
type ldapDir struct {
	t       *testing.T
	mu      sync.Mutex
	entries []*ldapEntry
	addr    string
	ca      *testCA
	binds   []string
}

type ldapEntry struct {
	dn       string
	attrs    map[string][]string
	password string
}

// newLDAPDir starts a directory; with ldaps it speaks TLS from the start,
// otherwise plain LDAP that StartTLS upgrades.
func newLDAPDir(t *testing.T, ldaps bool) *ldapDir {
	t.Helper()
	d := &ldapDir{t: t, ca: newTestCA(t)}
	_, _, pair := d.ca.issue(t, "127.0.0.1", true)
	serverTLS := &tls.Config{Certificates: []tls.Certificate{pair}, MinVersion: tls.VersionTLS12}
	srv, err := gldap.NewServer()
	if err != nil {
		t.Fatal(err)
	}
	mux, _ := gldap.NewMux()
	mux.Bind(d.bind)
	mux.Search(d.search)
	mux.ExtendedOperation(func(w *gldap.ResponseWriter, r *gldap.Request) {
		res := r.NewExtendedResponse(gldap.WithResponseCode(gldap.ResultSuccess))
		res.SetResponseName(gldap.ExtendedOperationStartTLS)
		w.Write(res)
		r.StartTLS(serverTLS)
	}, gldap.ExtendedOperationStartTLS)
	srv.Router(mux)
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	d.addr = l.Addr().String()
	l.Close()
	var opts []gldap.Option
	if ldaps {
		opts = append(opts, gldap.WithTLSConfig(serverTLS))
	}
	go srv.Run(d.addr, opts...)
	t.Cleanup(func() { srv.Stop() })
	for deadline := time.Now().Add(5 * time.Second); !srv.Ready() && time.Now().Before(deadline); {
		time.Sleep(10 * time.Millisecond)
	}
	return d
}

func (d *ldapDir) url(scheme string) string { return scheme + "://" + d.addr }

func (d *ldapDir) add(dn string, attrs map[string][]string, password string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.entries = append(d.entries, &ldapEntry{dn: dn, attrs: attrs, password: password})
}

// remove takes a value out of an attribute, such as a member out of a group.
func (d *ldapDir) remove(dn, attr, value string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, e := range d.entries {
		if sameDNs(e.dn, dn) {
			var kept []string
			for _, v := range e.attrs[attr] {
				if !strings.EqualFold(v, value) {
					kept = append(kept, v)
				}
			}
			e.attrs[attr] = kept
		}
	}
}

func sameDNs(a, b string) bool {
	da, err1 := ldap.ParseDN(a)
	db, err2 := ldap.ParseDN(b)
	return err1 == nil && err2 == nil && da.EqualFold(db)
}

func (d *ldapDir) bind(w *gldap.ResponseWriter, r *gldap.Request) {
	resp := r.NewBindResponse(gldap.WithResponseCode(gldap.ResultInvalidCredentials))
	defer w.Write(resp)
	m, err := r.GetSimpleBindMessage()
	if err != nil {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	d.binds = append(d.binds, m.UserName)
	if m.UserName == "" && m.Password == "" {
		resp.SetResultCode(gldap.ResultSuccess)
		return
	}
	for _, e := range d.entries {
		if sameDNs(e.dn, m.UserName) && e.password != "" && string(m.Password) == e.password {
			resp.SetResultCode(gldap.ResultSuccess)
		}
	}
}

func (d *ldapDir) inScope(e *ldapEntry, base string, scope gldap.Scope) bool {
	de, err1 := ldap.ParseDN(e.dn)
	db, err2 := ldap.ParseDN(base)
	if err1 != nil || err2 != nil {
		return false
	}
	switch scope {
	case gldap.BaseObject:
		return de.EqualFold(db)
	case gldap.SingleLevel:
		return db.AncestorOfFold(de) && len(de.RDNs) == len(db.RDNs)+1
	}
	return de.EqualFold(db) || db.AncestorOfFold(de)
}

func (d *ldapDir) search(w *gldap.ResponseWriter, r *gldap.Request) {
	done := r.NewSearchDoneResponse(gldap.WithResponseCode(gldap.ResultSuccess))
	defer w.Write(done)
	m, err := r.GetSearchMessage()
	if err != nil {
		return
	}
	f, err := ldap.CompileFilter(m.Filter)
	if err != nil {
		done.SetResultCode(gldap.ResultOperationsError)
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	exists := false
	for _, e := range d.entries {
		exists = exists || d.inScope(e, m.BaseDN, gldap.WholeSubtree)
	}
	if !exists {
		done.SetResultCode(gldap.ResultNoSuchObject)
		return
	}
	for _, e := range d.entries {
		if d.inScope(e, m.BaseDN, m.Scope) && matchFilter(f, e) {
			res := r.NewSearchResponseEntry(e.dn)
			for name, values := range e.attrs {
				res.AddAttribute(name, values)
			}
			w.Write(res)
		}
	}
}

func (e *ldapEntry) values(attr string) []string {
	for name, vs := range e.attrs {
		if strings.EqualFold(name, attr) {
			return vs
		}
	}
	return nil
}

func equalValues(a, b string) bool { return strings.EqualFold(a, b) || sameDNs(a, b) }

// matchFilter evaluates a compiled filter: and, or, not, equality,
// presence and substrings, case-insensitively, DN values as DNs.
func matchFilter(p *ber.Packet, e *ldapEntry) bool {
	str := func(q *ber.Packet) string { return ber.DecodeString(q.Data.Bytes()) }
	switch p.Tag {
	case ldap.FilterAnd:
		for _, c := range p.Children {
			if !matchFilter(c, e) {
				return false
			}
		}
		return true
	case ldap.FilterOr:
		for _, c := range p.Children {
			if matchFilter(c, e) {
				return true
			}
		}
		return false
	case ldap.FilterNot:
		return !matchFilter(p.Children[0], e)
	case ldap.FilterPresent:
		return len(e.values(str(p))) > 0
	case ldap.FilterEqualityMatch:
		for _, v := range e.values(str(p.Children[0])) {
			if equalValues(v, str(p.Children[1])) {
				return true
			}
		}
		return false
	case ldap.FilterSubstrings:
		for _, v := range e.values(str(p.Children[0])) {
			rest, ok := strings.ToLower(v), true
			for i, part := range p.Children[1].Children {
				s := strings.ToLower(str(part))
				switch {
				case part.Tag == ldap.FilterSubstringsInitial && i == 0:
					ok = ok && strings.HasPrefix(rest, s)
					rest = strings.TrimPrefix(rest, s)
				case part.Tag == ldap.FilterSubstringsFinal:
					ok = ok && strings.HasSuffix(rest, s)
				default:
					idx := strings.Index(rest, s)
					ok = ok && idx >= 0
					if idx >= 0 {
						rest = rest[idx+len(s):]
					}
				}
			}
			if ok {
				return true
			}
		}
		return false
	}
	return false
}

// TestLDAPDirFilters checks the test directory itself.
func TestLDAPDirFilters(t *testing.T) {
	e := &ldapEntry{dn: "uid=alice,ou=people,dc=example,dc=org", attrs: map[string][]string{"uid": {"alice"}, "objectClass": {"inetOrgPerson"},
		"member": {"CN=Bob,OU=People,DC=example,DC=org"}, "mail": {"alice@example.org"}}}
	for f, want := range map[string]bool{
		"(uid=ALICE)": true, "(&(objectClass=inetOrgPerson)(uid=alice))": true, "(|(uid=bob)(mail=*@example.org))": true,
		"(!(uid=alice))": false, "(member=cn=bob,ou=people,dc=example,dc=org)": true, "(mail=al*ce@*.org)": true, "(sn=*)": false,
	} {
		p, err := ldap.CompileFilter(f)
		if err != nil {
			t.Fatal(err)
		}
		if matchFilter(p, e) != want {
			t.Errorf("%s: %v", f, !want)
		}
	}
}
