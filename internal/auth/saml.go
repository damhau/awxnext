package auth

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"encoding/xml"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/mail"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/beevik/etree"
	"github.com/crewjam/saml"

	"github.com/damhau/awxnext/internal/api"
)

// SAML login as AWX's SAML backend (python-social-auth with python3-saml)
// and its SAML pipeline do it, with AWX's SOCIAL_AUTH_SAML_* settings:
// /sso/login/saml/?idp=<name> sends an authentication request to one of the
// enabled identity providers, /sso/complete/saml/ is the assertion consumer
// service, /sso/metadata/saml/ the service provider metadata.

// The attributes python-social-auth reads by default.
const (
	oidUserID    = "urn:oid:0.9.2342.19200300.100.1.1"
	oidMail      = "urn:oid:0.9.2342.19200300.100.1.3"
	oidGivenName = "urn:oid:2.5.4.42"
	oidSurname   = "urn:oid:2.5.4.4"
)

// samlIdP is an entry of SOCIAL_AUTH_SAML_ENABLED_IDPS.
type samlIdP struct {
	name, entityID, url string
	cert                *x509.Certificate
	conf                map[string]string
}

// attr is the setting's attribute name for conf_key, or the default.
func (p *samlIdP) attr(confKey, def string) string {
	if a := p.conf[confKey]; a != "" {
		return a
	}
	return def
}

type samlConfig struct {
	entityID           string
	acsURL, metaURL    url.URL
	cert               *x509.Certificate
	key                crypto.Signer
	orgInfo            map[string]any
	technical, support map[string]any
	idps               map[string]*samlIdP
	security           map[string]any
	orgMap             []orgEntry
	teamMap            []teamEntry
	orgAttr, teamAttr  map[string]any
	flags              map[string]any
}

// parseCertificate reads a PEM certificate or its base64 body, as AWX's
// settings take both.
func parseCertificate(s string) (*x509.Certificate, error) {
	if block, _ := pem.Decode([]byte(s)); block != nil {
		return x509.ParseCertificate(block.Bytes)
	}
	der, err := base64.StdEncoding.DecodeString(strings.Join(strings.Fields(s), ""))
	if err != nil {
		return nil, errors.New("Invalid certificate or key: no certificate found.")
	}
	return x509.ParseCertificate(der)
}

func parsePrivateKey(s string) (crypto.Signer, error) {
	block, _ := pem.Decode([]byte(s))
	if block == nil {
		return nil, errors.New("Invalid certificate or key: no private key found.")
	}
	if k, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return k, nil
	}
	if k, err := x509.ParsePKCS8PrivateKey(block.Bytes); err == nil {
		if signer, ok := k.(crypto.Signer); ok {
			return signer, nil
		}
	}
	if k, err := x509.ParseECPrivateKey(block.Bytes); err == nil {
		return k, nil
	}
	return nil, errors.New("Invalid private key.")
}

var langCodeRe = regexp.MustCompile(`(?i)^[a-z]{2}(?:-[a-z]{2})?$`)

func requireStrings(m map[string]any, keys ...string) error {
	for _, k := range keys {
		if s, _ := m[k].(string); strings.TrimSpace(s) == "" {
			return fmt.Errorf("%s: This field is required.", k)
		}
	}
	return nil
}

func validateSAMLOrgInfo(v any) error {
	m, err := stringMap(v)
	if err != nil {
		return err
	}
	var bad []string
	for lang, info := range m {
		if !langCodeRe.MatchString(lang) {
			bad = append(bad, `"`+lang+`"`)
			continue
		}
		im, ok := info.(map[string]any)
		if !ok {
			return fmt.Errorf("%s: Expected a dictionary of items.", lang)
		}
		if err := requireStrings(im, "name", "displayname", "url"); err != nil {
			return fmt.Errorf("%s: %v", lang, err)
		}
		if u, err := url.Parse(im["url"].(string)); err != nil || u.Scheme == "" || u.Host == "" {
			return fmt.Errorf("%s: url: Enter a valid URL.", lang)
		}
	}
	if len(bad) > 0 {
		sort.Strings(bad)
		return fmt.Errorf("Invalid language code(s) for org info: %s.", strings.Join(bad, ", "))
	}
	return nil
}

func validateSAMLContact(v any) error {
	m, err := stringMap(v)
	if err != nil || len(m) == 0 {
		return err
	}
	if err := requireStrings(m, "givenName", "emailAddress"); err != nil {
		return err
	}
	if _, err := mail.ParseAddress(m["emailAddress"].(string)); err != nil {
		return errors.New("emailAddress: Enter a valid email address.")
	}
	return nil
}

