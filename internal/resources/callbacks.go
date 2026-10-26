package resources

import (
	"context"
	"crypto/subtle"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/damhau/awxnext/internal/api"
	"github.com/damhau/awxnext/internal/rbac"
	"github.com/damhau/awxnext/internal/vars"
	"github.com/go-chi/chi/v5"
)

// callbackLookup resolves names and addresses (replaced in tests).
var callbackLookup = struct {
	Addr func(ctx context.Context, addr string) ([]string, error)
	Host func(ctx context.Context, host string) ([]string, error)
}{net.DefaultResolver.LookupAddr, net.DefaultResolver.LookupHost}

// remoteHosts lists the names and addresses identifying the caller, from
// REMOTE_HOST_HEADERS (headers only from PROXY_IP_ALLOWED_LIST proxies).
func remoteHosts(c *Ctx, r *http.Request) map[string]bool {
	var headers, proxies []string
	c.App.Settings.Decode(c, "REMOTE_HOST_HEADERS", &headers)
	c.App.Settings.Decode(c, "PROXY_IP_ALLOWED_LIST", &proxies)
	peer, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		peer = r.RemoteAddr
	}
	trusted := len(proxies) == 0 || contains(proxies, peer)
	out := map[string]bool{}
	for _, h := range headers {
		var raw string
		switch {
		case h == "REMOTE_ADDR":
			raw = peer
		case strings.HasPrefix(h, "HTTP_") && trusted:
			raw = r.Header.Get(strings.ReplaceAll(strings.TrimPrefix(h, "HTTP_"), "_", "-"))
		}
		for _, v := range strings.Split(raw, ",") {
			if v = strings.TrimSpace(v); v != "" {
				out[v] = true
			}
		}
	}
	ctx, cancel := context.WithTimeout(c, 3*time.Second)
	defer cancel()
	for h := range out {
		if net.ParseIP(h) == nil {
			continue
		}
		if names, err := callbackLookup.Addr(ctx, h); err == nil {
			for _, n := range names {
				out[strings.TrimSuffix(n, ".")] = true
			}
		}
	}
	for h := range out {
		if strings.HasSuffix(h, ".arpa") {
			delete(out, h)
		}
	}
	return out
}

// matchingHosts finds inventory hosts whose name or ansible_host matches the caller.
func matchingHosts(c *Ctx, inv int64, remote map[string]bool) ([]string, error) {
	if len(remote) == 0 || inv == 0 {
		return nil, nil
	}
	rows, err := c.DB().Query(c, `SELECT h.name, h.variables FROM hosts h WHERE h.inventory_id = $1
		OR h.id IN (SELECT host_id FROM smart_inventory_hosts($1)) ORDER BY h.name`, inv)
	if err != nil {
		return nil, err
	}
	byName := map[string][]string{}
	for rows.Next() {
		var name, v string
		rows.Scan(&name, &v)
		effective := name
		if hv, err := vars.Parse(v); err == nil {
			if ah, ok := hv["ansible_host"].(string); ok && ah != "" {
				effective = ah
			}
		}
		byName[effective] = append(byName[effective], name)
	}
	rows.Close()
	matches := map[string]bool{}
	for eff, names := range byName {
		if remote[eff] {
			for _, n := range names {
				matches[n] = true
			}
		}
	}
	if len(matches) != 1 {
		// Resolve host names to addresses (bounded concurrency and time).
		ctx, cancel := context.WithTimeout(c, 10*time.Second)
		defer cancel()
		var mu sync.Mutex
		sem := make(chan struct{}, 16)
		var wg sync.WaitGroup
		for eff, names := range byName {
			if net.ParseIP(eff) != nil {
				continue
			}
			eff, names := eff, names
			wg.Add(1)
			sem <- struct{}{}
			go func() {
				defer wg.Done()
				defer func() { <-sem }()
				addrs, err := callbackLookup.Host(ctx, eff)
				if err != nil {
					return
				}
				for _, a := range addrs {
					if remote[a] {
						mu.Lock()
						for _, n := range names {
							matches[n] = true
						}
						mu.Unlock()
						return
					}
				}
			}()
		}
		wg.Wait()
	}
	var out []string
	for n := range matches {
		out = append(out, n)
	}
	return out, nil
}

func installCallbackRoutes(app *api.App) {
	app.HandlePublic("GET", "/api/v2/job_templates/{id}/callback/", handleCallbackGet)
	app.HandlePublic("POST", "/api/v2/job_templates/{id}/callback/", handleCallbackPost)
}

func callbackTemplate(c *Ctx, r *http.Request) (Obj, error) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		return nil, api.ErrNotFound
	}
	return api.Get(c, api.Lookup("job_templates"), id)
}

