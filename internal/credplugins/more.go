package credplugins

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"unicode"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"
)

// The other lookups of AWX 24.6: CyberArk Central Credential Provider and
// Conjur, Centrify Vault, Thycotic (Delinea) DevOps Secrets Vault and Secret
// Server, and AWS Secrets Manager. Each makes the requests AWX's plugin (or
// the SDK it uses) makes.

var (
	// DSVURLTemplate builds a DevOps Secrets Vault tenant's URL from its name
	// and TLD, as AWX's url_template (a DEBUG-only input there); tests point
	// it at a fake.
	DSVURLTemplate = "https://%s.secretsvaultcloud.%s"
	// AWSEndpoint overrides the Secrets Manager endpoint (tests).
	AWSEndpoint = ""
)

// httpClient verifies the server with cacert (system CAs when empty) unless
// verify is false, presents a client certificate when given, and does not
// follow redirects, as AWX's plugins.
func httpClient(cacert, clientCert, clientKey string, verify bool) (*http.Client, error) {
	cfg := &tls.Config{InsecureSkipVerify: !verify} //nolint:gosec // the credential's Verify SSL input
	if cacert != "" {
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM([]byte(cacert)) {
			return nil, errors.New("invalid CA certificate")
		}
		cfg.RootCAs = pool
	}
	if clientCert != "" {
		pair, err := tls.X509KeyPair([]byte(clientCert), []byte(clientKey))
		if err != nil {
			return nil, fmt.Errorf("invalid client certificate: %w", err)
		}
		cfg.Certificates = []tls.Certificate{pair}
	}
	return &http.Client{
		Transport:     &http.Transport{TLSClientConfig: cfg, Proxy: http.ProxyFromEnvironment},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}, nil
}

// do sends a request and returns the body of a 2xx answer.
func do(c *http.Client, req *http.Request) ([]byte, error) {
	res, err := c.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(res.Body, 16<<20))
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return nil, fmt.Errorf("%s %s: HTTP %d: %s", req.Method, req.URL.Redacted(), res.StatusCode, strings.TrimSpace(string(body)))
	}
	return body, nil
}

// urljoin resolves ref against base, as Python's urllib.parse.urljoin.
func urljoin(base, ref string) (string, error) {
	b, err := url.Parse(base)
	if err != nil {
		return "", err
	}
	r, err := url.Parse(ref)
	if err != nil {
		return "", err
	}
	return b.ResolveReference(r).String(), nil
}

