package auth

import (
	"bytes"
	"context"
	"crypto"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/crewjam/saml"
	"github.com/go-ldap/ldap/v3"

	"github.com/damhau/awxnext/internal/api"
	"github.com/damhau/awxnext/internal/settings"
)

// The connection tests, POST /api/v2/settings/connections/test/: a login
// method's settings tried against its server or provider, with the values
// of the form, saved or not, before anyone signs in with them. LDAP
// connects, binds, and looks a user up with their groups and what they
// would get; OIDC reads the discovery document and the signing keys; SAML
// checks the service provider's certificate and key, and each identity
// provider's certificate, metadata and sign-in URL. Each step is reported,
// up to the first that fails, with why. Nothing is saved or changed.

// testStep is a step of a connection test.
type testStep struct {
	Step string `json:"step"`
	// IdP is the SAML identity provider the step is about.
	IdP    string `json:"idp,omitempty"`
	Status string `json:"status"` // ok, warning, skipped or failed
	Detail string `json:"detail"`
}

type testSteps []testStep

// add records a step; false when it failed.
func (l *testSteps) add(step, idp, status, detail string) bool {
	*l = append(*l, testStep{step, idp, status, detail})
	return status != "failed"
}

func (l *testSteps) ok(step, detail string) { l.add(step, "", "ok", detail) }

func (l *testSteps) fail(step string, err error) bool { return l.add(step, "", "failed", err.Error()) }

// failLDAP records a failed LDAP step, its error as a sentence.
func (l *testSteps) failLDAP(step string, err error) { l.add(step, "", "failed", ldapMessage(err)) }

// ldapMessage is an LDAP error as a sentence: a network error as itself,
// without the library's result code 200, and a directory's refusal without
// the colon of an empty diagnostic message.
func ldapMessage(err error) string {
	var le *ldap.Error
	if !errors.As(err, &le) {
		return err.Error()
	}
	s := err.Error()
	if le.ResultCode == ldap.ErrorNetwork && le.Err != nil {
		s = strings.Replace(s, le.Error(), le.Err.Error(), 1)
	}
	return strings.TrimSuffix(strings.TrimSpace(s), ":")
}

// plural is a count and its noun: 1 key, 2 keys.
func plural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return fmt.Sprintf("%d %s", n, many)
}

func (l testSteps) failed() bool {
	return slices.ContainsFunc(l, func(s testStep) bool { return s.Status == "failed" })
}

// ldapPrefix is the settings prefix of an LDAP login name: ldap, ldap1...
func ldapPrefix(name string) (string, bool) {
	if name == "ldap" {
		return settings.LDAPServers[0], true
	}
	if len(name) == 5 && strings.HasPrefix(name, "ldap") && name[4] >= '1' && name[4] <= '5' {
		return settings.LDAPServers[name[4]-'0'], true
	}
	return "", false
}

// ldapServerOf is the prefix of the LDAP server a setting belongs to.
func ldapServerOf(key string) string {
	best := ""
	for _, p := range settings.LDAPServers {
		if strings.HasPrefix(key, p) && len(p) > len(best) {
			best = p
		}
	}
	return best
}

func (a *Auth) connectionTest(c *api.Ctx, w http.ResponseWriter, r *http.Request) {
	if !c.User.Superuser {
		api.WriteError(w, api.ErrForbidden)
		return
	}
	data, err := api.ReadBody(r)
	if err != nil {
		api.WriteError(w, err)
		return
	}
	name, _ := data["login"].(string)
	prefix, isLDAP := ldapPrefix(name)
	var allowed func(string) bool
	switch {
	case isLDAP:
		allowed = func(k string) bool { return settings.Lookup(k).Category == "ldap" && ldapServerOf(k) == prefix }
	case name == "oidc" || name == "saml":
		allowed = func(k string) bool { return settings.Lookup(k).Category == name }
	default:
		api.WriteError(w, api.FieldError("login", "Choose one of ldap, ldap1, ldap2, ldap3, ldap4, ldap5, oidc, saml."))
		return
	}
	form, ok := readFormSettings(c, w, data, allowed, "setting of "+name)
	if !ok {
		return
	}
	var steps testSteps
	out := api.Obj{"login": name}
	switch {
	case isLDAP:
		username, _ := data["username"].(string)
		password, _ := data["password"].(string)
		s, err := loadLDAPServer(form.get, form.decrypt, prefix)
		switch {
		case err != nil:
			steps.fail("settings", err)
		case s == nil:
			steps.add("settings", "", "failed", "Set the server URI ("+prefix+"SERVER_URI).")
		default:
			if user := s.check(c, &steps, strings.TrimSpace(username), password); user != nil {
				out["user"] = user
			}
		}
	case name == "oidc":
		oidcCheck(c, &steps, form.get)
	case name == "saml":
		urls := map[string]string{}
		if m, ok := normalizeJSON(data["metadata_urls"]).(map[string]any); ok {
			for k, v := range m {
				urls[k], _ = v.(string)
			}
		}
		samlCheck(c, &steps, c.App, form, urls)
	}
	out["ok"] = !steps.failed()
	out["steps"] = steps
	api.WriteJSON(w, 200, out)
}

