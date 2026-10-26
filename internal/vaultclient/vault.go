// Package vaultclient is a minimal HashiCorp Vault HTTP client supporting
// token, AppRole and Kubernetes authentication. It is shared by the master-key
// provider (Transit) and credential lookups (KV, SSH signing).
package vaultclient

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

type Config struct {
	Addr      string
	Namespace string
	// Token auth.
	Token string
	// AppRole auth.
	RoleID   string
	SecretID string
	// Kubernetes auth: Role plus a service account JWT (read from JWTPath when JWT is empty).
	K8sRole string
	JWT     string
	JWTPath string
	// AuthMount overrides the auth mount path (default "approle" / "kubernetes").
	AuthMount     string
	CACert        string
	TLSSkipVerify bool
	Timeout       time.Duration
}

type Client struct {
	cfg   Config
	http  *http.Client
	mu    sync.Mutex
	token string
}

func New(cfg Config) (*Client, error) {
	if cfg.Addr == "" {
		return nil, errors.New("vault address is required")
	}
	tlsCfg := &tls.Config{InsecureSkipVerify: cfg.TLSSkipVerify}
	if cfg.CACert != "" {
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM([]byte(cfg.CACert)) {
			return nil, errors.New("invalid vault CA certificate")
		}
		tlsCfg.RootCAs = pool
	}
	timeout := cfg.Timeout
	if timeout == 0 {
		timeout = 30 * time.Second
	}
	return &Client{
		cfg:   cfg,
		token: cfg.Token,
		http:  &http.Client{Timeout: timeout, Transport: &http.Transport{TLSClientConfig: tlsCfg, Proxy: http.ProxyFromEnvironment}},
	}, nil
}

// Error is a Vault API error.
type Error struct {
	Status int
	Errors []string
}

func (e *Error) Error() string {
	return fmt.Sprintf("vault: HTTP %d: %s", e.Status, strings.Join(e.Errors, "; "))
}

func (c *Client) raw(ctx context.Context, method, path, token string, body any) (map[string]any, error) {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(c.cfg.Addr, "/")+"/v1/"+strings.TrimLeft(path, "/"), rd)
	if err != nil {
		return nil, err
	}
	if token != "" {
		req.Header.Set("X-Vault-Token", token)
	}
	if c.cfg.Namespace != "" {
		req.Header.Set("X-Vault-Namespace", c.cfg.Namespace)
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(res.Body, 16<<20))
	var out map[string]any
	if len(data) > 0 {
		if err := json.Unmarshal(data, &out); err != nil && res.StatusCode < 300 {
			return nil, fmt.Errorf("vault: invalid response: %w", err)
		}
	}
	if res.StatusCode >= 300 {
		e := &Error{Status: res.StatusCode}
		if errs, ok := out["errors"].([]any); ok {
			for _, x := range errs {
				e.Errors = append(e.Errors, fmt.Sprint(x))
			}
		}
		return nil, e
	}
	return out, nil
}

func (c *Client) login(ctx context.Context) (string, error) {
	var path string
	var body map[string]any
	switch {
	case c.cfg.RoleID != "":
		mount := c.cfg.AuthMount
		if mount == "" {
			mount = "approle"
		}
		path = "auth/" + mount + "/login"
		body = map[string]any{"role_id": c.cfg.RoleID, "secret_id": c.cfg.SecretID}
	case c.cfg.K8sRole != "":
		jwt := c.cfg.JWT
		if jwt == "" {
			p := c.cfg.JWTPath
			if p == "" {
				p = "/var/run/secrets/kubernetes.io/serviceaccount/token"
			}
			b, err := os.ReadFile(p)
			if err != nil {
				return "", fmt.Errorf("vault kubernetes auth: %w", err)
			}
			jwt = strings.TrimSpace(string(b))
		}
		mount := c.cfg.AuthMount
		if mount == "" {
			mount = "kubernetes"
		}
		path = "auth/" + mount + "/login"
		body = map[string]any{"role": c.cfg.K8sRole, "jwt": jwt}
	default:
		if c.cfg.Token == "" {
			return "", errors.New("vault: no authentication method configured")
		}
		return c.cfg.Token, nil
	}
	out, err := c.raw(ctx, "POST", path, "", body)
	if err != nil {
		return "", err
	}
	auth, _ := out["auth"].(map[string]any)
	tok, _ := auth["client_token"].(string)
	if tok == "" {
		return "", errors.New("vault: login returned no token")
	}
	return tok, nil
}

// Do performs an authenticated request, logging in (again) when needed.
func (c *Client) Do(ctx context.Context, method, path string, body any) (map[string]any, error) {
	c.mu.Lock()
	tok := c.token
	c.mu.Unlock()
	if tok == "" {
		t, err := c.login(ctx)
		if err != nil {
			return nil, err
		}
		c.mu.Lock()
		c.token, tok = t, t
		c.mu.Unlock()
	}
	out, err := c.raw(ctx, method, path, tok, body)
	var ve *Error
	if errors.As(err, &ve) && ve.Status == 403 && c.cfg.Token == "" {
		// Token expired: log in again once.
		t, lerr := c.login(ctx)
		if lerr != nil {
			return nil, lerr
		}
		c.mu.Lock()
		c.token = t
		c.mu.Unlock()
		return c.raw(ctx, method, path, t, body)
	}
	return out, err
}