func parseSAMLIdPs(v any) (map[string]*samlIdP, error) {
	m, err := stringMap(v)
	if err != nil {
		return nil, err
	}
	out := map[string]*samlIdP{}
	for name, raw := range m {
		c, ok := raw.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("%s: Expected a dictionary of items.", name)
		}
		if err := requireStrings(c, "entity_id", "url", "x509cert"); err != nil {
			return nil, fmt.Errorf("%s: %v", name, err)
		}
		if u, err := url.Parse(c["url"].(string)); err != nil || u.Scheme == "" || u.Host == "" {
			return nil, fmt.Errorf("%s: url: Enter a valid URL.", name)
		}
		cert, err := parseCertificate(c["x509cert"].(string))
		if err != nil {
			return nil, fmt.Errorf("%s: x509cert: %v", name, err)
		}
		p := &samlIdP{name: name, entityID: c["entity_id"].(string), url: c["url"].(string), cert: cert, conf: map[string]string{}}
		for k, val := range c {
			if s, ok := val.(string); ok {
				p.conf[k] = s
			}
		}
		out[name] = p
	}
	return out, nil
}

var samlSecurityKeys = map[string]bool{"nameIdEncrypted": true, "authnRequestsSigned": true, "logoutRequestSigned": true, "logoutResponseSigned": true,
	"signMetadata": true, "wantMessagesSigned": true, "wantAssertionsSigned": true, "wantAssertionsEncrypted": true, "wantNameId": true,
	"wantNameIdEncrypted": true, "wantAttributeStatement": true, "requestedAuthnContext": true, "requestedAuthnContextComparison": true,
	"metadataValidUntil": true, "metadataCacheDuration": true, "signatureAlgorithm": true, "digestAlgorithm": true}

// validateHybrid checks the known keys of an AWX HybridDictField; strict
// ones refuse the others.
func validateHybrid(v any, keys map[string]string, strict bool) error {
	m, err := stringMap(v)
	if err != nil {
		return err
	}
	var bad []string
	for k, val := range m {
		kind, known := keys[k]
		if !known {
			if strict {
				bad = append(bad, `"`+k+`"`)
			}
			continue
		}
		if val == nil {
			continue
		}
		switch kind {
		case "bool":
			if _, ok := val.(bool); !ok {
				return fmt.Errorf("%s: Must be a valid boolean.", k)
			}
		case "string":
			if _, ok := val.(string); !ok {
				return fmt.Errorf("%s: Not a valid string.", k)
			}
		case "strings":
			items, ok := val.([]any)
			if !ok {
				if _, isStr := val.(string); !isStr {
					return fmt.Errorf(`%s: Expected a list of items but got type "%s".`, k, pyName(val))
				}
			}
			for _, it := range items {
				if _, ok := it.(string); !ok {
					return fmt.Errorf("%s: Not a valid string.", k)
				}
			}
		}
	}
	if len(bad) > 0 {
		sort.Strings(bad)
		return fmt.Errorf("Invalid key(s): %s.", strings.Join(bad, ", "))
	}
	return nil
}

func validateSAMLSecurity(v any) error {
	m, err := stringMap(v)
	if err != nil {
		return err
	}
	for k, val := range m {
		if !samlSecurityKeys[k] || val == nil {
			continue
		}
		switch k {
		case "requestedAuthnContext":
			if _, err := parseExprAs(val, false); err != nil {
				return fmt.Errorf("%s: %v", k, err)
			}
		case "requestedAuthnContextComparison", "metadataValidUntil", "metadataCacheDuration", "signatureAlgorithm", "digestAlgorithm":
			if _, ok := val.(string); !ok {
				return fmt.Errorf("%s: Not a valid string.", k)
			}
		default:
			if _, ok := val.(bool); !ok {
				return fmt.Errorf("%s: Must be a valid boolean.", k)
			}
		}
	}
	return nil
}

var (
	samlOrgAttrKeys = map[string]string{"remove": "bool", "saml_attr": "string", "remove_admins": "bool", "saml_admin_attr": "string",
		"remove_auditors": "bool", "saml_auditor_attr": "string"}
	samlTeamAttrKeys = map[string]string{"team_org_map": "list", "remove": "bool", "saml_attr": "string"}
	samlFlagKeys     = map[string]string{"is_superuser_attr": "string", "is_superuser_value": "strings", "is_superuser_role": "strings",
		"remove_superusers": "bool", "is_system_auditor_attr": "string", "is_system_auditor_value": "strings", "is_system_auditor_role": "strings",
		"remove_system_auditors": "bool"}
)