// pyQuote escapes all but letters, digits and "_.-~", as Python's quote(s, safe=”).
func pyQuote(s string) string {
	var b strings.Builder
	for _, c := range []byte(s) {
		if c < 0x80 && (unicode.IsLetter(rune(c)) || unicode.IsDigit(rune(c)) || strings.IndexByte("_.-~", c) >= 0) {
			b.WriteByte(c)
		} else {
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
}

func boolInput(m map[string]any, k string, def bool) bool {
	if v, ok := m[k].(bool); ok {
		return v
	}
	return def
}

func orDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

// cyberArkCCP is AWX's aim plugin: GET <url>/<webservice>/api/Accounts.
func cyberArkCCP(ctx context.Context, inputs, metadata map[string]any) (string, error) {
	c, err := httpClient("", s(inputs, "client_cert"), s(inputs, "client_key"), boolInput(inputs, "verify", true))
	if err != nil {
		return "", err
	}
	q := []string{"AppId=" + pyQuote(s(inputs, "app_id")), "Query=" + pyQuote(s(metadata, "object_query")),
		"QueryFormat=" + pyQuote(orDefault(s(metadata, "object_query_format"), "Exact"))}
	if r := s(metadata, "reason"); r != "" {
		q = append(q, "reason="+pyQuote(r))
	}
	u, err := urljoin(s(inputs, "url"), orDefault(s(inputs, "webservice_id"), "AIMWebService")+"/api/Accounts")
	if err != nil {
		return "", err
	}
	req, _ := http.NewRequestWithContext(ctx, "GET", u+"?"+strings.Join(q, "&"), nil)
	body, err := do(c, req)
	if err != nil {
		return "", err
	}
	var obj map[string]any
	if err := json.Unmarshal(body, &obj); err != nil {
		return "", fmt.Errorf("invalid CCP response: %w", err)
	}
	// CCP capitalizes property names (UserName in camel case).
	prop := s(metadata, "object_property")
	switch strings.ToLower(prop) {
	case "", "password":
		prop = "Content"
	case "username":
		prop = "UserName"
	case "address":
		prop = "Address"
	default:
		prop = strings.ToUpper(prop[:1]) + strings.ToLower(prop[1:])
	}
	v, ok := obj[prop]
	if !ok {
		return "", fmt.Errorf("Property %s not found in object, available properties: Username, Password and Address", s(metadata, "object_property"))
	}
	return fmt.Sprint(v), nil
}

// conjur is AWX's conjur plugin: authenticate with the API key, then read
// the variable, at /authn and /secrets or, for Conjur Cloud, under /api.
func conjur(ctx context.Context, inputs, metadata map[string]any) (string, error) {
	c, err := httpClient(s(inputs, "cacert"), "", "", true)
	if err != nil {
		return "", err
	}
	account, user := pyQuote(s(inputs, "account")), pyQuote(s(inputs, "username"))
	var token []byte
	for _, prefix := range []string{"", "api/"} {
		u, err := urljoin(s(inputs, "url"), prefix+"authn/"+account+"/"+user+"/authenticate")
		if err != nil {
			return "", err
		}
		req, _ := http.NewRequestWithContext(ctx, "POST", u, strings.NewReader(s(inputs, "api_key")))
		req.Header.Set("Content-Type", "text/plain")
		req.Header.Set("Accept-Encoding", "base64")
		if token, err = do(c, req); err == nil {
			break
		}
		if prefix != "" {
			return "", err
		}
	}
	tok := strings.TrimSpace(string(token))
	if dec, err := base64.StdEncoding.DecodeString(tok); err != nil || base64.StdEncoding.EncodeToString(dec) != tok {
		tok = base64.StdEncoding.EncodeToString([]byte(tok))
	}
	version := ""
	if v := s(metadata, "secret_version"); v != "" {
		version = "?version=" + v
	}
	var lastErr error
	for _, prefix := range []string{"", "api/"} {
		u, err := urljoin(s(inputs, "url"), prefix+"secrets/"+account+"/variable/"+pyQuote(s(metadata, "secret_path")))
		if err != nil {
			return "", err
		}
		req, _ := http.NewRequestWithContext(ctx, "GET", u+version, nil)
		req.Header.Set("Authorization", fmt.Sprintf(`Token token="%s"`, tok))
		body, err := do(c, req)
		if err == nil {
			return string(body), nil
		}
		lastErr = err
	}
	return "", lastErr
}

// centrify is AWX's centrify_vault plugin: an OAuth2 client credentials
// token, the account's id from a Redrock query, then a password checkout.
func centrify(ctx context.Context, inputs, metadata map[string]any) (string, error) {
	c, err := httpClient("", "", "", true)
	if err != nil {
		return "", err
	}
	base := s(inputs, "url")
	tokenURL, err := urljoin(base, "/oauth2/token/"+orDefault(s(inputs, "oauth_application_id"), "awx"))
	if err != nil {
		return "", err
	}
	form := url.Values{"grant_type": {"client_credentials"}, "scope": {orDefault(s(inputs, "oauth_scope"), "awx")}}
	req, _ := http.NewRequestWithContext(ctx, "POST", tokenURL, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetBasicAuth(s(inputs, "client_id"), s(inputs, "client_password"))
	body, err := do(c, req)
	if err != nil {
		return "", err
	}
	var tok struct {
		AccessToken string `json:"access_token"`
	}
	if json.Unmarshal(body, &tok) != nil || tok.AccessToken == "" {
		return "", errors.New("OAuth request to tenant was unsuccessful")
	}
	call := func(path string, payload any, out any) error {
		u, err := urljoin(base, path)
		if err != nil {
			return err
		}
		b, _ := json.Marshal(payload)
		req, _ := http.NewRequestWithContext(ctx, "POST", u, bytes.NewReader(b))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+tok.AccessToken)
		req.Header.Set("X-CENTRIFY-NATIVE-CLIENT", "true")
		body, err := do(c, req)
		if err != nil {
			return err
		}
		return json.Unmarshal(body, out)
	}
	// Quotes in names are doubled so they cannot end the query's strings.
	esc := func(v string) string { return strings.ReplaceAll(v, "'", "''") }
	var q struct {
		Result struct {
			Results []struct {
				Row struct {
					ID any `json:"ID"`
				} `json:"Row"`
			} `json:"Results"`
		} `json:"Result"`
	}
	script := fmt.Sprintf("Select ID from VaultAccount where  Name='%s' and User='%s'", esc(s(metadata, "system-name")), esc(s(metadata, "account-name")))
	if err := call("/Redrock/query", map[string]any{"Script": script}, &q); err != nil {
		return "", err
	}
	if len(q.Result.Results) == 0 || q.Result.Results[0].Row.ID == nil {
		return "", errors.New("Error Detected!! Check the Inputs")
	}
	var pw struct {
		Result struct {
			Password *string `json:"Password"`
		} `json:"Result"`
	}
	if err := call("/ServerManage/CheckoutPassword", map[string]any{"ID": q.Result.Results[0].Row.ID}, &pw); err != nil {
		return "", err
	}
	if pw.Result.Password == nil {
		return "", errors.New("Password Not Found")
	}
	return *pw.Result.Password, nil
}

// thycoticDSV is AWX's dsv plugin (python-dsv-sdk): a client credentials
// token, then the secret's data field, base64-decoded when asked.
func thycoticDSV(ctx context.Context, inputs, metadata map[string]any) (string, error) {
	c, err := httpClient("", "", "", true)
	if err != nil {
		return "", err
	}
	base := fmt.Sprintf(DSVURLTemplate, s(inputs, "tenant"), strings.Trim(orDefault(s(inputs, "tld"), "com"), "."))
	b, _ := json.Marshal(map[string]string{"client_id": s(inputs, "client_id"), "client_secret": s(inputs, "client_secret"), "grant_type": "client_credentials"})
	req, _ := http.NewRequestWithContext(ctx, "POST", strings.TrimRight(base, "/")+"/v1/token", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	body, err := do(c, req)
	if err != nil {
		return "", err
	}
	var grant struct {
		AccessToken string `json:"accessToken"`
	}
	if json.Unmarshal(body, &grant) != nil || grant.AccessToken == "" {
		return "", errors.New("DSV returned no access token")
	}
	req, _ = http.NewRequestWithContext(ctx, "GET", strings.TrimRight(base, "/")+"/v1/secrets/"+strings.TrimLeft(s(metadata, "path"), "/"), nil)
	req.Header.Set("Authorization", "Bearer "+grant.AccessToken)
	if body, err = do(c, req); err != nil {
		return "", err
	}
	var secret struct {
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal(body, &secret); err != nil {
		return "", fmt.Errorf("invalid DSV response: %w", err)
	}
	v, ok := secret.Data[s(metadata, "secret_field")]
	if !ok {
		return "", fmt.Errorf("field %q not found in the secret", s(metadata, "secret_field"))
	}
	if s(metadata, "secret_decoding") == "Decode Base64" {
		dec, err := base64.StdEncoding.DecodeString(fmt.Sprint(v))
		if err != nil {
			return "", fmt.Errorf("secret field is not base64: %w", err)
		}
		return string(dec), nil
	}
	return fmt.Sprint(v), nil
}

// thycoticTSS is AWX's tss plugin (python-tss-sdk 1.2): a password grant
// token (with the domain when set), then the secret's field by slug, a file
// field's content fetched separately.
func thycoticTSS(ctx context.Context, inputs, metadata map[string]any) (string, error) {
	c, err := httpClient("", "", "", true)
	if err != nil {
		return "", err
	}
	base := strings.TrimRight(s(inputs, "server_url"), "/")
	form := url.Values{"username": {s(inputs, "username")}, "password": {s(inputs, "password")}, "grant_type": {"password"}}
	if d := s(inputs, "domain"); d != "" {
		form.Set("domain", d)
	}
	req, _ := http.NewRequestWithContext(ctx, "POST", base+"/oauth2/token", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	body, err := do(c, req)
	if err != nil {
		return "", err
	}
	var grant struct {
		AccessToken string `json:"access_token"`
	}
	if json.Unmarshal(body, &grant) != nil || grant.AccessToken == "" {
		return "", errors.New("Secret Server returned no access token")
	}
	get := func(path string) ([]byte, error) {
		req, _ := http.NewRequestWithContext(ctx, "GET", base+"/api/v1/"+path, nil)
		req.Header.Set("Authorization", "Bearer "+grant.AccessToken)
		return do(c, req)
	}
	id := url.PathEscape(s(metadata, "secret_id"))
	if body, err = get("secrets/" + id); err != nil {
		return "", err
	}
	var secret struct {
		Items []struct {
			Slug             string `json:"slug"`
			ItemValue        any    `json:"itemValue"`
			FileAttachmentID any    `json:"fileAttachmentId"`
		} `json:"items"`
	}
	if err := json.Unmarshal(body, &secret); err != nil {
		return "", fmt.Errorf("invalid Secret Server response: %w", err)
	}
	field := s(metadata, "secret_field")
	for _, it := range secret.Items {
		if it.Slug != field {
			continue
		}
		if it.FileAttachmentID != nil {
			content, err := get("secrets/" + id + "/fields/" + url.PathEscape(it.Slug))
			return string(content), err
		}
		if it.ItemValue == nil {
			return "", nil
		}
		return fmt.Sprint(it.ItemValue), nil
	}
	return "", fmt.Errorf("field %q not found in the secret", field)
}

// awsSecret is AWX's aws_secretsmanager plugin: GetSecretValue with the
// credential's keys, returning the string or binary secret.
func awsSecret(ctx context.Context, inputs, metadata map[string]any) (string, error) {
	opts := secretsmanager.Options{
		Region: s(metadata, "region_name"),
		Credentials: aws.NewCredentialsCache(aws.CredentialsProviderFunc(func(context.Context) (aws.Credentials, error) {
			return aws.Credentials{AccessKeyID: s(inputs, "aws_access_key"), SecretAccessKey: s(inputs, "aws_secret_key"), Source: "awxnext credential"}, nil
		})),
	}
	if AWSEndpoint != "" {
		opts.BaseEndpoint = aws.String(AWSEndpoint)
	}
	out, err := secretsmanager.New(opts).GetSecretValue(ctx, &secretsmanager.GetSecretValueInput{SecretId: aws.String(s(metadata, "secret_name"))})
	if err != nil {
		return "", err
	}
	if out.SecretString != nil {
		return *out.SecretString, nil
	}
	return string(out.SecretBinary), nil
}
