// Package galaxy reads collections from a Galaxy server's v3 API as
// ansible-galaxy does: galaxy.ansible.com, Red Hat's Automation Hub, or
// another galaxy_ng. The collection hub's remotes mirror collections with it.
package galaxy

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	maxJSONBytes = 32 << 20
	// ansible-galaxy's page size.
	pageSize = 100
	maxPages = 1000
	// Red Hat's SSO client for offline tokens, ansible-galaxy's default.
	ssoClientID     = "cloud-services"
	apiTimeout      = 2 * time.Minute
	downloadTimeout = 30 * time.Minute
)

// ErrNotFound is a collection or version the server doesn't have.
var ErrNotFound = errors.New("not found on the server")

// Client reads one Galaxy server.
type Client struct {
	HTTP *http.Client
	// URL is the server as a Galaxy credential gives it, such as
	// https://galaxy.ansible.com/ or
	// https://console.redhat.com/api/automation-hub/content/published/.
	URL string
	// AuthURL is the SSO token endpoint that exchanges Token, an offline
	// token, for access tokens, as Red Hat's Automation Hub needs.
	AuthURL   string
	Token     string
	UserAgent string

	root, v3 *url.URL
	access   string
}

// Version is a collection version's detail.
type Version struct {
	Namespace struct {
		Name string `json:"name"`
	} `json:"namespace"`
	Collection struct {
		Name string `json:"name"`
	} `json:"collection"`
	Version     string `json:"version"`
	DownloadURL string `json:"download_url"`
	Artifact    struct {
		Filename string `json:"filename"`
		SHA256   string `json:"sha256"`
		Size     int64  `json:"size"`
	} `json:"artifact"`
	Metadata struct {
		Dependencies map[string]string `json:"dependencies"`
	} `json:"metadata"`
	Signatures []Signature `json:"signatures"`
}

// Signature is a detached signature of a version's MANIFEST.json.
type Signature struct {
	Signature         string `json:"signature"`
	PubkeyFingerprint string `json:"pubkey_fingerprint"`
	SigningService    string `json:"signing_service"`
}

// Root is the server's API root, found by Connect.
func (c *Client) Root() string {
	if c.root == nil {
		return ""
	}
	return c.root.Redacted()
}

// Connect finds the server's API root and its v3 API as ansible-galaxy
// does: the URL itself, else the URL with api/ appended, and gets an access
// token when the server needs one.
func (c *Client) Connect(ctx context.Context) error {
	u, err := url.Parse(c.URL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return errors.New("the Galaxy credential's URL is not an http:// or https:// URL")
	}
	u.User, u.RawQuery, u.Fragment = nil, "", ""
	if !strings.HasSuffix(u.Path, "/") {
		u.Path += "/"
	}
	candidates := []*url.URL{u}
	switch {
	case strings.EqualFold(u.Host, "galaxy.ansible.com") && u.Path == "/":
		candidates = []*url.URL{u.JoinPath("api/")}
	case !strings.HasSuffix(u.Path, "/api/"):
		candidates = append(candidates, u.JoinPath("api/"))
	}
	if c.AuthURL != "" && c.Token != "" {
		if err := c.refresh(ctx); err != nil {
			return err
		}
	}
	var first error
	for _, cand := range candidates {
		cand.Path = strings.TrimSuffix(cand.Path, "/") + "/"
		var root struct {
			Available map[string]string `json:"available_versions"`
		}
		err := c.getJSON(ctx, cand, &root)
		if err == nil && root.Available["v3"] == "" {
			err = fmt.Errorf("%s has no v3 API in its available_versions", cand.Redacted())
		}
		if err != nil {
			if first == nil {
				first = err
			}
			continue
		}
		c.root = cand
		c.v3 = cand.JoinPath(strings.Trim(root.Available["v3"], "/") + "/")
		return nil
	}
	return fmt.Errorf("no Galaxy API at %s: %w", u.Redacted(), first)
}