func validateSAMLTeamAttr(v any) error {
	if err := validateHybrid(v, samlTeamAttrKeys, true); err != nil {
		return err
	}
	m, _ := stringMap(v)
	entries, ok := m["team_org_map"].([]any)
	if m["team_org_map"] != nil && !ok {
		return errors.New(`team_org_map: Expected a list of items.`)
	}
	for i, e := range entries {
		if err := validateHybrid(e, map[string]string{"team": "string", "team_alias": "string", "organization": "string"}, true); err != nil {
			return fmt.Errorf("team_org_map %d: %v", i+1, err)
		}
		em, _ := e.(map[string]any)
		if err := requireStrings(em, "team", "organization"); err != nil {
			return fmt.Errorf("team_org_map %d: %v", i+1, err)
		}
	}
	return nil
}

// samlValidators are the checks of the SAML settings.
func samlValidators() map[string]func(any) error {
	return map[string]func(any) error{
		"SOCIAL_AUTH_SAML_SP_PUBLIC_CERT": func(v any) error {
			if s, _ := v.(string); strings.TrimSpace(s) != "" {
				_, err := parseCertificate(s)
				return err
			}
			return nil
		},
		"SOCIAL_AUTH_SAML_SP_PRIVATE_KEY": func(v any) error {
			if s, _ := v.(string); strings.TrimSpace(s) != "" && s != "$encrypted$" {
				_, err := parsePrivateKey(s)
				return err
			}
			return nil
		},
		"SOCIAL_AUTH_SAML_ORG_INFO":           validateSAMLOrgInfo,
		"SOCIAL_AUTH_SAML_TECHNICAL_CONTACT":  validateSAMLContact,
		"SOCIAL_AUTH_SAML_SUPPORT_CONTACT":    validateSAMLContact,
		"SOCIAL_AUTH_SAML_ENABLED_IDPS":       func(v any) error { _, err := parseSAMLIdPs(v); return err },
		"SOCIAL_AUTH_SAML_SECURITY_CONFIG":    validateSAMLSecurity,
		"SOCIAL_AUTH_SAML_ORGANIZATION_MAP":   func(v any) error { _, err := ParseOrgMap(v); return err },
		"SOCIAL_AUTH_SAML_TEAM_MAP":           func(v any) error { _, err := ParseTeamMap(v); return err },
		"SOCIAL_AUTH_SAML_ORGANIZATION_ATTR":  func(v any) error { return validateHybrid(v, samlOrgAttrKeys, true) },
		"SOCIAL_AUTH_SAML_TEAM_ATTR":          validateSAMLTeamAttr,
		"SOCIAL_AUTH_SAML_USER_FLAGS_BY_ATTR": func(v any) error { return validateHybrid(v, samlFlagKeys, true) },
	}
}

// SAML logs users in through SAML identity providers.
type SAML struct {
	auth *Auth
	// used remembers assertion IDs until they expire, so each is used once.
	mu   sync.Mutex
	used map[string]time.Time
}

// baseURL is TOWER_URL_BASE, as AWX derives its SAML URLs from it, or the
// server's base URL.
func samlBase(app *api.App, get func(string) any) string {
	if s, _ := get("TOWER_URL_BASE").(string); strings.TrimSpace(s) != "" {
		return strings.TrimSuffix(s, "/")
	}
	return strings.TrimSuffix(app.Cfg.BaseURL, "/")
}