// check runs an LDAP login's steps: connect, bind as the bind DN, then,
// for a username, find the user, check the password if one is given, read
// the groups, and evaluate the maps. The user found is returned.
func (s *ldapServer) check(c *api.Ctx, l *testSteps, username, password string) api.Obj {
	var conn *ldap.Conn
	var failures []string
	for _, u := range s.uris {
		cn, err := s.dialURI(u)
		if err != nil {
			failures = append(failures, u+": "+ldapMessage(err))
			continue
		}
		conn = cn
		detail := "Connected to " + u
		if p, _ := url.Parse(u); s.startTLS && p.Scheme == "ldap" {
			detail += ", with StartTLS"
		}
		detail += "."
		if len(failures) > 0 {
			detail += " Before it: " + strings.Join(failures, "; ") + "."
		}
		l.ok("connect", detail)
		break
	}
	if conn == nil {
		l.add("connect", "", "failed", strings.Join(failures, "; "))
		return nil
	}
	defer conn.Close()
	if err := s.bindService(conn); err != nil {
		if ldap.IsErrorWithCode(err, ldap.LDAPResultInvalidCredentials) && s.bindDN != "" {
			l.add("bind", "", "failed", "The directory refuses the password of "+s.bindDN+" ("+s.prefix+"BIND_PASSWORD).")
		} else {
			l.failLDAP("bind", err)
		}
		return nil
	}
	if s.bindDN == "" {
		l.ok("bind", "Bound anonymously.")
	} else {
		l.ok("bind", "Bound as "+s.bindDN+".")
	}
	if username == "" {
		return nil
	}
	u := &ldapUser{conn: conn, server: s}
	if s.dnTemplate != "" {
		u.dn = substitute(s.dnTemplate, ldap.EscapeDN(username))
		entries, err := s.search(conn, ldapSearch{base: u.dn, scope: ldap.ScopeBaseObject}, "(objectClass=*)", nil)
		switch {
		case ldap.IsErrorWithCode(err, ldap.LDAPResultNoSuchObject) || err == nil && len(entries) == 0:
			l.add("user", "", "failed", "No entry at "+u.dn+", the user DN template's DN for "+username+".")
			return nil
		case err != nil:
			l.failLDAP("user", err)
			return nil
		}
		u.attrs = entries[0]
		l.ok("user", "Found at "+u.dn+", from the user DN template.")
	} else {
		found := map[string]*ldap.Entry{}
		for _, q := range s.userSearch {
			entries, err := s.search(conn, q, substitute(q.filter, ldap.EscapeFilter(username)), nil)
			if err != nil {
				l.failLDAP("user", err)
				return nil
			}
			for _, e := range entries {
				found[strings.ToLower(e.DN)] = e
			}
		}
		if len(found) != 1 {
			if len(found) == 0 {
				l.add("user", "", "failed", "The user search finds no "+username+".")
			} else {
				dns := make([]string, 0, len(found))
				for _, e := range found {
					dns = append(dns, e.DN)
				}
				sort.Strings(dns)
				l.add("user", "", "failed", fmt.Sprintf("The user search finds %d entries for %s, and a login needs exactly one: %s.", len(found), username, strings.Join(dns, "; ")))
			}
			return nil
		}
		for _, e := range found {
			u.dn, u.attrs = e.DN, e
		}
		l.ok("user", "Found "+u.dn+".")
	}
	user := api.Obj{"dn": u.dn}
	for _, f := range []string{"first_name", "last_name", "email"} {
		if a := s.attrMap[f]; a != "" {
			user[f] = u.attr(a)
		}
	}
	if password != "" {
		if err := conn.Bind(u.dn, password); err != nil {
			if ldap.IsErrorWithCode(err, ldap.LDAPResultInvalidCredentials) {
				l.add("password", "", "failed", "The directory refuses this password for "+u.dn+".")
			} else {
				l.failLDAP("password", err)
			}
			return user
		}
		// Groups are read as the bind DN, as at login.
		if err := s.bindService(conn); err != nil {
			l.add("password", "", "failed", "The password is accepted, but binding as the bind DN again fails: "+err.Error())
			return user
		}
		l.ok("password", "The password is accepted.")
	}
	if err := u.loadGroups(); err != nil {
		l.failLDAP("groups", err)
		return user
	}
	groups := make([]string, 0, len(u.groups))
	for g := range u.groups {
		groups = append(groups, g)
	}
	sort.Strings(groups)
	user["groups"] = groups
	l.ok("groups", "The group search finds "+plural(len(groups), "group", "groups")+".")
	for _, check := range []struct {
		dn   string
		want bool
	}{{s.require, true}, {s.deny, false}} {
		if check.dn == "" {
			continue
		}
		member, err := u.isMember(check.dn)
		switch {
		case err != nil:
			l.failLDAP("allowed", err)
			return user
		case member != check.want && check.want:
			l.add("allowed", "", "failed", "Not a member of the required group "+check.dn+": the login would be refused.")
			return user
		case member != check.want:
			l.add("allowed", "", "failed", "A member of the denied group "+check.dn+": the login would be refused.")
			return user
		}
	}
	if s.require != "" || s.deny != "" {
		l.ok("allowed", "Allowed by the required and denied groups.")
	}
	res, err := u.mapping()
	if err != nil {
		l.failLDAP("access", err)
		return user
	}
	for k, v := range mapResultObj(c, res) {
		user[k] = v
	}
	l.ok("access", "The maps grant "+plural(len(res.Grants), "role", "roles")+".")
	return user
}

