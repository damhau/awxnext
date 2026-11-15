package eventstreams

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"cel.dev/cel-go/cel"
	"cel.dev/cel-go/common/types"
	"github.com/coreos/go-oidc/v3/oidc"

	"github.com/damhau/awxnext/internal/netguard"
)

// providerTTL is how long a discovered issuer is kept before it is
// discovered again; its keys are fetched again sooner, when a token is
// signed with a key it does not have.
const providerTTL = time.Hour

type cachedProvider struct {
	provider *oidc.Provider
	expires  time.Time
}

var providers = struct {
	sync.Mutex
	m map[string]cachedProvider
}{m: map[string]cachedProvider{}}

// verifyOIDC checks the bearer token of a request: signed by the
// credential's issuer, for its audience, and meeting its claims condition.
// The issuer is reached through netguard: whoever may create a credential
// chooses its address.
func verifyOIDC(ctx context.Context, p Policy, cred *Credential, r *http.Request) error {
	scheme, token, _ := strings.Cut(r.Header.Get("Authorization"), " ")
	token = strings.TrimSpace(token)
	if !strings.EqualFold(scheme, "Bearer") || token == "" {
		return errUnauthorized
	}
	issuer, audience := cred.input("issuer", ""), cred.input("audience", "")
	if issuer == "" || audience == "" {
		return errUnauthorized
	}
	provider, err := discover(ctx, p, issuer, cred.input("ca_certificate", ""))
	if err != nil {
		slog.Warn("event stream: OIDC issuer", "issuer", issuer, "err", err)
		return &Error{Status: http.StatusServiceUnavailable, Message: "The stream's token issuer cannot be reached."}
	}
	idToken, err := provider.Verifier(&oidc.Config{ClientID: audience}).Verify(ctx, token)
	if err != nil {
		slog.Debug("event stream: token refused", "issuer", issuer, "err", err)
		return errUnauthorized
	}
	expr := strings.TrimSpace(cred.input("claims", ""))
	if expr == "" {
		return nil
	}
	var claims map[string]any
	if err := idToken.Claims(&claims); err != nil {
		return errUnauthorized
	}
	ok, err := claimsMatch(ctx, expr, claims)
	if err != nil {
		slog.Debug("event stream: claims condition", "err", err)
	}
	if !ok {
		return errClaims
	}
	return nil
}

func discover(ctx context.Context, p Policy, issuer, caCert string) (*oidc.Provider, error) {
	sum := sha256.Sum256([]byte(issuer + "\x00" + caCert + "\x00" + strings.Join(p.LocalAddresses, ",")))
	key := hex.EncodeToString(sum[:])
	providers.Lock()
	cp, ok := providers.m[key]
	providers.Unlock()
	if ok && time.Now().Before(cp.expires) {
		return cp.provider, nil
	}
	client, err := issuerClient(p, caCert)
	if err != nil {
		return nil, err
	}
	u, err := url.Parse(issuer)
	if err != nil {
		return nil, err
	}
	if err := guard(p).CheckURL(u); err != nil {
		return nil, err
	}
	dctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	provider, err := oidc.NewProvider(oidc.ClientContext(dctx, client), issuer)
	if err != nil {
		return nil, err
	}
	providers.Lock()
	if len(providers.m) > 1000 {
		providers.m = map[string]cachedProvider{}
	}
	providers.m[key] = cachedProvider{provider: provider, expires: time.Now().Add(providerTTL)}
	providers.Unlock()
	return provider, nil
}

func guard(p Policy) netguard.Guard {
	allowed, _ := netguard.ParseAllowed(p.LocalAddresses)
	return netguard.Guard{Allowed: allowed, Setting: LocalAddressesSetting}
}

func issuerClient(p Policy, caCert string) (*http.Client, error) {
	cfg := &tls.Config{MinVersion: tls.VersionTLS12}
	if caCert != "" {
		pool, err := x509.SystemCertPool()
		if err != nil {
			pool = x509.NewCertPool()
		}
		if !pool.AppendCertsFromPEM([]byte(caCert)) {
			return nil, errors.New("the credential's CA certificate is not a PEM certificate")
		}
		cfg.RootCAs = pool
	}
	t := &http.Transport{TLSClientConfig: cfg, Proxy: http.ProxyFromEnvironment, TLSHandshakeTimeout: 10 * time.Second}
	return &http.Client{Timeout: 10 * time.Second, Transport: guard(p).Transport(t, nil)}, nil
}

var claimsEnv = func() *cel.Env {
	env, err := cel.NewEnv(cel.Variable("claims", cel.MapType(cel.StringType, cel.DynType)))
	if err != nil {
		panic(err)
	}
	return env
}()

var programs = struct {
	sync.Mutex
	m map[string]cel.Program
}{m: map[string]cel.Program{}}

// CompileClaims checks a claims condition: a CEL expression over claims,
// the token's claims, such as claims.repository == "org/app".
func CompileClaims(expr string) (cel.Program, error) {
	programs.Lock()
	prg, ok := programs.m[expr]
	programs.Unlock()
	if ok {
		return prg, nil
	}
	ast, iss := claimsEnv.Compile(expr)
	if iss.Err() != nil {
		return nil, iss.Err()
	}
	if t := ast.OutputType(); t != cel.BoolType && t != cel.DynType {
		return nil, errors.New("the condition must be true or false, not " + t.String())
	}
	prg, err := claimsEnv.Program(ast, cel.CostLimit(100000), cel.InterruptCheckFrequency(100))
	if err != nil {
		return nil, err
	}
	programs.Lock()
	if len(programs.m) > 1000 {
		programs.m = map[string]cel.Program{}
	}
	programs.m[expr] = prg
	programs.Unlock()
	return prg, nil
}

// claimsMatch evaluates a claims condition; a claim it names that the
// token lacks makes it false.
func claimsMatch(ctx context.Context, expr string, claims map[string]any) (bool, error) {
	prg, err := CompileClaims(expr)
	if err != nil {
		return false, err
	}
	ctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	out, _, err := prg.ContextEval(ctx, map[string]any{"claims": claims})
	if err != nil {
		return false, err
	}
	return out == types.True, nil
}
