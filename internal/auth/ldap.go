package auth

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/go-ldap/ldap/v3"

	"github.com/damhau/awxnext/internal/settings"
)

// LDAP login as AWX's six LDAP backends do it (django-auth-ldap 4.6 with
// AWX's settings): find the user by a DN template or a search bound as the
// service account, check the password with the user's own bind, check the
// required and denied groups, then set the user's fields, flags and
// organization and team memberships from LDAP at each login.

// ldapSearch is one AWX search: [base DN, scope, filter].
type ldapSearch struct {
	base   string
	scope  int
	filter string
}

var ldapScopes = map[string]int{"SCOPE_BASE": ldap.ScopeBaseObject, "SCOPE_ONELEVEL": ldap.ScopeSingleLevel, "SCOPE_SUBTREE": ldap.ScopeWholeSubtree}

func validateLDAPDN(s string, withUser bool) error {
	dn := s
	if withUser {
		if !strings.Contains(s, "%(user)s") {
			return fmt.Errorf(`DN must include "%%(user)s" placeholder for username: %s`, s)
		}
		dn = strings.ReplaceAll(s, "%(user)s", "USER")
	}
	if _, err := ldap.ParseDN(dn); err != nil {
		return fmt.Errorf("Invalid DN: %s", s)
	}
	return nil
}

