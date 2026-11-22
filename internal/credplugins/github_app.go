package credplugins

import (
	"context"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
)

// GitHubAPIURL is the public GitHub's API, the default of github_api_url.
const GitHubAPIURL = "https://api.github.com"

const githubTokenDocs = "https://docs.github.com/rest/apps/apps#create-an-installation-access-token-for-an-app"

// githubAppToken returns a new installation access token of a GitHub App,
// as ansible/awx-plugins' github_app_lookup: a JWT signed with the app's key
// buys a token limited to the installation's repositories, valid one hour.
func githubAppToken(ctx context.Context, p Policy, inputs map[string]any) (string, error) {
	api := strings.TrimRight(s(inputs, "github_api_url"), "/")
	if api == "" {
		api = GitHubAPIURL
	}
	appID, installID := s(inputs, "app_or_client_id"), s(inputs, "install_id")
	if appID == "" {
		return "", errors.New("the GitHub App ID or Client ID is required")
	}
	if installID == "" || strings.Trim(installID, "0123456789") != "" {
		return "", fmt.Errorf("the GitHub App Installation ID must be a number, not %q", installID)
	}
	key, err := githubAppKey(s(inputs, "private_rsa_key"))
	if err != nil {
		return "", err
	}
	u, err := url.Parse(api)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return "", fmt.Errorf("the GitHub API endpoint URL must be an http(s) URL, not %q", api)
	}
	if err := p.checkURL(api, ""); err != nil {
		return "", err
	}
	signed, err := githubAppJWT(key, appID, time.Now())
	if err != nil {
		return "", err
	}
	c, err := httpClient(p, "", "", "", true)
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, "POST", api+"/app/installations/"+installID+"/access_tokens", nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+signed)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	res, err := c.Do(req)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	var out struct {
		Token   string `json:"token"`
		Message string `json:"message"`
	}
	_ = json.Unmarshal(body, &out)
	where := fmt.Sprintf("%s (app %s, installation %s)", api, appID, installID)
	switch {
	case res.StatusCode == http.StatusCreated && out.Token != "":
		return out.Token, nil
	case res.StatusCode == http.StatusNotFound:
		return "", fmt.Errorf("GitHub at %s has no such installation: is the app installed? %s: %s", where, githubTokenDocs, githubMessage(out.Message, body))
	case res.StatusCode == http.StatusUnauthorized:
		return "", fmt.Errorf("GitHub at %s refused the app's token: are the App ID or Client ID and the private key the app's? %s: %s", where, githubTokenDocs, githubMessage(out.Message, body))
	case res.StatusCode >= 200 && res.StatusCode < 300:
		return "", fmt.Errorf("GitHub at %s returned no token (HTTP %d)", where, res.StatusCode)
	}
	return "", fmt.Errorf("GitHub at %s answered HTTP %d: %s", where, res.StatusCode, githubMessage(out.Message, body))
}

func githubMessage(msg string, body []byte) string {
	if msg == "" {
		msg = strings.TrimSpace(string(body))
	}
	if len(msg) > 300 {
		msg = msg[:300] + "…"
	}
	return msg
}

func githubAppKey(pemText string) (*rsa.PrivateKey, error) {
	invalid := errors.New("the RSA private key must be the PEM file GitHub generated for the app")
	block, _ := pem.Decode([]byte(strings.TrimSpace(pemText)))
	if block == nil {
		return nil, invalid
	}
	if k, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return k, nil
	}
	k, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, invalid
	}
	rk, ok := k.(*rsa.PrivateKey)
	if !ok {
		return nil, invalid
	}
	return rk, nil
}

// githubAppJWT is the app's own token. GitHub refuses one that expires more
// than 10 minutes ahead, so it allows for a clock a minute off either way.
func githubAppJWT(key *rsa.PrivateKey, appID string, now time.Time) (string, error) {
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: key}, (&jose.SignerOptions{}).WithType("JWT"))
	if err != nil {
		return "", err
	}
	return jwt.Signed(signer).Claims(jwt.Claims{
		Issuer:   appID,
		IssuedAt: jwt.NewNumericDate(now.Add(-time.Minute)),
		Expiry:   jwt.NewNumericDate(now.Add(5 * time.Minute)),
	}).Serialize()
}