// oidcCheck reads the provider's discovery document and signing keys.
func oidcCheck(ctx context.Context, l *testSteps, get func(string) any) {
	endpoint, _ := get("SOCIAL_AUTH_OIDC_OIDC_ENDPOINT").(string)
	clientID, _ := get("SOCIAL_AUTH_OIDC_KEY").(string)
	verify, _ := get("SOCIAL_AUTH_OIDC_VERIFY_SSL").(bool)
	var missing []string
	if strings.TrimSpace(endpoint) == "" {
		missing = append(missing, "the provider URL (SOCIAL_AUTH_OIDC_OIDC_ENDPOINT)")
	}
	if strings.TrimSpace(clientID) == "" {
		missing = append(missing, "the client ID (SOCIAL_AUTH_OIDC_KEY)")
	}
	if len(missing) > 0 {
		l.add("settings", "", "failed", "Set "+strings.Join(missing, " and ")+".")
		return
	}
	issuer := strings.TrimSuffix(strings.TrimSuffix(strings.TrimSpace(endpoint), "/.well-known/openid-configuration"), "/")
	client := &http.Client{Timeout: 15 * time.Second, Transport: &http.Transport{
		Proxy:           http.ProxyFromEnvironment,
		TLSClientConfig: &tls.Config{InsecureSkipVerify: !verify},
	}}
	p, err := oidc.NewProvider(oidc.ClientContext(ctx, client), issuer)
	if err != nil {
		l.fail("discovery", err)
		return
	}
	var d struct {
		JWKS   string   `json:"jwks_uri"`
		Scopes []string `json:"scopes_supported"`
	}
	p.Claims(&d)
	status, detail := "ok", fmt.Sprintf("Read %s/.well-known/openid-configuration: sign-in at %s, tokens at %s.", issuer, p.Endpoint().AuthURL, p.Endpoint().TokenURL)
	if !verify {
		status, detail = "warning", detail+" The provider's certificate is not checked: SOCIAL_AUTH_OIDC_VERIFY_SSL is off."
	}
	l.add("discovery", "", status, detail)
	if len(d.Scopes) > 0 {
		var unknown []string
		for _, s := range stringList(get("OIDC_SCOPES")) {
			if !slices.Contains(d.Scopes, s) {
				unknown = append(unknown, s)
			}
		}
		if len(unknown) > 0 {
			l.add("scopes", "", "warning", fmt.Sprintf("The provider does not list %s among its scopes (%s): it may refuse the sign-in, or ignore them.",
				strings.Join(unknown, ", "), strings.Join(d.Scopes, ", ")))
		}
	}
	if d.JWKS == "" {
		l.add("keys", "", "failed", "The discovery document has no jwks_uri.")
		return
	}
	req, _ := http.NewRequestWithContext(ctx, "GET", d.JWKS, nil)
	res, err := client.Do(req)
	if err != nil {
		l.fail("keys", err)
		return
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		l.add("keys", "", "failed", fmt.Sprintf("%s answers HTTP %d.", d.JWKS, res.StatusCode))
		return
	}
	var set struct {
		Keys []struct {
			Kid string `json:"kid"`
			Use string `json:"use"`
			Alg string `json:"alg"`
			Kty string `json:"kty"`
		} `json:"keys"`
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&set); err != nil {
		l.add("keys", "", "failed", fmt.Sprintf("%s is not a key set: %v", d.JWKS, err))
		return
	}
	var signing []string
	for _, k := range set.Keys {
		if k.Use == "" || k.Use == "sig" {
			id := k.Kid
			if id == "" {
				id = "a key without ID"
			}
			if alg := cmpOr(k.Alg, k.Kty); alg != "" {
				id += " (" + alg + ")"
			}
			signing = append(signing, id)
		}
	}
	if len(signing) == 0 {
		l.add("keys", "", "failed", fmt.Sprintf("%s has no signing key: ID tokens could not be verified.", d.JWKS))
		return
	}
	l.ok("keys", fmt.Sprintf("%s has %s: %s.", d.JWKS, plural(len(signing), "signing key", "signing keys"), strings.Join(signing, ", ")))
}