var (
	adBindRe  = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9._-]*?\\[A-Za-z0-9 ._-]+?$`)
	upnBindRe = regexp.MustCompile(`^[a-zA-Z0-9_.+-]+@[a-zA-Z0-9-]+\.[a-zA-Z0-9-.]+$`)
)

// validateBindDN accepts a DN, DOMAIN\user or user@domain, as AWX does.
func validateBindDN(s string) error {
	t := strings.TrimSpace(s)
	if t == "" || adBindRe.MatchString(t) || upnBindRe.MatchString(t) {
		return nil
	}
	return validateLDAPDN(s, false)
}

func validateLDAPFilter(s string, withUser bool) error {
	f := strings.TrimSpace(s)
	if f == "" {
		return nil
	}
	if withUser {
		if !strings.Contains(f, "%(user)s") {
			return fmt.Errorf(`DN must include "%%(user)s" placeholder for username: %s`, s)
		}
		f = strings.ReplaceAll(f, "%(user)s", "USER")
	}
	if _, err := ldap.CompileFilter(strings.ReplaceAll(f, "%%", "%")); err != nil {
		return fmt.Errorf("Invalid filter: %s", s)
	}
	return nil
}

// parseLDAPSearch reads [base, scope, filter]; an empty list is no search.
func parseLDAPSearch(v any, withUser bool) (*ldapSearch, error) {
	items, ok := v.([]any)
	if v == nil || ok && len(items) == 0 {
		return nil, nil
	}
	if !ok {
		return nil, fmt.Errorf(`Expected a list of items but got type "%s".`, pyName(v))
	}
	if len(items) != 3 {
		return nil, fmt.Errorf("Expected a list of three items but got %d instead.", len(items))
	}
	base, _ := items[0].(string)
	scopeName, _ := items[1].(string)
	filter, _ := items[2].(string)
	if err := validateLDAPDN(base, false); err != nil {
		return nil, err
	}
	scope, ok := ldapScopes[scopeName]
	if !ok {
		return nil, fmt.Errorf(`"%v" is not a valid choice.`, items[1])
	}
	if err := validateLDAPFilter(filter, withUser); err != nil {
		return nil, err
	}
	if strings.TrimSpace(filter) == "" {
		filter = "(objectClass=*)"
	}
	return &ldapSearch{base: base, scope: scope, filter: filter}, nil
}

// parseLDAPUserSearch reads one search or a list of them (AWX's
// LDAPSearchUnion).
func parseLDAPUserSearch(v any) ([]ldapSearch, error) {
	items, _ := v.([]any)
	if len(items) == 0 {
		if _, err := parseLDAPSearch(v, true); err != nil {
			return nil, err
		}
		return nil, nil
	}
	if _, single := items[0].(string); single && len(items) == 3 {
		s, err := parseLDAPSearch(v, true)
		if err != nil {
			return nil, err
		}
		return []ldapSearch{*s}, nil
	}
	var out []ldapSearch
	for i, item := range items {
		if _, ok := item.([]any); !ok {
			return nil, fmt.Errorf("In order to ultilize LDAP Union, input element No. %d should be a search query array.", i+1)
		}
		s, err := parseLDAPSearch(item, true)
		if err != nil {
			return nil, fmt.Errorf("Error parsing LDAP Union element No. %d: %v", i+1, err)
		}
		if s != nil {
			out = append(out, *s)
		}
	}
	return out, nil
}

func parseLDAPURIs(v any) ([]string, error) {
	s, _ := v.(string)
	var out []string
	for _, u := range regexp.MustCompile(`[, ]`).Split(s, -1) {
		if u == "" {
			continue
		}
		p, err := url.Parse(u)
		if err != nil || (p.Scheme != "ldap" && p.Scheme != "ldaps") || p.Host == "" {
			return nil, errors.New("Enter a valid URL.")
		}
		out = append(out, u)
	}
	return out, nil
}

func stringMap(v any) (map[string]any, error) {
	if v == nil {
		return map[string]any{}, nil
	}
	m, ok := v.(map[string]any)
	if !ok {
		return nil, fmt.Errorf(`Expected a dictionary of items but got type "%s".`, pyName(v))
	}
	return m, nil
}

func invalidKeys(m map[string]any, allowed map[string]bool) string {
	var bad []string
	for k := range m {
		if !allowed[k] {
			bad = append(bad, `"`+k+`"`)
		}
	}
	if len(bad) == 0 {
		return ""
	}
	sort.Strings(bad)
	return strings.Join(bad, ", ")
}

var ldapUserAttrs = map[string]bool{"first_name": true, "last_name": true, "email": true}

func parseLDAPAttrMap(v any) (map[string]string, error) {
	m, err := stringMap(v)
	if err != nil {
		return nil, err
	}
	if bad := invalidKeys(m, ldapUserAttrs); bad != "" {
		return nil, fmt.Errorf("Invalid user attribute(s): %s.", bad)
	}
	out := map[string]string{}
	for k, val := range m {
		s, ok := val.(string)
		if !ok {
			return nil, fmt.Errorf("%s: Not a valid string.", k)
		}
		out[k] = s
	}
	return out, nil
}

// ldapDNList reads one DN or a list of them.
func ldapDNList(v any) ([]string, error) {
	var items []any
	switch x := v.(type) {
	case string:
		items = []any{x}
	case []any:
		items = x
	default:
		return nil, fmt.Errorf(`Expected a list of items but got type "%s".`, pyName(v))
	}
	out := make([]string, 0, len(items))
	for _, item := range items {
		s, ok := item.(string)
		if !ok {
			return nil, errors.New("Not a valid string.")
		}
		if err := validateLDAPDN(s, false); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, nil
}

var ldapFlags = map[string]bool{"is_superuser": true, "is_system_auditor": true}

func parseLDAPFlags(v any) (map[string][]string, error) {
	m, err := stringMap(v)
	if err != nil {
		return nil, err
	}
	out := map[string][]string{}
	for k, val := range m {
		if !ldapFlags[k] {
			return nil, fmt.Errorf(`Invalid user flag: "%s".`, k)
		}
		dns, err := ldapDNList(val)
		if err != nil {
			return nil, fmt.Errorf("%s: %v", k, err)
		}
		out[k] = dns
	}
	return out, nil
}

// ldapOptions are the python-ldap connection options awxnext uses.
type ldapOptions struct {
	timeout    time.Duration
	skipVerify bool
	caFile     string
}

func parseLDAPOptions(v any) (ldapOptions, error) {
	o := ldapOptions{timeout: 30 * time.Second}
	m, err := stringMap(v)
	if err != nil {
		return o, err
	}
	var bad []string
	for k, val := range m {
		if !strings.HasPrefix(k, "OPT_") {
			bad = append(bad, `"`+k+`"`)
			continue
		}
		switch k {
		case "OPT_NETWORK_TIMEOUT":
			if n, err := strconv.ParseFloat(fmt.Sprint(val), 64); err == nil && n > 0 {
				o.timeout = time.Duration(n * float64(time.Second))
			}
		case "OPT_X_TLS_REQUIRE_CERT":
			o.skipVerify = fmt.Sprint(val) == "0"
		case "OPT_X_TLS_CACERTFILE":
			o.caFile, _ = val.(string)
		}
	}
	if len(bad) > 0 {
		sort.Strings(bad)
		return o, fmt.Errorf("Invalid connection option(s): %s.", strings.Join(bad, ", "))
	}
	return o, nil
}

func validateGroupTypeParams(v any) error {
	m, err := stringMap(v)
	if err != nil {
		return err
	}
	allowed := map[string]bool{}
	for _, params := range settings.LDAPGroupTypes {
		for _, p := range params {
			allowed[p] = true
		}
	}
	if bad := invalidKeys(m, allowed); bad != "" {
		return fmt.Errorf("Invalid key(s): %s.", bad)
	}
	return nil
}

// groupType is how a server stores group membership.
type groupType struct {
	memberAttr string // groups listing member DNs
	nested     bool
	posixAttr  string // posix groups: the user attribute in memberUid
}

func newGroupType(name string, params map[string]any) (groupType, error) {
	param := func(k, def string) string {
		if s, ok := params[k].(string); ok && s != "" {
			return s
		}
		return def
	}
	switch name {
	case "PosixGroupType":
		return groupType{posixAttr: "uid"}, nil
	case "PosixUIDGroupType":
		return groupType{posixAttr: param("ldap_group_user_attr", "uid")}, nil
	case "MemberDNGroupType", "NestedMemberDNGroupType":
		attr := param("member_attr", "")
		if attr == "" {
			return groupType{}, errors.New("Missing required parameters in GROUP_TYPE_PARAMS.")
		}
		return groupType{memberAttr: attr, nested: name == "NestedMemberDNGroupType"}, nil
	case "GroupOfNamesType", "ActiveDirectoryGroupType":
		return groupType{memberAttr: "member"}, nil
	case "NestedGroupOfNamesType", "NestedActiveDirectoryGroupType":
		return groupType{memberAttr: "member", nested: true}, nil
	case "GroupOfUniqueNamesType":
		return groupType{memberAttr: "uniqueMember"}, nil
	case "NestedGroupOfUniqueNamesType":
		return groupType{memberAttr: "uniqueMember", nested: true}, nil
	case "OrganizationalRoleGroupType":
		return groupType{memberAttr: "roleOccupant"}, nil
	case "NestedOrganizationalRoleGroupType":
		return groupType{memberAttr: "roleOccupant", nested: true}, nil
	}
	return groupType{}, fmt.Errorf(`"%s" is not a valid choice.`, name)
}

// ldapServer is the configuration of one of the six servers.
type ldapServer struct {
	prefix, source string
	uris           []string
	bindDN, bindPW string
	startTLS       bool
	opts           ldapOptions
	userSearch     []ldapSearch
	dnTemplate     string
	attrMap        map[string]string
	groupSearch    *ldapSearch
	groupType      groupType
	require, deny  string
	flags          map[string][]string
	maps           AWXMaps
}

// ldapSource is the grant source of a server: "ldap", "ldap_1"…
func ldapSource(prefix string) string {
	if prefix == "AUTH_LDAP_" {
		return "ldap"
	}
	return "ldap_" + strings.TrimSuffix(strings.TrimPrefix(prefix, "AUTH_LDAP_"), "_")
}

// loadLDAPServer reads a server's settings; nil when it is off.
func loadLDAPServer(get func(string) any, decrypt func(string) (string, error), prefix string) (*ldapServer, error) {
	uris, err := parseLDAPURIs(get(prefix + "SERVER_URI"))
	if err != nil || len(uris) == 0 {
		return nil, err
	}
	s := &ldapServer{prefix: prefix, source: ldapSource(prefix), uris: uris}
	s.bindDN, _ = get(prefix + "BIND_DN").(string)
	if pw, _ := get(prefix + "BIND_PASSWORD").(string); pw != "" {
		if s.bindPW, err = decrypt(pw); err != nil {
			return nil, err
		}
	}
	s.startTLS, _ = get(prefix + "START_TLS").(bool)
	wrap := func(key string, err error) error { return fmt.Errorf("%s%s: %v", prefix, key, err) }
	if s.opts, err = parseLDAPOptions(get(prefix + "CONNECTION_OPTIONS")); err != nil {
		return nil, wrap("CONNECTION_OPTIONS", err)
	}
	if s.userSearch, err = parseLDAPUserSearch(get(prefix + "USER_SEARCH")); err != nil {
		return nil, wrap("USER_SEARCH", err)
	}
	s.dnTemplate, _ = get(prefix + "USER_DN_TEMPLATE").(string)
	if s.attrMap, err = parseLDAPAttrMap(get(prefix + "USER_ATTR_MAP")); err != nil {
		return nil, wrap("USER_ATTR_MAP", err)
	}
	if s.groupSearch, err = parseLDAPSearch(get(prefix+"GROUP_SEARCH"), false); err != nil {
		return nil, wrap("GROUP_SEARCH", err)
	}
	params, _ := get(prefix + "GROUP_TYPE_PARAMS").(map[string]any)
	typeName, _ := get(prefix + "GROUP_TYPE").(string)
	if s.groupType, err = newGroupType(typeName, params); err != nil {
		return nil, wrap("GROUP_TYPE", err)
	}
	s.require, _ = get(prefix + "REQUIRE_GROUP").(string)
	s.deny, _ = get(prefix + "DENY_GROUP").(string)
	if s.flags, err = parseLDAPFlags(get(prefix + "USER_FLAGS_BY_GROUP")); err != nil {
		return nil, wrap("USER_FLAGS_BY_GROUP", err)
	}
	if s.maps.Orgs, err = ParseLDAPOrgMap(get(prefix + "ORGANIZATION_MAP")); err != nil {
		return nil, wrap("ORGANIZATION_MAP", err)
	}
	if s.maps.Teams, err = ParseLDAPTeamMap(get(prefix + "TEAM_MAP")); err != nil {
		return nil, wrap("TEAM_MAP", err)
	}
	// AWX refuses to log in without a group search (ImproperlyConfigured).
	if s.groupSearch == nil {
		return nil, fmt.Errorf("%sGROUP_SEARCH must be set for LDAP login", prefix)
	}
	if s.dnTemplate == "" && len(s.userSearch) == 0 {
		return nil, fmt.Errorf("%sUSER_SEARCH or %sUSER_DN_TEMPLATE must be set for LDAP login", prefix, prefix)
	}
	return s, nil
}

// dial connects to the first server that answers.
func (s *ldapServer) dial() (*ldap.Conn, error) {
	var last error
	for _, u := range s.uris {
		p, _ := url.Parse(u)
		tc := &tls.Config{ServerName: p.Hostname(), InsecureSkipVerify: s.opts.skipVerify, MinVersion: tls.VersionTLS12}
		if s.opts.caFile != "" {
			pem, err := os.ReadFile(s.opts.caFile)
			if err != nil {
				return nil, err
			}
			tc.RootCAs = x509.NewCertPool()
			tc.RootCAs.AppendCertsFromPEM(pem)
		}
		conn, err := ldap.DialURL(u, ldap.DialWithDialer(&net.Dialer{Timeout: s.opts.timeout}), ldap.DialWithTLSConfig(tc))
		if err != nil {
			last = err
			continue
		}
		conn.SetTimeout(s.opts.timeout)
		if s.startTLS && p.Scheme == "ldap" {
			if err := conn.StartTLS(tc); err != nil {
				conn.Close()
				last = err
				continue
			}
		}
		return conn, nil
	}
	return nil, last
}

// bindService binds as the bind DN, or anonymously.
func (s *ldapServer) bindService(conn *ldap.Conn) error {
	if s.bindDN == "" {
		return conn.UnauthenticatedBind("")
	}
	return conn.Bind(s.bindDN, s.bindPW)
}

// ldapUser is an authenticated user and what is known of their groups.
type ldapUser struct {
	dn     string
	attrs  *ldap.Entry
	conn   *ldap.Conn
	server *ldapServer
	groups map[string]bool // lowercased DNs, once loaded
}

// substitute fills %(user)s as Python's % formatting does.
func substitute(template, value string) string {
	return strings.ReplaceAll(strings.ReplaceAll(template, "%(user)s", value), "%%", "%")
}

func (s *ldapServer) search(conn *ldap.Conn, q ldapSearch, filter string, attrs []string) ([]*ldap.Entry, error) {
	res, err := conn.Search(ldap.NewSearchRequest(q.base, q.scope, ldap.NeverDerefAliases, 0, int(s.opts.timeout.Seconds()), false, filter, attrs, nil))
	if err != nil {
		if ldap.IsErrorWithCode(err, ldap.LDAPResultNoSuchObject) {
			return nil, nil
		}
		return nil, err
	}
	return res.Entries, nil
}

var errLDAPRejected = errors.New("rejected")

// authenticate finds the user, checks the password and the group
// requirements. errLDAPRejected means this server does not log them in.
func (s *ldapServer) authenticate(username, password string) (*ldapUser, error) {
	if password == "" {
		return nil, errLDAPRejected
	}
	conn, err := s.dial()
	if err != nil {
		return nil, err
	}
	u := &ldapUser{conn: conn, server: s}
	if s.dnTemplate != "" {
		u.dn = substitute(s.dnTemplate, ldap.EscapeDN(username))
	} else {
		if err := s.bindService(conn); err != nil {
			conn.Close()
			return nil, err
		}
		found := map[string]*ldap.Entry{}
		for _, q := range s.userSearch {
			entries, err := s.search(conn, q, substitute(q.filter, ldap.EscapeFilter(username)), nil)
			if err != nil {
				conn.Close()
				return nil, err
			}
			for _, e := range entries {
				found[strings.ToLower(e.DN)] = e
			}
		}
		if len(found) != 1 {
			conn.Close()
			return nil, errLDAPRejected
		}
		for dn, e := range found {
			u.dn, u.attrs = dn, e
		}
	}
	if err := conn.Bind(u.dn, password); err != nil {
		conn.Close()
		if ldap.IsErrorWithCode(err, ldap.LDAPResultInvalidCredentials) {
			return nil, errLDAPRejected
		}
		return nil, err
	}
	// Searches are made as the service account again, as django-auth-ldap does.
	if err := s.bindService(conn); err != nil {
		conn.Close()
		return nil, err
	}
	if u.attrs == nil {
		entries, err := s.search(conn, ldapSearch{base: u.dn, scope: ldap.ScopeBaseObject}, "(objectClass=*)", nil)
		if err != nil {
			conn.Close()
			return nil, err
		}
		if len(entries) > 0 {
			u.attrs = entries[0]
		} else {
			u.attrs = &ldap.Entry{DN: u.dn}
		}
	}
	for _, check := range []struct {
		dn   string
		want bool
	}{{s.require, true}, {s.deny, false}} {
		if check.dn == "" {
			continue
		}
		member, err := u.isMember(check.dn)
		if err != nil {
			conn.Close()
			return nil, err
		}
		if member != check.want {
			conn.Close()
			return nil, errLDAPRejected
		}
	}
	return u, nil
}

func (u *ldapUser) attr(name string) string {
	if v := u.attrs.GetEqualFoldAttributeValues(name); len(v) > 0 {
		return v[0]
	}
	return ""
}

// posixTerms are the memberUid and gidNumber terms of a posix user.
func (u *ldapUser) posixTerms() (string, bool) {
	uid := u.attr(u.server.groupType.posixAttr)
	if uid == "" {
		return "", false
	}
	if gid := u.attr("gidNumber"); gid != "" {
		return fmt.Sprintf("(|(gidNumber=%s)(memberUid=%s))", ldap.EscapeFilter(gid), ldap.EscapeFilter(uid)), true
	}
	return fmt.Sprintf("(memberUid=%s)", ldap.EscapeFilter(uid)), true
}

// loadGroups finds the user's groups with the group search.
func (u *ldapUser) loadGroups() error {
	if u.groups != nil {
		return nil
	}
	s, gt := u.server, u.server.groupType
	groups := map[string]bool{}
	and := func(term string) string { return "(&" + s.groupSearch.filter + term + ")" }
	switch {
	case gt.posixAttr != "":
		if terms, ok := u.posixTerms(); ok {
			entries, err := s.search(u.conn, *s.groupSearch, and(terms), []string{"dn"})
			if err != nil {
				return err
			}
			for _, e := range entries {
				groups[strings.ToLower(e.DN)] = true
			}
		}
	case gt.nested:
		// Bottom up, never searching with the same member twice.
		next, handled := []string{u.dn}, map[string]bool{}
		for len(next) > 0 {
			var terms strings.Builder
			for _, dn := range next {
				handled[strings.ToLower(dn)] = true
				fmt.Fprintf(&terms, "(%s=%s)", gt.memberAttr, ldap.EscapeFilter(dn))
			}
			entries, err := s.search(u.conn, *s.groupSearch, and("(|"+terms.String()+")"), []string{"dn"})
			if err != nil {
				return err
			}
			next = nil
			for _, e := range entries {
				dn := strings.ToLower(e.DN)
				groups[dn] = true
				if !handled[dn] {
					next = append(next, e.DN)
				}
			}
		}
	default:
		entries, err := s.search(u.conn, *s.groupSearch, and(fmt.Sprintf("(%s=%s)", gt.memberAttr, ldap.EscapeFilter(u.dn))), []string{"dn"})
		if err != nil {
			return err
		}
		for _, e := range entries {
			groups[strings.ToLower(e.DN)] = true
		}
	}
	u.groups = groups
	return nil
}

func sameDN(a, b string) bool {
	if strings.EqualFold(a, b) {
		return true
	}
	da, err1 := ldap.ParseDN(a)
	db, err2 := ldap.ParseDN(b)
	return err1 == nil && err2 == nil && da.EqualFold(db)
}

func (u *ldapUser) inGroups(dn string) bool {
	for g := range u.groups {
		if sameDN(g, dn) {
			return true
		}
	}
	return false
}

// isMember is django-auth-ldap's is_member_of: before the groups are
// loaded, non-nested types look at the group entry itself; otherwise the
// group must be one the group search found.
func (u *ldapUser) isMember(groupDN string) (bool, error) {
	gt := u.server.groupType
	if u.groups == nil && !gt.nested {
		var filter string
		if gt.posixAttr != "" {
			terms, ok := u.posixTerms()
			if !ok {
				return false, nil
			}
			filter = terms
		} else {
			filter = fmt.Sprintf("(%s=%s)", gt.memberAttr, ldap.EscapeFilter(u.dn))
		}
		entries, err := u.server.search(u.conn, ldapSearch{base: groupDN, scope: ldap.ScopeBaseObject}, filter, []string{"dn"})
		if err != nil {
			return false, err
		}
		return len(entries) > 0, nil
	}
	if err := u.loadGroups(); err != nil {
		return false, err
	}
	return u.inGroups(groupDN), nil
}

// mapping evaluates the user's flags, then (with the groups the search
// finds, as AWX loads them before its maps) the organization and team maps.
func (u *ldapUser) mapping() (MappingResult, error) {
	var res MappingResult
	flags := map[string]*bool{}
	for flag, dns := range u.server.flags {
		value := false
		for _, dn := range dns {
			m, err := u.isMember(dn)
			if err != nil {
				return res, err
			}
			value = value || m
		}
		flags[flag] = &value
	}
	if err := u.loadGroups(); err != nil {
		return res, err
	}
	res = Mapping{AWX: u.server.maps}.Evaluate(Identity{Match: func(lit string, _ *regexp.Regexp) bool { return u.inGroups(lit) }})
	res.Superuser, res.Auditor = flags["is_superuser"], flags["is_system_auditor"]
	return res, nil
}

// LDAP logs users in with AWX's LDAP settings.
type LDAP struct {
	auth *Auth
}

// servers returns the configured servers, in AWX's order.
func (l *LDAP) servers(ctx context.Context) []*ldapServer {
	app := l.auth.app
	get := func(k string) any { return app.Settings.Get(ctx, k) }
	var out []*ldapServer
	for _, prefix := range settings.LDAPServers {
		s, err := loadLDAPServer(get, app.Crypto.Decrypt, prefix)
		if err != nil {
			slog.Warn("ldap configuration", "server", prefix, "error", err)
			continue
		}
		if s != nil {
			out = append(out, s)
		}
	}
	return out
}

// Enabled reports whether a server is configured.
func (l *LDAP) Enabled(ctx context.Context) bool {
	for _, p := range settings.LDAPServers {
		if s, _ := l.auth.app.Settings.Get(ctx, p+"SERVER_URI").(string); strings.TrimSpace(s) != "" {
			return true
		}
	}
	return false
}

// Authenticate tries each server in turn, as AWX's LDAP backends are tried
// before local accounts. ok is false when LDAP does not log the user in,
// leaving the local password check. A user who exists and is not an LDAP
// user is never logged in through LDAP.
func (l *LDAP) Authenticate(ctx context.Context, username, password string) (int64, bool, error) {
	username = strings.TrimSpace(username)
	if username == "" || !l.Enabled(ctx) {
		return 0, false, nil
	}
	app := l.auth.app
	var source string
	if err := app.DB.QueryRow(ctx, "SELECT external_source FROM users WHERE lower(username) = lower($1) ORDER BY username = $1 DESC LIMIT 1", username).Scan(&source); err == nil && source != "ldap" {
		return 0, false, nil
	}
	for _, s := range l.servers(ctx) {
		u, err := s.authenticate(username, password)
		if err != nil {
			if !errors.Is(err, errLDAPRejected) {
				slog.Warn("ldap login", "server", s.prefix, "user", username, "error", err)
			}
			continue
		}
		uid, err := l.provision(ctx, u, username)
		u.conn.Close()
		if err != nil {
			return 0, true, err
		}
		return uid, true, nil
	}
	return 0, false, nil
}

// provision creates or updates the user and applies the mapping.
func (l *LDAP) provision(ctx context.Context, u *ldapUser, username string) (int64, error) {
	res, err := u.mapping()
	if err != nil {
		return 0, err
	}
	fields := map[string]string{}
	for field, attr := range u.server.attrMap {
		if v := u.attrs.GetEqualFoldAttributeValues(attr); len(v) > 0 {
			fields[field] = v[0]
		}
	}
	tx, err := l.auth.app.DB.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)
	var uid int64
	var active bool
	// django-auth-ldap finds users case-insensitively and creates them in lower case.
	name := strings.ToLower(username)
	err = tx.QueryRow(ctx, "SELECT id, is_active FROM users WHERE lower(username) = $1 AND external_source = 'ldap'", name).Scan(&uid, &active)
	if err != nil {
		active = true
		if err := tx.QueryRow(ctx, `INSERT INTO users (username, external_source, external_id) VALUES ($1, 'ldap', $2) RETURNING id`, name, u.dn).Scan(&uid); err != nil {
			return 0, err
		}
	}
	if !active {
		return 0, errors.New("This account is disabled.")
	}
	if _, err := tx.Exec(ctx, `UPDATE users SET external_id = $2, first_name = coalesce($3, first_name), last_name = coalesce($4, last_name),
		email = coalesce($5, email), last_login = now() WHERE id = $1`, uid, u.dn, nullable(fields, "first_name"), nullable(fields, "last_name"), nullable(fields, "email")); err != nil {
		return 0, err
	}
	warnings, err := ApplyMapping(ctx, tx, uid, u.server.source, res)
	if err != nil {
		return 0, err
	}
	for _, w := range warnings {
		slog.Warn("ldap mapping", "user", name, "warning", w)
	}
	return uid, tx.Commit(ctx)
}

func nullable(m map[string]string, k string) any {
	if v, ok := m[k]; ok {
		return v
	}
	return nil
}

// ldapValidators returns the checks of each LDAP setting, by key suffix.
func ldapValidators() map[string]func(any) error {
	return map[string]func(any) error{
		"SERVER_URI": func(v any) error { _, err := parseLDAPURIs(v); return err },
		"BIND_DN": func(v any) error {
			s, _ := v.(string)
			return validateBindDN(s)
		},
		"CONNECTION_OPTIONS": func(v any) error { _, err := parseLDAPOptions(v); return err },
		"USER_SEARCH":        func(v any) error { _, err := parseLDAPUserSearch(v); return err },
		"USER_DN_TEMPLATE": func(v any) error {
			if s, _ := v.(string); s != "" {
				return validateLDAPDN(s, true)
			}
			return nil
		},
		"USER_ATTR_MAP":     func(v any) error { _, err := parseLDAPAttrMap(v); return err },
		"GROUP_SEARCH":      func(v any) error { _, err := parseLDAPSearch(v, false); return err },
		"GROUP_TYPE_PARAMS": validateGroupTypeParams,
		"REQUIRE_GROUP": func(v any) error {
			if s, _ := v.(string); s != "" {
				return validateLDAPDN(s, false)
			}
			return nil
		},
		"DENY_GROUP": func(v any) error {
			if s, _ := v.(string); s != "" {
				return validateLDAPDN(s, false)
			}
			return nil
		},
		"USER_FLAGS_BY_GROUP": func(v any) error { _, err := parseLDAPFlags(v); return err },
		"ORGANIZATION_MAP":    func(v any) error { _, err := ParseLDAPOrgMap(v); return err },
		"TEAM_MAP":            func(v any) error { _, err := ParseLDAPTeamMap(v); return err },
	}
}
