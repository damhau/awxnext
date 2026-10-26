// Package azureclient is a minimal Azure Key Vault client with Entra ID
// authentication (client secret, workload identity federation or managed
// identity). It is shared by the master-key provider and credential lookups.
package azureclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
)

const (
	vaultScope = "https://vault.azure.net/.default"
	apiVersion = "7.4"
)

type Config struct {
	TenantID     string
	ClientID     string
	ClientSecret string
	// FederatedTokenFile enables workload identity federation (AKS sets AZURE_FEDERATED_TOKEN_FILE).
	FederatedTokenFile string
	// ManagedIdentity uses the instance metadata service when no secret or federated token is set.
	ManagedIdentity bool
	// AuthorityHost defaults to https://login.microsoftonline.com.
	AuthorityHost string
	// IMDSEndpoint overrides the managed identity endpoint (tests).
	IMDSEndpoint string
}

type Client struct {
	cfg    Config
	http   *http.Client
	mu     sync.Mutex
	token  string
	expiry time.Time
}

func New(cfg Config) *Client {
	if cfg.AuthorityHost == "" {
		cfg.AuthorityHost = "https://login.microsoftonline.com"
	}
	if cfg.FederatedTokenFile == "" && cfg.ClientSecret == "" {
		cfg.FederatedTokenFile = os.Getenv("AZURE_FEDERATED_TOKEN_FILE")
	}
	return &Client{cfg: cfg, http: &http.Client{Timeout: 30 * time.Second, Transport: &http.Transport{Proxy: http.ProxyFromEnvironment}}}
}

// Error is an Azure API error.
type Error struct {
	Status  int
	Code    string
	Message string
}

func (e *Error) Error() string {
	return fmt.Sprintf("azure: HTTP %d %s: %s", e.Status, e.Code, e.Message)
}

func (c *Client) accessToken(ctx context.Context) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.token != "" && time.Until(c.expiry) > 2*time.Minute {
		return c.token, nil
	}
	var req *http.Request
	var err error
	switch {
	case c.cfg.ClientSecret != "" || c.cfg.FederatedTokenFile != "":
		if c.cfg.TenantID == "" || c.cfg.ClientID == "" {
			return "", errors.New("azure: tenant id and client id are required")
		}
		form := url.Values{
			"grant_type": {"client_credentials"},
			"client_id":  {c.cfg.ClientID},
			"scope":      {vaultScope},
		}
		if c.cfg.ClientSecret != "" {
			form.Set("client_secret", c.cfg.ClientSecret)
		} else {
			assertion, err := os.ReadFile(c.cfg.FederatedTokenFile)
			if err != nil {
				return "", fmt.Errorf("azure: read federated token: %w", err)
			}
			form.Set("client_assertion_type", "urn:ietf:params:oauth:client-assertion-type:jwt-bearer")
			form.Set("client_assertion", strings.TrimSpace(string(assertion)))
		}
		req, err = http.NewRequestWithContext(ctx, "POST", strings.TrimRight(c.cfg.AuthorityHost, "/")+"/"+url.PathEscape(c.cfg.TenantID)+"/oauth2/v2.0/token", strings.NewReader(form.Encode()))
		if err != nil {
			return "", err
		}
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	case c.cfg.ManagedIdentity:
		endpoint := c.cfg.IMDSEndpoint
		if endpoint == "" {
			endpoint = "http://169.254.169.254/metadata/identity/oauth2/token"
		}
		q := url.Values{"api-version": {"2018-02-01"}, "resource": {"https://vault.azure.net"}}
		if c.cfg.ClientID != "" {
			q.Set("client_id", c.cfg.ClientID)
		}
		req, err = http.NewRequestWithContext(ctx, "GET", endpoint+"?"+q.Encode(), nil)
		if err != nil {
			return "", err
		}
		req.Header.Set("Metadata", "true")
	default:
		return "", errors.New("azure: no authentication method configured")
	}
	res, err := c.http.Do(req)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	var out struct {
		AccessToken string          `json:"access_token"`
		ExpiresIn   json.RawMessage `json:"expires_in"`
		Error       string          `json:"error"`
		Description string          `json:"error_description"`
	}
	json.NewDecoder(res.Body).Decode(&out)
	if res.StatusCode >= 300 || out.AccessToken == "" {
		return "", &Error{Status: res.StatusCode, Code: out.Error, Message: out.Description}
	}
	secs := 3600
	var n json.Number
	if json.Unmarshal(out.ExpiresIn, &n) == nil {
		if v, err := n.Int64(); err == nil {
			secs = int(v)
		}
	} else {
		var s string
		if json.Unmarshal(out.ExpiresIn, &s) == nil {
			fmt.Sscan(s, &secs)
		}
	}
	c.token, c.expiry = out.AccessToken, time.Now().Add(time.Duration(secs)*time.Second)
	return c.token, nil
}

// Do calls a Key Vault URL (absolute) with the given body and decodes the JSON response.
func (c *Client) Do(ctx context.Context, method, u string, body any) (map[string]any, error) {
	tok, err := c.accessToken(ctx)
	if err != nil {
		return nil, err
	}
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		rd = bytes.NewReader(b)
	}
	if !strings.Contains(u, "api-version=") {
		sep := "?"
		if strings.Contains(u, "?") {
			sep = "&"
		}
		u += sep + "api-version=" + apiVersion
	}
	req, err := http.NewRequestWithContext(ctx, method, u, rd)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/json")
	res, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(res.Body, 16<<20))
	var out map[string]any
	json.Unmarshal(data, &out)
	if res.StatusCode >= 300 {
		e := &Error{Status: res.StatusCode}
		if ae, ok := out["error"].(map[string]any); ok {
			e.Code, _ = ae["code"].(string)
			e.Message, _ = ae["message"].(string)
		}
		return nil, e
	}
	return out, nil
}

// GetSecret reads a secret value from a vault (version may be empty for latest).
func (c *Client) GetSecret(ctx context.Context, vaultURL, name, version string) (string, error) {
	u := strings.TrimRight(vaultURL, "/") + "/secrets/" + url.PathEscape(name)
	if version != "" {
		u += "/" + url.PathEscape(version)
	}
	out, err := c.Do(ctx, "GET", u, nil)
	if err != nil {
		return "", err
	}
	v, _ := out["value"].(string)
	return v, nil
}