func (s *SAML) config(ctx context.Context) (*samlConfig, error) {
	app := s.auth.app
	get := func(k string) any { return app.Settings.Get(ctx, k) }
	base := samlBase(app, get)
	cfg := &samlConfig{}
	cfg.entityID, _ = get("SOCIAL_AUTH_SAML_SP_ENTITY_ID").(string)
	if cfg.entityID == "" {
		cfg.entityID = base
	}
	acs, _ := url.Parse(base + "/sso/complete/saml/")
	meta, _ := url.Parse(base + "/sso/metadata/saml/")
	cfg.acsURL, cfg.metaURL = *acs, *meta
	var missing []string
	if certPEM, _ := get("SOCIAL_AUTH_SAML_SP_PUBLIC_CERT").(string); certPEM != "" {
		c, err := parseCertificate(certPEM)
		if err != nil {
			return nil, fmt.Errorf("SOCIAL_AUTH_SAML_SP_PUBLIC_CERT: %v", err)
		}
		cfg.cert = c
	} else {
		missing = append(missing, "SOCIAL_AUTH_SAML_SP_PUBLIC_CERT")
	}
	if enc, _ := get("SOCIAL_AUTH_SAML_SP_PRIVATE_KEY").(string); enc != "" {
		keyPEM, err := app.Crypto.Decrypt(enc)
		if err != nil {
			return nil, err
		}
		if cfg.key, err = parsePrivateKey(keyPEM); err != nil {
			return nil, fmt.Errorf("SOCIAL_AUTH_SAML_SP_PRIVATE_KEY: %v", err)
		}
	} else {
		missing = append(missing, "SOCIAL_AUTH_SAML_SP_PRIVATE_KEY")
	}
	cfg.orgInfo, _ = get("SOCIAL_AUTH_SAML_ORG_INFO").(map[string]any)
	cfg.technical, _ = get("SOCIAL_AUTH_SAML_TECHNICAL_CONTACT").(map[string]any)
	cfg.support, _ = get("SOCIAL_AUTH_SAML_SUPPORT_CONTACT").(map[string]any)
	for key, v := range map[string]map[string]any{"SOCIAL_AUTH_SAML_ORG_INFO": cfg.orgInfo, "SOCIAL_AUTH_SAML_TECHNICAL_CONTACT": cfg.technical,
		"SOCIAL_AUTH_SAML_SUPPORT_CONTACT": cfg.support} {
		if len(v) == 0 {
			missing = append(missing, key)
		}
	}
	var err error
	if cfg.idps, err = parseSAMLIdPs(get("SOCIAL_AUTH_SAML_ENABLED_IDPS")); err != nil {
		return nil, fmt.Errorf("SOCIAL_AUTH_SAML_ENABLED_IDPS: %v", err)
	}
	if len(cfg.idps) == 0 {
		missing = append(missing, "SOCIAL_AUTH_SAML_ENABLED_IDPS")
	}
	if cfg.entityID == "" {
		missing = append(missing, "SOCIAL_AUTH_SAML_SP_ENTITY_ID")
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return nil, fmt.Errorf("SAML is not configured: set %s.", strings.Join(missing, ", "))
	}
	cfg.security, _ = get("SOCIAL_AUTH_SAML_SECURITY_CONFIG").(map[string]any)
	if cfg.orgMap, err = ParseOrgMap(get("SOCIAL_AUTH_SAML_ORGANIZATION_MAP")); err != nil {
		return nil, err
	}
	if cfg.teamMap, err = ParseTeamMap(get("SOCIAL_AUTH_SAML_TEAM_MAP")); err != nil {
		return nil, err
	}
	cfg.orgAttr, _ = get("SOCIAL_AUTH_SAML_ORGANIZATION_ATTR").(map[string]any)
	cfg.teamAttr, _ = get("SOCIAL_AUTH_SAML_TEAM_ATTR").(map[string]any)
	cfg.flags, _ = get("SOCIAL_AUTH_SAML_USER_FLAGS_BY_ATTR").(map[string]any)
	return cfg, nil
}

// Enabled reports whether identity providers are configured (AWX lists
// SAML logins when SOCIAL_AUTH_SAML_ENABLED_IDPS is set).
func (s *SAML) Enabled(ctx context.Context) bool {
	m, _ := s.auth.app.Settings.Get(ctx, "SOCIAL_AUTH_SAML_ENABLED_IDPS").(map[string]any)
	return len(m) > 0
}

func (s *SAML) security(cfg *samlConfig, k string) bool {
	b, _ := cfg.security[k].(bool)
	return b
}

// serviceProvider is the service provider for one identity provider.
func (s *SAML) serviceProvider(cfg *samlConfig, idp *samlIdP) *saml.ServiceProvider {
	sp := &saml.ServiceProvider{
		EntityID: cfg.entityID, Key: cfg.key, Certificate: cfg.cert, MetadataURL: cfg.metaURL, AcsURL: cfg.acsURL,
		AuthnNameIDFormat: saml.UnspecifiedNameIDFormat,
		// As AWX (python3-saml without a request ID), responses the
		// identity provider starts are accepted.
		AllowIDPInitiated: true,
		IDPMetadata: &saml.EntityDescriptor{EntityID: idp.entityID, IDPSSODescriptors: []saml.IDPSSODescriptor{{
			SSODescriptor: saml.SSODescriptor{RoleDescriptor: saml.RoleDescriptor{
				ProtocolSupportEnumeration: "urn:oasis:names:tc:SAML:2.0:protocol",
				KeyDescriptors: []saml.KeyDescriptor{{Use: "signing", KeyInfo: saml.KeyInfo{X509Data: saml.X509Data{
					X509Certificates: []saml.X509Certificate{{Data: base64.StdEncoding.EncodeToString(idp.cert.Raw)}}}}}},
			}},
			SingleSignOnServices: []saml.Endpoint{{Binding: saml.HTTPRedirectBinding, Location: idp.url}, {Binding: saml.HTTPPostBinding, Location: idp.url}},
		}}},
	}
	if s.security(cfg, "authnRequestsSigned") {
		sp.SignatureMethod = "http://www.w3.org/2001/04/xmldsig-more#rsa-sha256"
		if alg, _ := cfg.security["signatureAlgorithm"].(string); alg != "" {
			sp.SignatureMethod = alg
		}
	}
	if e, _ := parseExprAs(cfg.security["requestedAuthnContext"], false); len(e.literals) > 0 {
		cmp, _ := cfg.security["requestedAuthnContextComparison"].(string)
		if cmp == "" {
			cmp = "exact"
		}
		sp.RequestedAuthnContext = &saml.RequestedAuthnContext{Comparison: cmp, AuthnContextClassRef: e.literals[0]}
	}
	return sp
}

