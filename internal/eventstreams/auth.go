package eventstreams

import (
	"context"
	"crypto/hmac"
	"crypto/sha1" //nolint:gosec // HMAC-SHA1, for the senders that sign only with it
	"crypto/sha256"
	"crypto/sha512"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"hash"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"

	"github.com/damhau/awxnext/internal/netguard"
)

// The credential types of event streams, by namespace.
const (
	CredHMAC   = "event_stream_hmac"
	CredGitHub = "event_stream_github"
	CredGitLab = "event_stream_gitlab"
	CredToken  = "event_stream_token"
	CredBasic  = "event_stream_basic"
	CredMTLS   = "event_stream_mtls"
	CredOIDC   = "event_stream_oidc"
)

// IsStreamCredential reports whether namespace is an event stream's
// credential type.
func IsStreamCredential(namespace string) bool { return strings.HasPrefix(namespace, "event_stream_") }

// The settings the receiver reads.
const (
	// TrustedProxiesSetting lists the proxies whose client certificate
	// header is believed. Empty: none, and mTLS streams refuse every request.
	TrustedProxiesSetting = "EVENT_STREAM_TRUSTED_PROXIES"
	// ClientCertHeaderSetting names the header that carries the client's
	// certificate.
	ClientCertHeaderSetting = "EVENT_STREAM_CLIENT_CERT_HEADER"
	// LocalAddressesSetting lists the loopback and link-local addresses
	// OIDC issuers may be at.
	LocalAddressesSetting = "EVENT_STREAM_ALLOWED_LOCAL_ADDRESSES"
)

// Credential is a stream's credential, its secrets decrypted.
type Credential struct {
	Namespace string
	Inputs    map[string]any
}

func (c *Credential) input(key, def string) string {
	if s, ok := c.Inputs[key].(string); ok && s != "" {
		return s
	}
	return def
}

// Policy is what the settings allow the receiver.
type Policy struct {
	TrustedProxies   []netip.Prefix
	ClientCertHeader string
	LocalAddresses   []string
}

// NewPolicy reads the settings' values, skipping invalid entries (the
// settings API refuses them when they are saved).
func NewPolicy(trustedProxies []string, clientCertHeader string, localAddresses []string) Policy {
	p := Policy{ClientCertHeader: clientCertHeader, LocalAddresses: localAddresses}
	p.TrustedProxies, _ = netguard.ParseNetworks(trustedProxies)
	if p.ClientCertHeader == "" {
		p.ClientCertHeader = "X-Forwarded-Client-Cert"
	}
	return p
}

// Verify checks that a request to a stream comes from its sender: body is
// the request's body. A refusal is an *Error: 401, or 403 for a valid
// token whose claims the stream refuses.
func Verify(ctx context.Context, p Policy, cred *Credential, r *http.Request, body []byte) error {
	switch cred.Namespace {
	case CredHMAC:
		return verifyHMAC(cred, r, body)
	case CredGitHub:
		return verifySignature(r.Header.Get("X-Hub-Signature-256"), "sha256=", "hex", sha256.New, cred.input("secret", ""), body)
	case CredGitLab:
		return check(secretEqual(r.Header.Get("X-Gitlab-Token"), cred.input("token", "")))
	case CredToken:
		header := cred.input("http_header_key", "Authorization")
		got := r.Header.Get(header)
		if strings.EqualFold(header, "Authorization") {
			scheme, token, _ := strings.Cut(got, " ")
			if !strings.EqualFold(scheme, "Bearer") {
				return errUnauthorized
			}
			got = strings.TrimSpace(token)
		}
		return check(secretEqual(got, cred.input("token", "")))
	case CredBasic:
		user, pass, ok := r.BasicAuth()
		return check(ok && secretEqual(user, cred.input("username", "")) && secretEqual(pass, cred.input("password", "")))
	case CredMTLS:
		return verifyClientCert(p, cred, r)
	case CredOIDC:
		return verifyOIDC(ctx, p, cred, r)
	}
	return errUnauthorized
}

