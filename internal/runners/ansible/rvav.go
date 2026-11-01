package ansible

// The rvav source, awxnext's own (AWX has none): awxnext reads the VMs rvav
// manages from rvav's API itself and imports them, with no execution
// environment, collection or project. rvav serves them in the format of
// `ansible-inventory --list`, which ParseInventoryJSON reads.

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/damhau/awxnext/internal/jobs"
	"github.com/damhau/awxnext/internal/netguard"
	"github.com/damhau/awxnext/internal/resources"
)

// RvavLocalAddressesSetting lists the loopback and link-local addresses rvav
// sources may read from. Empty: none, nor the cloud metadata service.
const RvavLocalAddressesSetting = "INVENTORY_SOURCE_ALLOWED_LOCAL_ADDRESSES"

// rvavInventoryPath is where rvav serves its inventory.
const rvavInventoryPath = "/api/v1/ansible/inventory"

// rvavMaxBytes is the most an rvav source reads: about 100,000 hosts.
const rvavMaxBytes = 64 << 20

func runRvavSource(ctx context.Context, rc *jobs.RunContext) jobs.Result {
	row, app := rc.Job, rc.M.App
	invID := row.Int("inventory_id")
	if invID == 0 {
		return fail("The inventory for this update no longer exists.")
	}
	cid := row.Int("credential_id")
	if cid == 0 {
		return fail("The source has no rvav credential. Give it one with rvav's address and an inventory token: rvav makes both in Administration, AWX, Inventory token.")
	}
	cred, err := resources.ResolveCredential(ctx, app, cid)
	if err != nil {
		return fail(err.Error())
	}
	if cred.Namespace != resources.RvavNamespace {
		return fail(fmt.Sprintf("The source's credential %s is not an rvav credential.", cred.Name))
	}
	in := func(k string) string { v, _ := cred.Inputs[k].(string); return strings.TrimSpace(v) }
	u, err := url.Parse(strings.TrimRight(in("url"), "/") + rvavInventoryPath)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return fail(fmt.Sprintf("The rvav URL of the credential %s, %q, is not an http or https address.", cred.Name, in("url")))
	}
	verify, set := cred.Inputs["verify_ssl"].(bool)
	var addrs []string
	app.Settings.Decode(ctx, RvavLocalAddressesSetting, &addrs)
	allowed, _ := netguard.ParseAllowed(addrs)
	client, err := rvavClient(netguard.Guard{Allowed: allowed, Setting: RvavLocalAddressesSetting}, in("ssl_ca_cert"), verify || !set)
	if err != nil {
		return fail(fmt.Sprintf("The credential %s: %v", cred.Name, err))
	}

	timeout := time.Duration(row.Int("timeout")) * time.Second
	if timeout == 0 {
		timeout = time.Duration(app.Settings.Int(ctx, "DEFAULT_INVENTORY_UPDATE_TIMEOUT")) * time.Second
	}
	fctx := ctx
	if timeout > 0 {
		var cancel context.CancelFunc
		fctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	update := map[string]any{"job_args": "GET " + u.Redacted()}
	rc.Out.Line("Reading the inventory from " + u.Redacted())
	b, err := rvavFetch(fctx, client, u, in("token"))
	switch {
	case ctx.Err() != nil:
		return jobs.Result{Status: "canceled", Update: update}
	case errors.Is(fctx.Err(), context.DeadlineExceeded):
		return jobs.Result{Status: "failed", Explanation: "Job terminated due to timeout", Update: update}
	case err != nil:
		rc.Out.Line(err.Error())
		return jobs.Result{Status: "failed", Explanation: err.Error(), Update: update}
	}
	data, err := ParseInventoryJSON(b)
	if err != nil {
		rc.Out.Line("rvav's answer is not an inventory: " + err.Error())
		return jobs.Result{Status: "failed", Explanation: "rvav's answer is not an inventory: " + err.Error(), Update: update}
	}
	return importUpdate(ctx, rc, invID, data, update)
}

// rvavClient checks rvav's certificate against the system's CAs and cacert,
// unless verify is false, goes through HTTPS_PROXY as other outbound
// requests do, follows no redirects, and connects to no loopback, link-local
// or cloud metadata address that g does not allow: they are awxnext's own
// host, and anyone who may create a credential chooses its URL.
func rvavClient(g netguard.Guard, cacert string, verify bool) (*http.Client, error) {
	cfg := &tls.Config{InsecureSkipVerify: !verify} //nolint:gosec // the credential's Verify SSL input
	if cacert != "" {
		pool, err := x509.SystemCertPool()
		if err != nil {
			pool = x509.NewCertPool()
		}
		if !pool.AppendCertsFromPEM([]byte(cacert)) {
			return nil, errors.New("its Certificate Authority data is not a PEM certificate")
		}
		cfg.RootCAs = pool
	}
	return &http.Client{
		Transport:     g.Transport(&http.Transport{TLSClientConfig: cfg, Proxy: http.ProxyFromEnvironment}, nil),
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}, nil
}

// rvavFetch reads rvav's inventory with the token, saying what to do when
// rvav refuses it.
func rvavFetch(ctx context.Context, c *http.Client, u *url.URL, token string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")
	res, err := c.Do(req)
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
		return nil, fmt.Errorf("GET %s: %w", u.Redacted(), err)
	}
	defer res.Body.Close()
	body, err := io.ReadAll(io.LimitReader(res.Body, rvavMaxBytes+1))
	if err != nil {
		return nil, fmt.Errorf("GET %s: %w", u.Redacted(), err)
	}
	switch {
	case res.StatusCode == http.StatusOK:
	case res.StatusCode == http.StatusUnauthorized:
		return nil, errors.New("rvav refused the token (HTTP 401): it may have expired or been revoked. Make a new one in rvav, Administration, AWX, Inventory token.")
	case res.StatusCode == http.StatusForbidden:
		return nil, errors.New("rvav refused the token (HTTP 403): its user may not read rvav's inventory. Make one in rvav, Administration, AWX, Inventory token.")
	case res.StatusCode >= 300 && res.StatusCode < 400:
		return nil, fmt.Errorf("GET %s: HTTP %d to %s, which is not followed: give the credential the address rvav is served at", u.Redacted(), res.StatusCode, res.Header.Get("Location"))
	default:
		// Not the answer itself: the URL may be any host awxnext reaches.
		return nil, fmt.Errorf("GET %s: HTTP %d", u.Redacted(), res.StatusCode)
	}
	if len(body) > rvavMaxBytes {
		return nil, fmt.Errorf("rvav's inventory is larger than %d MiB, the most awxnext reads", rvavMaxBytes>>20)
	}
	return body, nil
}