// Routes registers the SAML endpoints.
func (s *SAML) Routes() {
	s.auth.app.HandlePublic("GET", "/sso/login/saml/", s.login)
	s.auth.app.HandlePublic("POST", "/sso/complete/saml/", s.complete)
	s.auth.app.HandlePublic("GET", "/sso/metadata/saml/", s.metadata)
}

func (s *SAML) fail(w http.ResponseWriter, r *http.Request, msg string, err error) {
	slog.Warn("saml login failed", "reason", msg, "err", err)
	http.Redirect(w, r, "/login?error="+url.QueryEscape(msg), http.StatusFound)
}

type samlRelayState struct {
	IdP  string `json:"idp"`
	Next string `json:"next"`
}

func (s *SAML) login(c *api.Ctx, w http.ResponseWriter, r *http.Request) {
	cfg, err := s.config(c)
	if err != nil {
		s.fail(w, r, "SAML is not configured.", err)
		return
	}
	idp := cfg.idps[r.URL.Query().Get("idp")]
	if idp == nil {
		s.fail(w, r, "Unknown SAML identity provider.", nil)
		return
	}
	relay, _ := json.Marshal(samlRelayState{IdP: idp.name, Next: safeNext(r.URL.Query().Get("next"))})
	u, err := s.serviceProvider(cfg, idp).MakeRedirectAuthenticationRequest(string(relay))
	if err != nil {
		s.fail(w, r, "Internal error.", err)
		return
	}
	http.Redirect(w, r, u.String(), http.StatusFound)
}

// signatures reports which parts of a SAML response are signed and
// whether its assertion is encrypted.
func signatures(form string) (response, assertion, encrypted bool) {
	raw, err := base64.StdEncoding.DecodeString(form)
	if err != nil {
		return
	}
	doc := etree.NewDocument()
	if doc.ReadFromBytes(raw) != nil || doc.Root() == nil {
		return
	}
	root := doc.Root()
	response = root.FindElement("./Signature") != nil
	assertion = root.FindElement("./Assertion/Signature") != nil
	encrypted = root.FindElement("./EncryptedAssertion") != nil
	return
}

func (s *SAML) complete(c *api.Ctx, w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		s.fail(w, r, "Invalid SAML response.", err)
		return
	}
	cfg, err := s.config(c)
	if err != nil {
		s.fail(w, r, "SAML is not configured.", err)
		return
	}
	// RelayState is {"idp", "next"}, or an identity provider's name.
	var relay samlRelayState
	if json.Unmarshal([]byte(r.PostForm.Get("RelayState")), &relay) != nil {
		relay = samlRelayState{IdP: r.PostForm.Get("RelayState")}
	}
	idp := cfg.idps[relay.IdP]
	if idp == nil {
		s.fail(w, r, "Unknown SAML identity provider.", nil)
		return
	}
	sp := s.serviceProvider(cfg, idp)
	assertion, err := sp.ParseResponse(r, nil)
	if err != nil {
		var ie *saml.InvalidResponseError
		if errors.As(err, &ie) {
			err = ie.PrivateErr
		}
		s.fail(w, r, "The SAML response is not valid.", err)
		return
	}
	respSigned, assertionSigned, encrypted := signatures(r.PostForm.Get("SAMLResponse"))
	switch {
	case s.security(cfg, "wantMessagesSigned") && !respSigned:
		s.fail(w, r, "The SAML response is not signed.", nil)
		return
	case s.security(cfg, "wantAssertionsSigned") && !assertionSigned && !encrypted:
		s.fail(w, r, "The SAML assertion is not signed.", nil)
		return
	case s.security(cfg, "wantAssertionsEncrypted") && !encrypted:
		s.fail(w, r, "The SAML assertion is not encrypted.", nil)
		return
	}
	if !s.useOnce(assertion) {
		s.fail(w, r, "This SAML response was already used.", nil)
		return
	}
	attrs := map[string][]string{}
	for _, st := range assertion.AttributeStatements {
		for _, a := range st.Attributes {
			for _, v := range a.Values {
				attrs[a.Name] = append(attrs[a.Name], v.Value)
			}
		}
	}
	// python-social-auth offers the subject's NameID as "name_id".
	if assertion.Subject != nil && assertion.Subject.NameID != nil {
		attrs["name_id"] = []string{assertion.Subject.NameID.Value}
	}
	if s.security(cfg, "wantNameId") && len(attrs["name_id"]) == 0 {
		s.fail(w, r, "The SAML assertion has no NameID.", nil)
		return
	}
	uid, err := s.provision(c, cfg, idp, attrs)
	if err != nil {
		s.fail(w, r, err.Error(), err)
		return
	}
	if err := s.auth.StartSession(c, w, r, uid); err != nil {
		s.fail(w, r, "Internal error.", err)
		return
	}
	http.Redirect(w, r, safeNext(relay.Next), http.StatusFound)
}