// Versions lists a collection's versions, following the pages.
func (c *Client) Versions(ctx context.Context, namespace, name string) ([]string, error) {
	next := c.v3.JoinPath("collections", namespace, name, "versions/")
	next.RawQuery = fmt.Sprintf("limit=%d", pageSize)
	var out []string
	seen := map[string]bool{}
	for page := 0; next != nil; page++ {
		if page == maxPages || seen[next.String()] {
			return nil, fmt.Errorf("the server's version list of %s.%s doesn't end", namespace, name)
		}
		seen[next.String()] = true
		var body struct {
			Links struct {
				Next string `json:"next"`
			} `json:"links"`
			Data []struct {
				Version string `json:"version"`
			} `json:"data"`
			// pulp_ansible's name for data.
			Results []struct {
				Version string `json:"version"`
			} `json:"results"`
		}
		if err := c.getJSON(ctx, next, &body); err != nil {
			return nil, err
		}
		for _, d := range append(body.Data, body.Results...) {
			out = append(out, d.Version)
		}
		next = nil
		if body.Links.Next != "" {
			u, err := c.sameServer(body.Links.Next)
			if err != nil {
				return nil, fmt.Errorf("the next page of %s.%s's versions: %w", namespace, name, err)
			}
			next = u
		}
	}
	return out, nil
}

// Version reads a version's detail: its artifact, dependencies and
// signatures.
func (c *Client) Version(ctx context.Context, namespace, name, version string) (*Version, error) {
	var v Version
	if err := c.getJSON(ctx, c.v3.JoinPath("collections", namespace, name, "versions", version+"/"), &v); err != nil {
		return nil, err
	}
	if v.Namespace.Name != namespace || v.Collection.Name != name || v.Version != version {
		return nil, fmt.Errorf("the server answered with %s.%s %s for %s.%s %s", v.Namespace.Name, v.Collection.Name, v.Version, namespace, name, version)
	}
	return &v, nil
}

// Download reads a version's artifact, at most limit bytes, and checks it
// against the SHA-256 its detail gives. The token goes only to the
// server's own host: a download URL may point at object storage.
func (c *Client) Download(ctx context.Context, v *Version, limit int64) ([]byte, error) {
	if v.Artifact.SHA256 == "" {
		return nil, errors.New("the server gives no SHA-256 of the artifact")
	}
	u, err := c.root.Parse(v.DownloadURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, fmt.Errorf("the server's download URL %q is not an http:// or https:// URL", v.DownloadURL)
	}
	ctx, cancel := context.WithTimeout(ctx, downloadTimeout)
	defer cancel()
	res, err := c.do(ctx, u, strings.EqualFold(u.Host, c.root.Host))
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, statusError(u, res)
	}
	data, err := io.ReadAll(io.LimitReader(res.Body, limit+1))
	if err != nil {
		return nil, fmt.Errorf("downloading %s: %w", u.Redacted(), err)
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("the artifact is larger than %d bytes, the most the hub takes (HUB_MAX_UPLOAD_SIZE)", limit)
	}
	sum := sha256.Sum256(data)
	if got := hex.EncodeToString(sum[:]); !strings.EqualFold(got, v.Artifact.SHA256) {
		return nil, fmt.Errorf("the artifact's SHA-256 is %s, not %s as the server says", got, strings.ToLower(v.Artifact.SHA256))
	}
	return data, nil
}

// sameServer resolves a link the server gave against its API root, and
// refuses one to another host.
func (c *Client) sameServer(link string) (*url.URL, error) {
	u, err := c.root.Parse(link)
	if err != nil {
		return nil, err
	}
	if !strings.EqualFold(u.Host, c.root.Host) || u.Scheme != c.root.Scheme {
		return nil, fmt.Errorf("the link %s leaves the server", u.Redacted())
	}
	return u, nil
}