func check(ok bool) error {
	if !ok {
		return errUnauthorized
	}
	return nil
}

// secretEqual compares in constant time, whatever the lengths; an empty
// secret matches nothing.
func secretEqual(got, want string) bool {
	if want == "" {
		return false
	}
	a, b := sha256.Sum256([]byte(got)), sha256.Sum256([]byte(want))
	return hmac.Equal(a[:], b[:])
}

var hashes = map[string]func() hash.Hash{"sha1": sha1.New, "sha256": sha256.New, "sha512": sha512.New}

func verifyHMAC(cred *Credential, r *http.Request, body []byte) error {
	h, ok := hashes[cred.input("hash_algorithm", "sha256")]
	if !ok {
		return errUnauthorized
	}
	prefix, set := cred.Inputs["signature_prefix"].(string)
	if !set {
		prefix = "sha256="
	}
	return verifySignature(r.Header.Get(cred.input("http_header_key", "X-Hub-Signature-256")), prefix,
		cred.input("signature_encoding", "hex"), h, cred.input("secret", ""), body)
}

func verifySignature(header, prefix, encoding string, h func() hash.Hash, secret string, body []byte) error {
	if secret == "" || !strings.HasPrefix(header, prefix) {
		return errUnauthorized
	}
	sig := strings.TrimSpace(strings.TrimPrefix(header, prefix))
	var got []byte
	var err error
	if encoding == "base64" {
		got, err = base64.StdEncoding.DecodeString(sig)
	} else {
		got, err = hex.DecodeString(sig)
	}
	if err != nil {
		return errUnauthorized
	}
	mac := hmac.New(h, []byte(secret))
	mac.Write(body)
	return check(hmac.Equal(got, mac.Sum(nil)))
}