// useOnce refuses an assertion seen before, until it expires.
func (s *SAML) useOnce(a *saml.Assertion) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	for id, exp := range s.used {
		if now.After(exp) {
			delete(s.used, id)
		}
	}
	if _, seen := s.used[a.ID]; seen {
		return false
	}
	exp := now.Add(10 * time.Minute)
	if a.Conditions != nil && !a.Conditions.NotOnOrAfter.IsZero() {
		exp = a.Conditions.NotOnOrAfter.Add(saml.MaxClockSkew)
	}
	if s.used == nil {
		s.used = map[string]time.Time{}
	}
	s.used[a.ID] = exp
	return true
}

func (s *SAML) metadata(c *api.Ctx, w http.ResponseWriter, r *http.Request) {
	cfg, err := s.config(c)
	if err != nil {
		// As AWX, the errors are the response.
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		fmt.Fprint(w, err.Error())
		return
	}
	var idp *samlIdP
	for _, p := range cfg.idps {
		idp = p
		break
	}
	md := s.serviceProvider(cfg, idp).Metadata()
	sp := &md.SPSSODescriptors[0]
	org := &saml.Organization{}
	langs := make([]string, 0, len(cfg.orgInfo))
	for lang := range cfg.orgInfo {
		langs = append(langs, lang)
	}
	sort.Strings(langs)
	for _, lang := range langs {
		info, _ := cfg.orgInfo[lang].(map[string]any)
		name, _ := info["name"].(string)
		display, _ := info["displayname"].(string)
		u, _ := info["url"].(string)
		org.OrganizationNames = append(org.OrganizationNames, saml.LocalizedName{Lang: lang, Value: name})
		org.OrganizationDisplayNames = append(org.OrganizationDisplayNames, saml.LocalizedName{Lang: lang, Value: display})
		org.OrganizationURLs = append(org.OrganizationURLs, saml.LocalizedURI{Lang: lang, Value: u})
	}
	md.Organization = org
	for _, contact := range []struct {
		kind string
		info map[string]any
	}{{"technical", cfg.technical}, {"support", cfg.support}} {
		name, _ := contact.info["givenName"].(string)
		email, _ := contact.info["emailAddress"].(string)
		sp.ContactPeople = append(sp.ContactPeople, saml.ContactPerson{ContactType: contact.kind, GivenName: name, EmailAddresses: []string{email}})
	}
	b, err := xml.MarshalIndent(md, "", "  ")
	if err != nil {
		api.WriteError(w, err)
		return
	}
	w.Header().Set("Content-Type", "text/xml")
	w.Write(b)
}

// details are the user fields of a SAML login, as AWX's identity provider
// reads them: the first value of each attribute.
func samlDetails(idp *samlIdP, attrs map[string][]string) (permanent string, fields map[string]string) {
	first := func(name string) string {
		if v := attrs[name]; len(v) > 0 {
			return v[0]
		}
		return ""
	}
	fields = map[string]string{
		"username":   first(idp.attr("attr_username", oidUserID)),
		"email":      first(idp.attr("attr_email", oidMail)),
		"first_name": first(idp.attr("attr_first_name", oidGivenName)),
		"last_name":  first(idp.attr("attr_last_name", oidSurname)),
	}
	return first(idp.attr("attr_user_permanent_id", oidUserID)), fields
}

var usernameCleanRe = regexp.MustCompile(`[^\w.@+-]+`)