func cmpOr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// certStatus says whether a certificate is valid now, and until when; it
// warns 30 days before it expires.
func certStatus(c *x509.Certificate, now time.Time) (status, text string) {
	name := c.Subject.String()
	if name == "" {
		name = "The certificate"
	}
	day := func(t time.Time) string { return t.UTC().Format("2006-01-02") }
	switch left := c.NotAfter.Sub(now); {
	case now.Before(c.NotBefore):
		return "failed", fmt.Sprintf("%s is not valid before %s.", name, day(c.NotBefore))
	case left < 0:
		return "failed", fmt.Sprintf("%s expired on %s.", name, day(c.NotAfter))
	case left < 24*time.Hour:
		return "warning", fmt.Sprintf("%s expires on %s, in less than a day.", name, day(c.NotAfter))
	case left < 30*24*time.Hour:
		return "warning", fmt.Sprintf("%s expires on %s, in %d days.", name, day(c.NotAfter), int(left.Hours()/24))
	}
	return "ok", fmt.Sprintf("%s, valid until %s.", name, day(c.NotAfter))
}

// samlCheck checks the service provider's certificate and key, then each
// identity provider's certificate, metadata and sign-in URL.
func samlCheck(ctx context.Context, l *testSteps, app *api.App, form *formSettings, metadataURLs map[string]string) {
	cfg, err := loadSAMLConfig(app, form.get, form.decrypt)
	if err != nil {
		l.fail("settings", err)
		return
	}
	now := time.Now()
	status, text := certStatus(cfg.cert, now)
	if pub, ok := cfg.key.Public().(interface{ Equal(crypto.PublicKey) bool }); !ok || !pub.Equal(cfg.cert.PublicKey) {
		status, text = "failed", "The private key (SOCIAL_AUTH_SAML_SP_PRIVATE_KEY) does not match the certificate (SOCIAL_AUTH_SAML_SP_PUBLIC_CERT)."
	} else {
		text = "Entity ID " + cfg.entityID + ". Its certificate: " + text
	}
	l.add("service-provider", "", status, text)
	names := make([]string, 0, len(cfg.idps))
	for n := range cfg.idps {
		names = append(names, n)
	}
	sort.Strings(names)
	client := &http.Client{Timeout: 15 * time.Second, Transport: &http.Transport{Proxy: http.ProxyFromEnvironment}}
	noFollow := *client
	noFollow.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	for _, name := range names {
		idp := cfg.idps[name]
		status, text := certStatus(idp.cert, now)
		l.add("certificate", name, status, text)
		samlMetadataCheck(ctx, l, client, idp, strings.TrimSpace(metadataURLs[name]))
		req, err := http.NewRequestWithContext(ctx, "GET", idp.url, nil)
		if err == nil {
			var res *http.Response
			if res, err = noFollow.Do(req); err == nil {
				res.Body.Close()
				l.add("sso", name, "ok", fmt.Sprintf("%s answers (HTTP %d).", idp.url, res.StatusCode))
				continue
			}
		}
		l.add("sso", name, "failed", err.Error())
	}
}