func (c *Client) getJSON(ctx context.Context, u *url.URL, out any) error {
	ctx, cancel := context.WithTimeout(ctx, apiTimeout)
	defer cancel()
	res, err := c.do(ctx, u, true)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode == http.StatusNotFound {
		return fmt.Errorf("%s: %w", u.Redacted(), ErrNotFound)
	}
	if res.StatusCode != http.StatusOK {
		return statusError(u, res)
	}
	body, err := io.ReadAll(io.LimitReader(res.Body, maxJSONBytes+1))
	if err != nil {
		return fmt.Errorf("reading %s: %w", u.Redacted(), err)
	}
	if len(body) > maxJSONBytes {
		return fmt.Errorf("%s answered with more than %d bytes", u.Redacted(), maxJSONBytes)
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("%s didn't answer with Galaxy's JSON: %v", u.Redacted(), err)
	}
	return nil
}

// do sends a GET, with the token when auth, and once more with a new
// access token when the server refuses one that expired.
func (c *Client) do(ctx context.Context, u *url.URL, auth bool) (*http.Response, error) {
	send := func() (*http.Response, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Accept", "application/json")
		if c.UserAgent != "" {
			req.Header.Set("User-Agent", c.UserAgent)
		}
		if auth {
			switch {
			case c.access != "":
				req.Header.Set("Authorization", "Bearer "+c.access)
			case c.Token != "":
				req.Header.Set("Authorization", "Token "+c.Token)
			}
		}
		res, err := c.HTTP.Do(req)
		if err != nil {
			return nil, fmt.Errorf("GET %s: %w", u.Redacted(), unwrapURLError(err))
		}
		return res, nil
	}
	res, err := send()
	if err != nil || res.StatusCode != http.StatusUnauthorized || !auth || c.access == "" {
		return res, err
	}
	res.Body.Close()
	if err := c.refresh(ctx); err != nil {
		return nil, err
	}
	return send()
}

// refresh exchanges the offline token for an access token at the SSO
// server, as ansible-galaxy's KeycloakToken does.
func (c *Client) refresh(ctx context.Context) error {
	u, err := url.Parse(c.AuthURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return errors.New("the Galaxy credential's auth URL is not an http:// or https:// URL")
	}
	form := url.Values{"grant_type": {"refresh_token"}, "client_id": {ssoClientID}, "refresh_token": {c.Token}}
	ctx, cancel := context.WithTimeout(ctx, apiTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if c.UserAgent != "" {
		req.Header.Set("User-Agent", c.UserAgent)
	}
	res, err := c.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("getting an access token from %s: %w", u.Redacted(), unwrapURLError(err))
	}
	defer res.Body.Close()
	var body struct {
		AccessToken string `json:"access_token"`
		Error       string `json:"error"`
	}
	raw, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	json.Unmarshal(raw, &body)
	if res.StatusCode != http.StatusOK || body.AccessToken == "" {
		msg := fmt.Sprintf("the SSO server %s refused the offline token: %s", u.Redacted(), res.Status)
		if body.Error != "" {
			msg += " (" + body.Error + ")"
		}
		return errors.New(msg)
	}
	c.access = body.AccessToken
	return nil
}

// statusError describes a refused request with galaxy_ng's error title.
func statusError(u *url.URL, res *http.Response) error {
	var body struct {
		Errors []struct {
			Title  string `json:"title"`
			Detail string `json:"detail"`
		} `json:"errors"`
		Detail string `json:"detail"`
	}
	raw, _ := io.ReadAll(io.LimitReader(res.Body, 64<<10))
	json.Unmarshal(raw, &body)
	msg := fmt.Sprintf("GET %s: %s", u.Redacted(), res.Status)
	switch {
	case len(body.Errors) > 0 && body.Errors[0].Detail != "":
		msg += ": " + body.Errors[0].Detail
	case len(body.Errors) > 0 && body.Errors[0].Title != "":
		msg += ": " + body.Errors[0].Title
	case body.Detail != "":
		msg += ": " + body.Detail
	}
	return errors.New(msg)
}

// unwrapURLError drops the method and URL url.Error repeats.
func unwrapURLError(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		return ue.Err
	}
	return err
}