func randomHex(n int) string {
	b := make([]byte, (n+1)/2)
	rand.Read(b)
	return hex.EncodeToString(b)[:n]
}

// provision finds or creates the user, as python-social-auth's pipeline
// does, and applies AWX's SAML mapping.
func (s *SAML) provision(ctx context.Context, cfg *samlConfig, idp *samlIdP, attrs map[string][]string) (int64, error) {
	permanent, details := samlDetails(idp, attrs)
	if permanent == "" {
		return 0, fmt.Errorf("The SAML response has no %s attribute to identify the user.", idp.attr("attr_user_permanent_id", oidUserID))
	}
	uid := idp.name + ":" + permanent
	tx, err := s.auth.app.DB.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)
	var id int64
	var active, superuser, auditor bool
	var username, email string
	err = tx.QueryRow(ctx, "SELECT id, is_active, is_superuser, is_system_auditor, username, email FROM users WHERE external_source = 'saml' AND external_id = $1",
		uid).Scan(&id, &active, &superuser, &auditor, &username, &email)
	if err != nil {
		// python-social-auth associates an account with the same email,
		// which AWX's SAML backend then refuses unless it is that SAML user.
		if details["email"] != "" {
			var n int
			tx.QueryRow(ctx, "SELECT count(*) FROM users WHERE lower(email) = lower($1)", details["email"]).Scan(&n)
			if n > 0 {
				return 0, errors.New("The given email address is associated with another account.")
			}
		}
		// get_username: cleaned, and made unique with random hex.
		base := usernameCleanRe.ReplaceAllString(details["username"], "")
		if base == "" {
			base = randomHex(32)
		}
		if len(base) > 150 {
			base = base[:150]
		}
		username = base
		for {
			var taken bool
			tx.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM users WHERE lower(username) = lower($1))", username).Scan(&taken)
			if !taken {
				break
			}
			short := base
			if len(short) > 150-16 {
				short = short[:150-16]
			}
			username = short + randomHex(16)
		}
		email, active = details["email"], true
		if err := tx.QueryRow(ctx, `INSERT INTO users (username, email, external_source, external_id) VALUES ($1, $2, 'saml', $3) RETURNING id`,
			username, email, uid).Scan(&id); err != nil {
			return 0, err
		}
	}
	if !active {
		return 0, errors.New("Your account is inactive")
	}
	// user_details: the names follow the identity provider; the email is
	// set when the account is created.
	if _, err := tx.Exec(ctx, `UPDATE users SET first_name = coalesce(nullif($2, ''), first_name), last_name = coalesce(nullif($3, ''), last_name),
		last_login = now() WHERE id = $1`, id, details["first_name"], details["last_name"]); err != nil {
		return 0, err
	}
	res := samlMapping(cfg, username, email, attrs, superuser, auditor)
	warnings, err := ApplyMapping(ctx, tx, id, "saml", res)
	if err != nil {
		return 0, err
	}
	for _, w := range warnings {
		slog.Warn("saml mapping", "user", username, "warning", w)
	}
	return id, tx.Commit(ctx)
}