// samlMetadataCheck compares an identity provider's metadata with its
// settings: the entity ID, the signing certificate and the sign-in URL. The
// metadata is read from the URL given, else from the entity ID when it is
// a URL, as many identity providers serve it there.
func samlMetadataCheck(ctx context.Context, l *testSteps, client *http.Client, idp *samlIdP, src string) {
	fromEntity := false
	if src == "" {
		if u, err := url.Parse(idp.entityID); err == nil && (u.Scheme == "https" || u.Scheme == "http") && u.Host != "" {
			src, fromEntity = idp.entityID, true
		} else {
			l.add("metadata", idp.name, "skipped", "The entity ID is not a URL: give the metadata URL to compare the metadata with these settings.")
			return
		}
	}
	unread := func(why string) {
		if fromEntity {
			l.add("metadata", idp.name, "skipped", fmt.Sprintf("No metadata at the entity ID %s (%s): give the metadata URL to compare it with these settings.", src, why))
		} else {
			l.add("metadata", idp.name, "failed", fmt.Sprintf("%s: %s", src, why))
		}
	}
	req, err := http.NewRequestWithContext(ctx, "GET", src, nil)
	if err != nil {
		unread(err.Error())
		return
	}
	res, err := client.Do(req)
	if err != nil {
		unread(err.Error())
		return
	}
	body, err := io.ReadAll(io.LimitReader(res.Body, 4<<20))
	res.Body.Close()
	if err != nil || res.StatusCode != 200 {
		unread(fmt.Sprintf("HTTP %d", res.StatusCode))
		return
	}
	md, err := findEntity(body, idp.entityID)
	if err != nil {
		unread(err.Error())
		return
	}
	fail := func(f string, args ...any) { l.add("metadata", idp.name, "failed", fmt.Sprintf(f, args...)) }
	if md.EntityID != idp.entityID {
		fail("The metadata at %s is for %s, not %s.", src, md.EntityID, idp.entityID)
		return
	}
	if len(md.IDPSSODescriptors) == 0 {
		fail("The metadata at %s has no identity provider role.", src)
		return
	}
	var listed []string
	var signs bool
	var sso []string
	for _, d := range md.IDPSSODescriptors {
		for _, k := range d.KeyDescriptors {
			if k.Use != "" && k.Use != "signing" {
				continue
			}
			for _, xc := range k.KeyInfo.X509Data.X509Certificates {
				der, err := base64.StdEncoding.DecodeString(strings.Join(strings.Fields(xc.Data), ""))
				if err != nil {
					continue
				}
				if bytes.Equal(der, idp.cert.Raw) {
					signs = true
				}
				if c, err := x509.ParseCertificate(der); err == nil {
					_, t := certStatus(c, time.Now())
					listed = append(listed, t)
				}
			}
		}
		for _, e := range d.SingleSignOnServices {
			sso = append(sso, e.Location)
		}
	}
	if !signs {
		fail("The certificate set here is not one the metadata at %s lists for signing: it lists %s", src, cmpOr(strings.Join(listed, " "), "none."))
		return
	}
	if !slices.Contains(sso, idp.url) {
		l.add("metadata", idp.name, "warning", fmt.Sprintf("Read %s: the certificate matches, but the sign-in URL set here, %s, is not in it. It lists %s.", src, idp.url, cmpOr(strings.Join(sso, ", "), "none")))
		return
	}
	l.add("metadata", idp.name, "ok", fmt.Sprintf("Read %s: the entity ID, the certificate and the sign-in URL match.", src))
}

// findEntity reads SAML metadata: an entity, or a federation's list of
// them, from which the one with this entity ID.
func findEntity(b []byte, entityID string) (*saml.EntityDescriptor, error) {
	var ed saml.EntityDescriptor
	if xml.Unmarshal(b, &ed) == nil {
		return &ed, nil
	}
	var set saml.EntitiesDescriptor
	if xml.Unmarshal(b, &set) != nil {
		return nil, errors.New("not SAML metadata")
	}
	var walk func(s saml.EntitiesDescriptor) *saml.EntityDescriptor
	walk = func(s saml.EntitiesDescriptor) *saml.EntityDescriptor {
		for i := range s.EntityDescriptors {
			if s.EntityDescriptors[i].EntityID == entityID {
				return &s.EntityDescriptors[i]
			}
		}
		for _, sub := range s.EntitiesDescriptors {
			if e := walk(sub); e != nil {
				return e
			}
		}
		return nil
	}
	if e := walk(set); e != nil {
		return e, nil
	}
	return nil, fmt.Errorf("the metadata lists other entities, not %s", entityID)
}