func handleCallbackGet(c *Ctx, w http.ResponseWriter, r *http.Request) {
	if c.User.Anonymous() {
		api.WriteError(w, api.ErrUnauthorized)
		return
	}
	jt, err := callbackTemplate(c, r)
	if err != nil {
		api.WriteError(w, err)
		return
	}
	if ok, err := api.HasPerm(c, api.Lookup("job_templates"), jt, "change"); err != nil || !ok {
		api.WriteError(w, api.ErrForbidden)
		return
	}
	inv, _ := api.ID(jt["inventory"])
	hosts, err := matchingHosts(c, inv, remoteHosts(c, r))
	if err != nil {
		api.WriteError(w, err)
		return
	}
	api.WriteJSON(w, 200, Obj{"host_config_key": jt["host_config_key"], "matching_hosts": nonNil(hosts)})
}

func handleCallbackPost(c *Ctx, w http.ResponseWriter, r *http.Request) {
	sys := &api.Ctx{Context: c.Context, App: c.App, User: rbac.System(), Req: r, Extra: map[string]any{}}
	var key string
	var extra map[string]any
	if strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		body, err := api.ReadBody(r)
		if err != nil {
			api.WriteError(w, err)
			return
		}
		key = str(body["host_config_key"])
		switch v := body["extra_vars"].(type) {
		case map[string]any:
			extra = v
		case string:
			extra, _ = vars.Parse(v)
		}
	} else {
		r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
		if err := r.ParseForm(); err == nil {
			key = r.PostForm.Get("host_config_key")
		}
		if key == "" {
			if q, err := url.ParseQuery(r.URL.RawQuery); err == nil {
				key = q.Get("host_config_key")
			}
		}
	}
	jt, err := callbackTemplate(sys, r)
	if err != nil {
		api.WriteError(w, api.ErrForbidden)
		return
	}
	cfgKey := str(jt["host_config_key"])
	if key == "" || cfgKey == "" || subtle.ConstantTimeCompare([]byte(key), []byte(cfgKey)) != 1 {
		api.WriteError(w, api.ErrForbidden)
		return
	}
	inv, _ := api.ID(jt["inventory"])
	remote := remoteHosts(sys, r)
	hosts, err := matchingHosts(sys, inv, remote)
	if err != nil {
		api.WriteError(w, err)
		return
	}
	if len(hosts) != 1 {
		// Sync update-on-launch sources, then try again.
		ids, _ := idList(sys, "SELECT id FROM unified_job_templates WHERE type = 'inventory_source' AND inventory_id = $1 AND update_on_launch", inv)
		var updates []int64
		for _, sid := range ids {
			src, err := api.Get(sys, api.Lookup("inventory_sources"), sid)
			if err != nil {
				continue
			}
			if uid, err := LaunchInventoryUpdate(sys, src, "callback"); err == nil {
				updates = append(updates, uid)
			}
		}
		deadline := time.Now().Add(2 * time.Minute)
		for len(updates) > 0 && time.Now().Before(deadline) {
			var running int
			sys.DB().QueryRow(sys, "SELECT count(*) FROM unified_jobs WHERE id = ANY($1) AND status IN ('pending', 'waiting', 'running')", updates).Scan(&running)
			if running == 0 {
				break
			}
			select {
			case <-r.Context().Done():
				return
			case <-time.After(500 * time.Millisecond):
			}
		}
		if hosts, err = matchingHosts(sys, inv, remote); err != nil {
			api.WriteError(w, err)
			return
		}
	}
	switch {
	case len(hosts) == 0:
		api.WriteJSON(w, 400, Obj{"msg": "No matching host could be found!"})
		return
	case len(hosts) > 1:
		api.WriteJSON(w, 400, Obj{"msg": "Multiple hosts matched the request!"})
		return
	}
	host := hosts[0]
	var pending bool
	sys.DB().QueryRow(sys, `SELECT EXISTS (SELECT 1 FROM unified_jobs WHERE type = 'job' AND unified_job_template_id = $1
		AND "limit" = $2 AND status IN ('pending', 'waiting', 'running'))`, jt["id"], host).Scan(&pending)
	if pending {
		api.WriteJSON(w, 400, Obj{"msg": "Host callback job already pending."})
		return
	}
	body := Obj{}
	if extra != nil && boolv(jt["ask_variables_on_launch"]) {
		safe := map[string]any{}
		for k, v := range extra {
			if !strings.HasPrefix(k, "ansible_") {
				safe[k] = v
			}
		}
		body["extra_vars"] = safe
	}
	jobID, _, err := LaunchJobTemplate(sys, jt, body, "callback", &LaunchOverrides{Trusted: Obj{"limit": host},
		ExtraJob: map[string]any{"job_slice_number": 0}})
	if err != nil {
		api.WriteJSON(w, 400, Obj{"msg": "Cannot start automatically, user input required!", "detail": fmt.Sprint(launchFailure(err))})
		return
	}
	w.Header().Set("Location", api.Lookup("jobs").URL(jobID))
	api.WriteJSON(w, 201, Obj{"job": jobID})
}