// samlMapping is AWX's SAML pipeline (populate_user and update_user_flags):
// organizations and teams from attributes, then the organization and team
// maps, where a null expression removes like false, then the flags.
func samlMapping(cfg *samlConfig, username, email string, attrs map[string][]string, superuser, auditor bool) MappingResult {
	type target struct{ role, org, team string }
	// state: true grants, false revokes, nil leaves as it is.
	state := map[target]*bool{}
	var order []target
	set := func(t target, v *bool) {
		if _, ok := state[t]; !ok {
			order = append(order, t)
		}
		state[t] = v
	}
	yes, no := true, false
	res := MappingResult{Grants: []Grant{}, Keep: []Grant{}, Errors: []string{}}
	opt := func(m map[string]any, k string) bool {
		v, ok := m[k]
		if !ok {
			return true
		}
		b, _ := v.(bool)
		return b
	}
	alias := func(org string) string {
		for _, o := range cfg.orgMap {
			if o.name == org && o.alias != "" {
				return o.alias
			}
		}
		return org
	}
	// Organizations from attributes; with remove (the default) every other
	// membership of that role goes.
	for _, r := range []struct{ role, remove, attr string }{
		{"Organization Member", "remove", "saml_attr"}, {"Organization Admin", "remove_admins", "saml_admin_attr"},
		{"Organization Audit", "remove_auditors", "saml_auditor_attr"},
	} {
		if !opt(cfg.orgAttr, r.remove) {
			res.KeepRoles = append(res.KeepRoles, r.role)
		}
		if name, _ := cfg.orgAttr[r.attr].(string); name != "" {
			for _, org := range attrs[name] {
				set(target{r.role, alias(org), ""}, &yes)
			}
		}
	}
	// Teams from an attribute.
	if name, _ := cfg.teamAttr["saml_attr"].(string); name != "" {
		if !opt(cfg.teamAttr, "remove") {
			res.KeepRoles = append(res.KeepRoles, "Team Member")
		}
		member := map[string]bool{}
		for _, t := range attrs[name] {
			member[t] = true
		}
		entries, _ := cfg.teamAttr["team_org_map"].([]any)
		for _, e := range entries {
			m, _ := e.(map[string]any)
			team, _ := m["team"].(string)
			teamAlias, _ := m["team_alias"].(string)
			org, _ := m["organization"].(string)
			if member[team] {
				if org == "" {
					continue
				}
				if teamAlias != "" {
					team = teamAlias
				}
				set(target{"Team Member", org, team}, &yes)
			} else {
				set(target{"Team Member", org, team}, &no)
			}
		}
	}
	match := IdentityMatcher(username, email)
	decide := func(e mapExpr, remove bool) *bool {
		switch {
		case e.set && e.matches(match):
			return &yes
		case remove:
			return &no
		}
		return nil
	}
	// The organization map: a role the attributes grant stays granted.
	for _, o := range cfg.orgMap {
		org := o.name
		if o.alias != "" {
			org = o.alias
		}
		for _, r := range []struct {
			role   string
			expr   mapExpr
			remove bool
		}{{"Organization Admin", o.admins, o.removeAdmins}, {"Organization Member", o.users, o.removeUsers}, {"Organization Audit", o.auditors, true}} {
			t := target{r.role, org, ""}
			if v := state[t]; v != nil && *v {
				continue
			}
			set(t, decide(r.expr, r.remove))
		}
	}
	// The team map overrides the team attribute.
	for _, tm := range cfg.teamMap {
		if v := decide(tm.users, tm.remove); v != nil {
			set(target{"Team Member", tm.organization, tm.name}, v)
		}
	}
	for _, t := range order {
		g := Grant{RoleDefinition: t.role, Organization: t.org, Team: t.team, Map: "SAML", Create: true}
		switch v := state[t]; {
		case v == nil:
			res.Keep = append(res.Keep, g)
		case *v:
			res.Grants = append(res.Grants, g)
		default:
			res.Revoke = append(res.Revoke, g)
		}
	}
	res.Superuser = samlFlag(cfg.flags, "superuser", attrs, superuser)
	res.Auditor = samlFlag(cfg.flags, "system_auditor", attrs, auditor)
	return res
}

func asStrings(v any) []string {
	switch x := v.(type) {
	case string:
		return []string{x}
	case []any:
		var out []string
		for _, e := range x {
			if s, ok := e.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

func intersects(a, b []string) bool {
	for _, x := range a {
		for _, y := range b {
			if x == y {
				return true
			}
		}
	}
	return false
}

// samlFlag is AWX's _check_flag: the Role attribute against is_<flag>_role,
// then is_<flag>_attr (with is_<flag>_value); without remove_<flag>s a flag
// the user has is kept.
func samlFlag(flags map[string]any, flag string, attrs map[string][]string, old bool) *bool {
	value := false
	if roles := asStrings(flags["is_"+flag+"_role"]); len(roles) > 0 && intersects(roles, attrs["Role"]) {
		value = true
	}
	if attr, _ := flags["is_"+flag+"_attr"].(string); attr != "" && len(attrs[attr]) > 0 {
		if want := asStrings(flags["is_"+flag+"_value"]); len(want) > 0 {
			value = intersects(want, attrs[attr])
		} else {
			value = true
		}
	}
	remove := true
	if v, ok := flags["remove_"+flag+"s"]; ok {
		remove, _ = v.(bool)
	}
	if !remove && old && !value {
		return nil
	}
	return &value
}

// samlBackends lists the identity providers for /api/v2/auth/, as AWX does.
func (s *SAML) backends(ctx context.Context, out api.Obj) {
	m, _ := s.auth.app.Settings.Get(ctx, "SOCIAL_AUTH_SAML_ENABLED_IDPS").(map[string]any)
	base := samlBase(s.auth.app, func(k string) any { return s.auth.app.Settings.Get(ctx, k) })
	for name := range m {
		out["saml:"+name] = api.Obj{"name": name, "login_url": "/sso/login/saml/?idp=" + url.QueryEscape(name),
			"complete_url": base + "/sso/complete/saml/", "metadata_url": base + "/sso/metadata/saml/"}
	}
}
