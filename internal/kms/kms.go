// Package kms provides master-key (KEK) implementations for envelope encryption.
package kms

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"

	"github.com/damhau/awxnext/internal/azureclient"
	"github.com/damhau/awxnext/internal/crypto"
	"github.com/damhau/awxnext/internal/vaultclient"
)

// FromEnv builds the master key from environment variables with the given
// prefix (normally "AWXNEXT_"; "AWXNEXT_OLD_" is used when re-wrapping).
//
//	<P>KMS                       local (default) | vault-transit | azure-keyvault
//	<P>SECRET_KEY                local: secret the master key is derived from
//	<P>KMS_VAULT_ADDR, _TOKEN, _NAMESPACE, _MOUNT (transit), _KEY,
//	<P>KMS_VAULT_ROLE_ID, _SECRET_ID (AppRole), _K8S_ROLE (Kubernetes auth), _CACERT
//	<P>KMS_AZURE_VAULT_URL, _KEY, _TENANT_ID, _CLIENT_ID, _CLIENT_SECRET, _MANAGED_IDENTITY
func FromEnv(prefix, fallbackSecret string) (crypto.KEK, error) {
	get := func(k string) string { return os.Getenv(prefix + k) }
	switch kind := get("KMS"); kind {
	case "", "local":
		secret := get("SECRET_KEY")
		if secret == "" {
			secret = fallbackSecret
		}
		return crypto.NewLocalKEK(secret)
	case "vault-transit":
		c, err := vaultclient.New(vaultclient.Config{
			Addr:      get("KMS_VAULT_ADDR"),
			Namespace: get("KMS_VAULT_NAMESPACE"),
			Token:     get("KMS_VAULT_TOKEN"),
			RoleID:    get("KMS_VAULT_ROLE_ID"),
			SecretID:  get("KMS_VAULT_SECRET_ID"),
			K8sRole:   get("KMS_VAULT_K8S_ROLE"),
			CACert:    get("KMS_VAULT_CACERT"),
		})
		if err != nil {
			return nil, err
		}
		mount := get("KMS_VAULT_MOUNT")
		if mount == "" {
			mount = "transit"
		}
		key := get("KMS_VAULT_KEY")
		if key == "" {
			return nil, errors.New(prefix + "KMS_VAULT_KEY is required")
		}
		return &VaultTransit{client: c, mount: mount, key: key, addr: get("KMS_VAULT_ADDR")}, nil
	case "azure-keyvault":
		vaultURL := strings.TrimRight(get("KMS_AZURE_VAULT_URL"), "/")
		key := get("KMS_AZURE_KEY")
		if vaultURL == "" || key == "" {
			return nil, errors.New(prefix + "KMS_AZURE_VAULT_URL and " + prefix + "KMS_AZURE_KEY are required")
		}
		c := azureclient.New(azureclient.Config{
			TenantID:        get("KMS_AZURE_TENANT_ID"),
			ClientID:        get("KMS_AZURE_CLIENT_ID"),
			ClientSecret:    get("KMS_AZURE_CLIENT_SECRET"),
			ManagedIdentity: get("KMS_AZURE_MANAGED_IDENTITY") != "",
		})
		return &AzureKeyVault{client: c, vaultURL: vaultURL, key: key}, nil
	default:
		return nil, fmt.Errorf("unknown %sKMS %q", prefix, kind)
	}
}

// VaultTransit wraps data keys with a Vault Transit key.
type VaultTransit struct {
	client *vaultclient.Client
	addr   string
	mount  string
	key    string
}

func NewVaultTransit(c *vaultclient.Client, addr, mount, key string) *VaultTransit {
	return &VaultTransit{client: c, addr: addr, mount: mount, key: key}
}

func (v *VaultTransit) ID() string {
	host := v.addr
	if u, err := url.Parse(v.addr); err == nil && u.Host != "" {
		host = u.Host
	}
	return "vault-transit:" + host + "/" + v.mount + "/" + v.key
}

func (v *VaultTransit) Wrap(ctx context.Context, dek []byte) (string, error) {
	out, err := v.client.Do(ctx, "POST", v.mount+"/encrypt/"+url.PathEscape(v.key), map[string]any{
		"plaintext": base64.StdEncoding.EncodeToString(dek),
	})
	if err != nil {
		return "", err
	}
	data, _ := out["data"].(map[string]any)
	ct, _ := data["ciphertext"].(string)
	if ct == "" {
		return "", errors.New("vault transit: no ciphertext returned")
	}
	return ct, nil
}

func (v *VaultTransit) Unwrap(ctx context.Context, wrapped string) ([]byte, error) {
	out, err := v.client.Do(ctx, "POST", v.mount+"/decrypt/"+url.PathEscape(v.key), map[string]any{"ciphertext": wrapped})
	if err != nil {
		return nil, err
	}
	data, _ := out["data"].(map[string]any)
	pt, _ := data["plaintext"].(string)
	return base64.StdEncoding.DecodeString(pt)
}

// AzureKeyVault wraps data keys with an RSA key in Azure Key Vault (RSA-OAEP-256).
type AzureKeyVault struct {
	client   *azureclient.Client
	vaultURL string
	key      string
}

func NewAzureKeyVault(c *azureclient.Client, vaultURL, key string) *AzureKeyVault {
	return &AzureKeyVault{client: c, vaultURL: strings.TrimRight(vaultURL, "/"), key: key}
}

func (a *AzureKeyVault) ID() string {
	host := a.vaultURL
	if u, err := url.Parse(a.vaultURL); err == nil && u.Host != "" {
		host = u.Host
	}
	return "azure-keyvault:" + host + "/" + a.key
}

// Wrap stores "<key id with version>|<wrapped value>" so unwrap uses the exact key version.
func (a *AzureKeyVault) Wrap(ctx context.Context, dek []byte) (string, error) {
	out, err := a.client.Do(ctx, "POST", a.vaultURL+"/keys/"+url.PathEscape(a.key)+"/wrapkey", map[string]any{
		"alg":   "RSA-OAEP-256",
		"value": base64.RawURLEncoding.EncodeToString(dek),
	})
	if err != nil {
		return "", err
	}
	kid, _ := out["kid"].(string)
	val, _ := out["value"].(string)
	if kid == "" || val == "" {
		return "", errors.New("azure key vault: wrapkey returned no value")
	}
	return kid + "|" + val, nil
}

func (a *AzureKeyVault) Unwrap(ctx context.Context, wrapped string) ([]byte, error) {
	kid, val, ok := strings.Cut(wrapped, "|")
	if !ok {
		return nil, errors.New("azure key vault: malformed wrapped key")
	}
	// Only ever send the access token to the configured vault and key.
	if !strings.HasPrefix(kid, a.vaultURL+"/keys/"+a.key+"/") {
		return nil, fmt.Errorf("azure key vault: wrapped key references unexpected key %q", kid)
	}
	out, err := a.client.Do(ctx, "POST", kid+"/unwrapkey", map[string]any{"alg": "RSA-OAEP-256", "value": val})
	if err != nil {
		return nil, err
	}
	v, _ := out["value"].(string)
	return base64.RawURLEncoding.DecodeString(v)
}