// verifyClientCert checks the client certificate a trusted proxy passes
// in its header: it must chain to the credential's CA, and name the
// credential's subject when it gives one. The header of any other client
// is ignored, since a client can send any header it likes.
func verifyClientCert(p Policy, cred *Credential, r *http.Request) error {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	peer, err := netip.ParseAddr(host)
	if err != nil || !trusted(p.TrustedProxies, peer.Unmap()) {
		slog.Debug("event stream: client certificate header from an untrusted address", "peer", r.RemoteAddr)
		return errUnauthorized
	}
	certs, err := headerCertificates(r.Header.Get(p.ClientCertHeader))
	if err != nil || len(certs) == 0 {
		return errUnauthorized
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM([]byte(cred.input("ca_certificate", ""))) {
		return errUnauthorized
	}
	inter := x509.NewCertPool()
	for _, c := range certs[1:] {
		inter.AddCert(c)
	}
	leaf := certs[0]
	if _, err := leaf.Verify(x509.VerifyOptions{Roots: roots, Intermediates: inter, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}); err != nil {
		return errUnauthorized
	}
	if want := cred.input("subject", ""); want != "" && !certNames(leaf)[want] {
		return errUnauthorized
	}
	return nil
}

func trusted(proxies []netip.Prefix, a netip.Addr) bool {
	for _, p := range proxies {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

func certNames(c *x509.Certificate) map[string]bool {
	names := map[string]bool{c.Subject.String(): true}
	if c.Subject.CommonName != "" {
		names[c.Subject.CommonName] = true
	}
	for _, n := range c.DNSNames {
		names[n] = true
	}
	for _, n := range c.EmailAddresses {
		names[n] = true
	}
	for _, u := range c.URIs {
		names[u.String()] = true
	}
	return names
}

// headerCertificates reads the certificates a proxy passes in a header:
// Envoy's X-Forwarded-Client-Cert (Cert and Chain, URL-encoded PEM),
// URL-encoded PEM (ingress-nginx), or base64 DER, comma-separated
// (Traefik). The client's certificate comes first.
func headerCertificates(v string) ([]*x509.Certificate, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return nil, errors.New("no certificate")
	}
	if xfcc, ok := xfccCertificates(v); ok {
		if xfcc == "" {
			return nil, errors.New("no certificate in the X-Forwarded-Client-Cert element")
		}
		v = xfcc
	}
	v = unescapePEM(v)
	if strings.Contains(v, "-----BEGIN") {
		var out []*x509.Certificate
		rest := []byte(v)
		for {
			var b *pem.Block
			if b, rest = pem.Decode(rest); b == nil {
				break
			}
			if b.Type != "CERTIFICATE" {
				continue
			}
			c, err := x509.ParseCertificate(b.Bytes)
			if err != nil {
				return nil, err
			}
			out = append(out, c)
		}
		return out, nil
	}
	var out []*x509.Certificate
	for _, part := range strings.Split(v, ",") {
		der, err := base64.StdEncoding.DecodeString(strings.TrimSpace(part))
		if err != nil {
			return nil, err
		}
		c, err := x509.ParseCertificate(der)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, nil
}

// unescapePEM undoes the URL encoding of a PEM header. Proxies write a
// space as %20, and a '+' of the base64 may stay as it is, so '+' is a
// space only when the BEGIN line shows the encoder wrote spaces that way.
func unescapePEM(v string) string {
	if strings.Contains(v, "-----BEGIN+") {
		if dec, err := url.QueryUnescape(v); err == nil {
			return dec
		}
	}
	if dec, err := url.PathUnescape(v); err == nil {
		return dec
	}
	return v
}

// xfccCertificates is the URL-encoded PEM of the Chain, or else the Cert,
// of the last element of an X-Forwarded-Client-Cert header, and false when
// v is not one. The last element is the one the closest proxy, the trusted
// one, added: with forward_client_cert_details APPEND_FORWARD the elements
// before it are what the client sent.
func xfccCertificates(v string) (string, bool) {
	elements := splitQuoted(v, ',')
	last := elements[len(elements)-1]
	var cert, chain string
	found := false
	for _, kv := range splitQuoted(last, ';') {
		k, val, ok := strings.Cut(kv, "=")
		if !ok {
			continue
		}
		val = strings.Trim(strings.TrimSpace(val), `"`)
		switch strings.ToLower(strings.TrimSpace(k)) {
		case "cert":
			cert, found = val, true
		case "chain":
			chain, found = val, true
		case "by", "hash", "subject", "uri", "dns":
			found = true
		}
	}
	if chain != "" {
		return chain, true
	}
	return cert, found
}

func splitQuoted(s string, sep rune) []string {
	var out []string
	var cur strings.Builder
	quoted := false
	for _, r := range s {
		switch {
		case r == '"':
			quoted = !quoted
			cur.WriteRune(r)
		case r == sep && !quoted:
			out = append(out, cur.String())
			cur.Reset()
		default:
			cur.WriteRune(r)
		}
	}
	return append(out, cur.String())
}

// CheckInputs checks the inputs of an event stream credential beyond what
// its type's schema says, by input.
func CheckInputs(namespace string, inputs map[string]any) map[string][]string {
	errs := map[string][]string{}
	pemInput := func(key string, required bool) {
		s, _ := inputs[key].(string)
		if s == "" {
			if required {
				errs[key] = append(errs[key], "required for this credential type.")
			}
			return
		}
		if !x509.NewCertPool().AppendCertsFromPEM([]byte(s)) {
			errs[key] = append(errs[key], "must be one or more certificates in PEM.")
		}
	}
	switch namespace {
	case CredMTLS:
		pemInput("ca_certificate", true)
	case CredOIDC:
		pemInput("ca_certificate", false)
		issuer, _ := inputs["issuer"].(string)
		if u, err := url.Parse(issuer); err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
			errs["issuer"] = append(errs["issuer"], "must be the issuer's URL, such as https://token.actions.githubusercontent.com.")
		}
		if expr, _ := inputs["claims"].(string); strings.TrimSpace(expr) != "" {
			if _, err := CompileClaims(strings.TrimSpace(expr)); err != nil {
				errs["claims"] = append(errs["claims"], "is not a valid condition: "+err.Error())
			}
		}
	}
	if len(errs) == 0 {
		return nil
	}
	return errs
}
