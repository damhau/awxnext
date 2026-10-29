// Package credplugins resolves credential input sources from external secret
// stores: HashiCorp Vault (KV v1/v2, SSH signing) and Azure Key Vault.
package credplugins

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/damhau/awxnext/internal/azureclient"
	"github.com/damhau/awxnext/internal/vaultclient"
)

// Lookup fetches one value from the external store described by a credential
// of an external type (namespace), using its decrypted inputs and the
// input source metadata.
func Lookup(ctx context.Context, namespace string, inputs, metadata map[string]any) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	switch namespace {
	case "hashivault_kv":
		return vaultKV(ctx, inputs, metadata)
	case "hashivault_ssh":
		return vaultSSH(ctx, inputs, metadata)
	case "azure_kv":
		return azureKV(ctx, inputs, metadata)
	}
	return "", fmt.Errorf("unsupported credential plugin %q", namespace)
}

func s(m map[string]any, k string) string {
	v, _ := m[k].(string)
	return strings.TrimSpace(v)
}

func vaultClient(inputs, metadata map[string]any) (*vaultclient.Client, error) {
	authPath := s(metadata, "auth_path")
	if authPath == "" {
		authPath = s(inputs, "default_auth_path")
	}
	cfg := vaultclient.Config{
		Addr:      s(inputs, "url"),
		Namespace: s(inputs, "namespace"),
		Token:     s(inputs, "token"),
		CACert:    s(inputs, "cacert"),
		AuthMount: strings.Trim(authPath, "/"),
	}
	// AWX's order: token, username and password, AppRole, Kubernetes, TLS
	// certificate. Auth paths left at AWX's default "approle" use the method's own.
	byDefault := func(mount string) {
		if cfg.AuthMount == "" || cfg.AuthMount == "approle" {
			cfg.AuthMount = mount
		}
	}
	cert, key := s(inputs, "client_cert_public"), s(inputs, "client_cert_private")
	switch {
	case cfg.Token != "":
		cfg.AuthMount = ""
	case s(inputs, "username") != "" && s(inputs, "password") != "":
		cfg.Username, cfg.Password = s(inputs, "username"), s(inputs, "password")
		byDefault("userpass")
	case s(inputs, "role_id") != "" && s(inputs, "secret_id") != "":
		cfg.RoleID, cfg.SecretID = s(inputs, "role_id"), s(inputs, "secret_id")
	case s(inputs, "kubernetes_role") != "":
		cfg.K8sRole = s(inputs, "kubernetes_role")
		byDefault("kubernetes")
	case cert != "" && key != "":
		cfg.CertRole = s(inputs, "client_cert_role")
		byDefault("cert")
	default:
		return nil, errors.New("Token, Username/Password, AppRole, Kubernetes, or TLS authentication parameters must be set")
	}
	// As AWX, the certificate goes with the requests whenever both halves are set.
	if cert != "" && key != "" {
		cfg.ClientCert, cfg.ClientKey = cert, key
	}
	return vaultclient.New(cfg)
}

// splitBackend separates the KV mount from the secret path, following AWX:
// without secret_backend the first path segment is the mount.
func splitBackend(metadata map[string]any) (string, string, error) {
	path := strings.Trim(s(metadata, "secret_path"), "/")
	backend := strings.Trim(s(metadata, "secret_backend"), "/")
	if backend == "" {
		i := strings.Index(path, "/")
		if i < 0 {
			return "", "", errors.New("secret_path must include the secret backend, e.g. kv/my/secret")
		}
		backend, path = path[:i], path[i+1:]
	}
	if path == "" {
		return "", "", errors.New("secret_path is required")
	}
	return backend, path, nil
}

func vaultKV(ctx context.Context, inputs, metadata map[string]any) (string, error) {
	c, err := vaultClient(inputs, metadata)
	if err != nil {
		return "", err
	}
	backend, path, err := splitBackend(metadata)
	if err != nil {
		return "", err
	}
	key := s(metadata, "secret_key")
	var data map[string]any
	if s(inputs, "api_version") == "v2" {
		p := backend + "/data/" + path
		if v := s(metadata, "secret_version"); v != "" {
			p += "?version=" + url.QueryEscape(v)
		}
		out, err := c.Do(ctx, "GET", p, nil)
		if err != nil {
			return "", err
		}
		outer, _ := out["data"].(map[string]any)
		data, _ = outer["data"].(map[string]any)
	} else {
		out, err := c.Do(ctx, "GET", backend+"/"+path, nil)
		if err != nil {
			return "", err
		}
		data, _ = out["data"].(map[string]any)
	}
	if data == nil {
		return "", fmt.Errorf("no secret found at %s/%s", backend, path)
	}
	v, ok := data[key]
	if !ok {
		return "", fmt.Errorf("key %q not found in secret %s/%s", key, backend, path)
	}
	if str, ok := v.(string); ok {
		return str, nil
	}
	return fmt.Sprint(v), nil
}

func vaultSSH(ctx context.Context, inputs, metadata map[string]any) (string, error) {
	c, err := vaultClient(inputs, metadata)
	if err != nil {
		return "", err
	}
	mount := strings.Trim(s(metadata, "secret_path"), "/")
	role := s(metadata, "role")
	body := map[string]any{"public_key": s(metadata, "public_key")}
	if p := s(metadata, "valid_principals"); p != "" {
		body["valid_principals"] = p
	}
	out, err := c.Do(ctx, "POST", mount+"/sign/"+url.PathEscape(role), body)
	if err != nil {
		return "", err
	}
	data, _ := out["data"].(map[string]any)
	signed, _ := data["signed_key"].(string)
	if signed == "" {
		return "", errors.New("vault returned no signed key")
	}
	return signed, nil
}

var azureAuthorities = map[string]string{
	"":                  "https://login.microsoftonline.com",
	"AzureCloud":        "https://login.microsoftonline.com",
	"AzureChinaCloud":   "https://login.chinacloudapi.cn",
	"AzureUSGovernment": "https://login.microsoftonline.us",
}

func azureKV(ctx context.Context, inputs, metadata map[string]any) (string, error) {
	vaultURL := s(inputs, "url")
	if vaultURL == "" {
		return "", errors.New("vault URL is required")
	}
	if !strings.HasPrefix(vaultURL, "https://") && !strings.HasPrefix(vaultURL, "http://") {
		vaultURL = "https://" + vaultURL
	}
	authority, ok := azureAuthorities[s(inputs, "cloud_name")]
	if !ok {
		return "", fmt.Errorf("unknown cloud environment %q", s(inputs, "cloud_name"))
	}
	if a := s(inputs, "authority_host"); a != "" {
		authority = a
	}
	cfg := azureclient.Config{
		TenantID:      s(inputs, "tenant"),
		ClientID:      s(inputs, "client"),
		ClientSecret:  s(inputs, "secret"),
		AuthorityHost: authority,
	}
	if cfg.ClientSecret == "" {
		// No secret: workload identity federation when configured, else managed identity.
		cfg.ManagedIdentity = true
	}
	c := azureclient.New(cfg)
	return c.GetSecret(ctx, vaultURL, s(metadata, "secret_field"), s(metadata, "secret_version"))
}
