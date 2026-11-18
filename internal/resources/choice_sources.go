package resources

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/expr-lang/expr"
	"github.com/expr-lang/expr/vm"
	"github.com/jackc/pgx/v5"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/clientcredentials"

	"github.com/damhau/awxnext/internal/api"
	"github.com/damhau/awxnext/internal/jobs"
	"github.com/damhau/awxnext/internal/netguard"
	"github.com/damhau/awxnext/internal/rbac"
	"github.com/damhau/awxnext/internal/tmpl"
)

// A choice source is an organization's list for survey questions
// (choices_from: {type: source, source: N}): from a JSON API that the
// server calls (http), or from what a job template's run publishes with
// set_stats (job).
const (
	sourceHTTP = "http"
	sourceJob  = "job"

	// ChoiceSourceHostsSetting lists the hosts http sources may call.
	ChoiceSourceHostsSetting = "CHOICE_SOURCE_HOSTS"
	// ChoiceSourceLocalAddressesSetting lists the loopback and link-local
	// addresses they may reach.
	ChoiceSourceLocalAddressesSetting = "CHOICE_SOURCE_ALLOWED_LOCAL_ADDRESSES"

	launchChoiceSource = "choice_source"
	maxSourceReply     = 1 << 20
	sourceTimeout      = 5 * time.Second
	// staleSourceLists is how long an http source's list stays to fall back
	// on when its API can't be reached.
	staleSourceLists = 24 * time.Hour
)

var sourceCredentialTypes = map[string]bool{"http_api_token": true, "http_api_basic": true, "oauth2_client_credentials": true}

func registerChoiceSources() {
	api.Register(&Resource{
		Name:         "choice_sources",
		Type:         "choice_source",
		Table:        "choice_sources",
		Verbose:      "Choice Source",
		Ordering:     "name",
		ContentType:  "choicesource",
		OrgColumn:    "organization_id",
		Versioned:    true,
		SearchFields: []string{"url"},
		Fields: []*Field{
			{Name: "organization", Type: api.TFK, Ref: "organizations", Required: true},
			{Name: "kind", Type: api.TChoice, Default: sourceHTTP, Choices: labelled(sourceHTTP, "JSON API", sourceJob, "Job template"),
				Help: "http: a JSON API that awxnext calls. job: what a job template's run publishes with set_stats."},
			{Name: "url", Type: api.TString, Default: "", Help: "http: the API's URL. Its host must be listed in " + ChoiceSourceHostsSetting + "."},
			{Name: "parameters", Type: api.TJSON, Default: Obj{}, Validate: validateSourceParameters,
				Help: "http: query parameters added to the URL, by name. Values can use the earlier answers and the user: {{ answers.site }}, {{ user.username }}."},
			{Name: "credential", Type: api.TFK, Ref: "credentials", Nullable: true,
				Help: "http: how awxnext authenticates to the API: an HTTP API Token, HTTP API Basic Authentication or OAuth 2.0 Client Credentials credential."},
			{Name: "job_template", Type: api.TFK, Ref: "job_templates", Nullable: true,
				Help: "job: the template whose run publishes the list with set_stats."},
			{Name: "items", Type: api.TString, Default: "", Validate: validateSourceExpr,
				Help: "An expression over data (the API's reply, or the job's artifacts) that gives the list, such as data.results. Empty: data itself."},
			{Name: "value", Type: api.TString, Default: "", Validate: validateSourceExpr,
				Help: "An expression over item that gives the answer, such as item.name. Empty: the item itself."},
			{Name: "label", Type: api.TString, Default: "", Validate: validateSourceExpr,
				Help: "An expression over item and value that gives what people see, such as item.name + \" (\" + value + \")\". Empty: the value."},
			{Name: "filter", Type: api.TString, Default: "", Validate: validateSourceExpr,
				Help: "An expression over item, value, label, answers and user: only the items for which it's true are listed. Empty: all."},
			{Name: "cache_seconds", Type: api.TInt, Default: 300, Validate: between(0, 86400),
				Help: "http: how long a list is kept before the API is called again, for the same parameters."},
			{Name: "refresh_minutes", Type: api.TInt, Default: 0, Validate: between(0, 525600),
				Help: "job: minutes between refreshes. 0: only by hand."},
			{Name: "next_refresh", Type: api.TDateTime, ReadOnly: true, Nullable: true},
			{Name: "last_job", Type: api.TFK, Ref: "jobs", ReadOnly: true, Nullable: true, Help: "job: the last refresh's job."},
			{Name: "last_refresh", Type: api.TDateTime, ReadOnly: true, Nullable: true, Help: "job: when the list was last published."},
			{Name: "last_refresh_job", Type: api.TFK, Ref: "jobs", ReadOnly: true, Nullable: true, Help: "job: the job that published the list."},
			{Name: "last_error", Type: api.TText, ReadOnly: true, Help: "job: why the last refresh didn't change the list."},
		},
		BeforeSave:   saveChoiceSource,
		AfterUpdate:  choiceSourceUpdated,
		BeforeDelete: refuseSourceInUse,
		Actions: []*api.Action{
			{Name: "test", Methods: []string{"POST"}, Perm: "change", Handler: testChoiceSourceView},
			{Name: "refresh", Methods: []string{"POST"}, Perm: "change", Handler: refreshChoiceSourceView},
		},
	})
}

// refuseSourceInUse keeps a source that surveys list choices from: they
// would refuse every launch.
func refuseSourceInUse(c *Ctx, obj Obj) error {
	id, _ := api.ID(obj["id"])
	ref, _ := json.Marshal([]any{map[string]any{"choices_from": map[string]any{"type": "source", "source": id}}})
	rows, err := c.DB().Query(c, `SELECT name FROM unified_job_templates WHERE survey_spec -> 'spec' @> $1::jsonb ORDER BY name LIMIT 6`, string(ref))
	if err != nil {
		return err
	}
	defer rows.Close()
	var names []string
	for rows.Next() {
		var n string
		if rows.Scan(&n) == nil {
			names = append(names, n)
		}
	}
	if len(names) == 0 {
		return rows.Err()
	}
	if len(names) > 5 {
		names = append(names[:5], "…")
	}
	return api.Detail(409, fmt.Sprintf("The surveys of %s list choices from this source: take it out of them first.", strings.Join(names, ", ")))
}

func validateSourceExpr(c *Ctx, v any, _ Obj) error {
	if s := str(v); s != "" {
		if err := checkExpr(s); err != nil {
			return fmt.Errorf("Not a valid expression: %v", err)
		}
	}
	return nil
}

func validateSourceParameters(c *Ctx, v any, _ Obj) error {
	m, ok := v.(map[string]any)
	if !ok && v != nil {
		return errors.New("Expected an object of query parameters by name.")
	}
	for k, p := range m {
		s, ok := p.(string)
		if !ok {
			return fmt.Errorf("%s: expected a string.", k)
		}
		if err := tmpl.Validate(s); err != nil {
			return fmt.Errorf("%s: %v", k, err)
		}
	}
	return nil
}

var sourceHTTPFields = []string{"url", "parameters", "credential", "cache_seconds"}

func saveChoiceSource(c *Ctx, old, data Obj) error {
	merged := Obj{}
	for k, v := range old {
		merged[k] = v
	}
	for k, v := range data {
		merged[k] = v
	}
	changed := func(k string) bool {
		v, ok := data[k]
		return ok && (old == nil || !sameValue(old[k], v))
	}
	fe := api.FieldErrors{}
	kind := str(merged["kind"])
	if kind == "" {
		kind = sourceHTTP
	}
	switch kind {
	case sourceHTTP:
		if changed("kind") {
			data["job_template"], data["refresh_minutes"], data["next_refresh"] = nil, int64(0), nil
		} else if id, ok := api.ID(data["job_template"]); ok && id != 0 {
			fe.Add("job_template", "Only for job sources.")
		}
		if n, _ := api.ID(merged["refresh_minutes"]); n != 0 && !changed("kind") {
			fe.Add("refresh_minutes", "Only for job sources: an http source calls its API when a list is needed.")
		}
		raw := str(merged["url"])
		if raw == "" {
			fe.Add("url", "This field is required for an http source.")
		} else if changed("url") || old == nil {
			if err := checkSourceURL(c, raw); err != nil {
				fe.Add("url", err.Error())
			}
		}
		if changed("credential") {
			if err := checkSourceCredential(c, data["credential"]); err != nil {
				return err
			}
		}
	case sourceJob:
		if changed("kind") {
			data["url"], data["parameters"], data["credential"] = "", Obj{}, nil
		} else {
			for _, k := range []string{"url", "credential"} {
				if v, ok := data[k]; ok && v != nil && v != "" {
					fe.Add(k, "Only for http sources.")
				}
			}
			if m, ok := data["parameters"].(map[string]any); ok && len(m) > 0 {
				fe.Add("parameters", "Only for http sources.")
			}
		}
		if _, ok := api.ID(merged["job_template"]); !ok {
			fe.Add("job_template", "This field is required for a job source.")
		} else if changed("job_template") {
			if err := api.RequirePermID(c, "job_template", "job_templates", data["job_template"], "execute"); err != nil {
				return err
			}
		}
		if old == nil || changed("refresh_minutes") || changed("kind") {
			data["next_refresh"] = nil
			if n, _ := api.ID(merged["refresh_minutes"]); n > 0 {
				next := time.Now().Add(time.Duration(n) * time.Minute)
				if old == nil || changed("kind") {
					next = time.Now()
				}
				data["next_refresh"] = next
			}
		}
		if old != nil && !changed("kind") && (changed("items") || changed("value") || changed("label")) {
			if job, ok := api.ID(old["last_refresh_job"]); ok {
				if _, err := jobSourceEntries(c, sourceFrom(merged), job); err != nil {
					fe.Add("items", fmt.Sprintf("The list job %d published doesn't read with these expressions: %v", job, err))
				}
			}
		}
	default:
		fe.Add("kind", "Must be http or job.")
	}
	return fe.Err()
}

// checkSourceURL refuses a URL that isn't http(s) or whose host isn't one
// http sources may call.
func checkSourceURL(c *Ctx, raw string) error {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return errors.New("Enter an http or https URL.")
	}
	return sourceHosts(c).check(u)
}

func checkSourceCredential(c *Ctx, v any) error {
	id, ok := api.ID(v)
	if !ok {
		return nil
	}
	if err := api.RequirePermID(c, "credential", "credentials", id, "use"); err != nil {
		return err
	}
	var ns *string
	if err := c.DB().QueryRow(c, `SELECT ct.namespace FROM credentials cr JOIN credential_types ct ON ct.id = cr.credential_type_id WHERE cr.id = $1`, id).Scan(&ns); err != nil {
		return err
	}
	if ns == nil || !sourceCredentialTypes[*ns] {
		return api.FieldError("credential", "Must be an HTTP API Token, HTTP API Basic Authentication or OAuth 2.0 Client Credentials credential.")
	}
	return nil
}

// choiceSourceUpdated drops lists that a change makes stale: all of an http
// source's, and a job source's when it reads the published list otherwise.
func choiceSourceUpdated(c *Ctx, old, obj Obj) error {
	if str(old["kind"]) != str(obj["kind"]) || str(obj["kind"]) == sourceHTTP {
		for _, k := range []string{"kind", "url", "parameters", "credential", "items", "value", "label"} {
			if !sameValue(old[k], obj[k]) {
				_, err := c.DB().Exec(c, "DELETE FROM choice_source_lists WHERE source_id = $1", obj["id"])
				return err
			}
		}
		return nil
	}
	if sameValue(old["items"], obj["items"]) && sameValue(old["value"], obj["value"]) && sameValue(old["label"], obj["label"]) {
		return nil
	}
	job, ok := api.ID(obj["last_refresh_job"])
	if !ok {
		return nil
	}
	entries, err := jobSourceEntries(c, sourceFrom(obj), job)
	if err != nil {
		return err
	}
	return storeSourceList(c, obj["id"], "", entries)
}

// choiceSource is what reading a source needs.
type choiceSource struct {
	id                       int64
	name, kind, url          string
	parameters               map[string]any
	credential               int64
	items, value, label, flt string
	cacheSeconds             int64
	lastError                string
}

func sourceFrom(o Obj) *choiceSource {
	s := &choiceSource{name: str(o["name"]), kind: str(o["kind"]), url: str(o["url"]),
		items: str(o["items"]), value: str(o["value"]), label: str(o["label"]), flt: str(o["filter"]), lastError: str(o["last_error"])}
	s.id, _ = api.ID(o["id"])
	s.credential, _ = api.ID(o["credential"])
	s.cacheSeconds, _ = api.ID(o["cache_seconds"])
	s.parameters, _ = o["parameters"].(map[string]any)
	return s
}

func loadChoiceSource(c *Ctx, id int64) (*choiceSource, error) {
	sys := *c
	sys.User = rbac.System()
	o, err := api.Get(&sys, api.Lookup("choice_sources"), id)
	if err != nil {
		return nil, fmt.Errorf("choice source %d does not exist", id)
	}
	return sourceFrom(o), nil
}

// sourceEntry is one item of a source's list.
type sourceEntry struct {
	Value string `json:"value"`
	Label string `json:"label"`
	Item  any    `json:"item"`
}

// choiceOption is a choice and what people see for it.
type choiceOption struct {
	Value string `json:"value"`
	Label string `json:"label"`
}

// sourceOptions lists a source's choices for the user of c, given the
// answers to the earlier questions.
func sourceOptions(c *Ctx, id int64, answered map[string]any) ([]choiceOption, error) {
	src, err := loadChoiceSource(c, id)
	if err != nil {
		return nil, err
	}
	user := requesterEnv(c)
	entries, err := src.entries(c, answered, user, false)
	if err != nil {
		return nil, err
	}
	entries, err = src.filter(entries, answered, user)
	if err != nil {
		return nil, err
	}
	out := make([]choiceOption, len(entries))
	for i, e := range entries {
		out[i] = choiceOption{Value: e.Value, Label: e.Label}
	}
	return out, nil
}

// entries is the source's list before its filter: an http source's from
// its cache or its API, a job source's as last published. fresh skips the
// cache.
func (s *choiceSource) entries(c *Ctx, answered, user map[string]any, fresh bool) ([]sourceEntry, error) {
	if s.kind == sourceJob {
		entries, _, err := cachedSourceList(c, s.id, "")
		if errors.Is(err, pgx.ErrNoRows) {
			msg := "the source has no list yet: its job template hasn't published one"
			if s.lastError != "" {
				msg += " (" + strings.TrimSuffix(s.lastError, ".") + ")"
			}
			return nil, errors.New(msg)
		}
		return entries, err
	}
	target, err := s.target(answered, user)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256([]byte(target))
	key := hex.EncodeToString(sum[:])
	cached, fetched, cacheErr := cachedSourceList(c, s.id, key)
	if cacheErr == nil && !fresh && time.Since(fetched) < time.Duration(s.cacheSeconds)*time.Second {
		return cached, nil
	}
	data, err := s.call(c, target)
	if err == nil {
		var entries []sourceEntry
		if entries, err = s.read(data); err == nil {
			return entries, storeSourceList(c, s.id, key, entries)
		}
	}
	if cacheErr == nil && !fresh {
		return cached, nil
	}
	return nil, err
}

func cachedSourceList(c *Ctx, id int64, key string) ([]sourceEntry, time.Time, error) {
	var entries []sourceEntry
	var fetched time.Time
	err := c.DB().QueryRow(c, "SELECT entries, fetched FROM choice_source_lists WHERE source_id = $1 AND key = $2", id, key).Scan(&entries, &fetched)
	return entries, fetched, err
}

func storeSourceList(c *Ctx, id any, key string, entries []sourceEntry) error {
	if entries == nil {
		entries = []sourceEntry{}
	}
	b, err := json.Marshal(entries)
	if err != nil {
		return err
	}
	_, err = c.DB().Exec(c, `INSERT INTO choice_source_lists (source_id, key, entries) VALUES ($1, $2, $3)
		ON CONFLICT (source_id, key) DO UPDATE SET entries = EXCLUDED.entries, fetched = now()`, id, key, string(b))
	return err
}

// target is the URL an http source calls, its parameters rendered with the
// answers and the user.
func (s *choiceSource) target(answered, user map[string]any) (string, error) {
	u, err := url.Parse(s.url)
	if err != nil {
		return "", fmt.Errorf("the source's URL doesn't parse")
	}
	q := u.Query()
	names := make([]string, 0, len(s.parameters))
	for k := range s.parameters {
		names = append(names, k)
	}
	sort.Strings(names)
	env := map[string]any{"answers": answered, "user": user}
	for _, k := range names {
		v, err := tmpl.RenderBlank(str(s.parameters[k]), env)
		if err != nil {
			return "", fmt.Errorf("parameter %s: %v", k, err)
		}
		q.Set(k, v)
	}
	u.RawQuery = q.Encode()
	return u.String(), nil
}

// hostPolicy is what the settings allow http sources to call.
type hostPolicy struct {
	hosts netguard.Hosts
	guard netguard.Guard
}

func sourceHosts(c *Ctx) hostPolicy {
	var hosts, addrs []string
	c.App.Settings.Decode(c, ChoiceSourceHostsSetting, &hosts)
	c.App.Settings.Decode(c, ChoiceSourceLocalAddressesSetting, &addrs)
	h, _ := netguard.ParseHosts(hosts)
	a, _ := netguard.ParseAllowed(addrs)
	return hostPolicy{hosts: h, guard: netguard.Guard{Allowed: a, Setting: ChoiceSourceLocalAddressesSetting}}
}

func (p hostPolicy) check(u *url.URL) error {
	if p.hosts.Match(u) {
		return nil
	}
	return fmt.Errorf("%s is not a host choice sources may call: an administrator lists those in %s", netguard.Origin(u), ChoiceSourceHostsSetting)
}

// call gets the JSON at target, as the source's credential, through the
// administrator's host list and the refused addresses.
func (s *choiceSource) call(c *Ctx, target string) (any, error) {
	p := sourceHosts(c)
	u, err := url.Parse(target)
	if err != nil {
		return nil, err
	}
	if err := p.check(u); err != nil {
		return nil, err
	}
	if err := p.guard.CheckURL(u); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(c, sourceTimeout)
	defer cancel()
	t := &http.Transport{Proxy: http.ProxyFromEnvironment, ForceAttemptHTTP2: true}
	client := &http.Client{Transport: p.guard.Transport(t, p.check)}
	req, err := http.NewRequestWithContext(ctx, "GET", target, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	if s.credential != 0 {
		if err := s.authenticate(ctx, c, p, client, req); err != nil {
			return nil, err
		}
	}
	resp, err := client.Do(req)
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
		if errors.Is(err, context.DeadlineExceeded) {
			return nil, fmt.Errorf("%s didn't answer within %s", netguard.Origin(u), sourceTimeout)
		}
		return nil, fmt.Errorf("calling %s: %v", netguard.Origin(u), err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("%s answered %s", netguard.Origin(u), resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxSourceReply+1))
	if err != nil {
		return nil, fmt.Errorf("reading the reply of %s: %v", netguard.Origin(u), err)
	}
	if len(body) > maxSourceReply {
		return nil, fmt.Errorf("the reply of %s is over 1 MB", netguard.Origin(u))
	}
	var data any
	if err := json.Unmarshal(body, &data); err != nil {
		return nil, fmt.Errorf("the reply of %s is not JSON", netguard.Origin(u))
	}
	return data, nil
}

func (s *choiceSource) authenticate(ctx context.Context, c *Ctx, p hostPolicy, client *http.Client, req *http.Request) error {
	rc, err := ResolveCredential(ctx, c.App, s.credential)
	if err != nil {
		return fmt.Errorf("the source's credential: %v", err)
	}
	in := func(k string) string { return str(rc.Inputs[k]) }
	switch rc.Namespace {
	case "http_api_token":
		header := in("header")
		if header == "" {
			header = "Authorization"
		}
		req.Header.Set(header, strings.TrimSpace(in("prefix")+" "+in("token")))
	case "http_api_basic":
		req.SetBasicAuth(in("username"), in("password"))
	case "oauth2_client_credentials":
		tu, err := url.Parse(in("token_url"))
		if err != nil {
			return errors.New("the credential's token URL doesn't parse")
		}
		if err := p.check(tu); err != nil {
			return err
		}
		cc := clientcredentials.Config{ClientID: in("client_id"), ClientSecret: in("client_secret"), TokenURL: tu.String(), Scopes: strings.Fields(in("scope"))}
		tok, err := cc.Token(context.WithValue(ctx, oauth2.HTTPClient, client))
		if err != nil {
			var re *oauth2.RetrieveError
			if errors.As(err, &re) && re.Response != nil {
				return fmt.Errorf("getting a token from %s: it answered %s", netguard.Origin(tu), re.Response.Status)
			}
			return fmt.Errorf("getting a token from %s failed", netguard.Origin(tu))
		}
		tok.SetAuthHeader(req)
	default:
		return errors.New("the source's credential is not of a type choice sources use")
	}
	return nil
}

// read turns data (an API's reply, a job's artifacts) into the source's
// entries with its items, value and label expressions.
func (s *choiceSource) read(data any) ([]sourceEntry, error) {
	var list []any
	if s.items == "" {
		l, ok := data.([]any)
		if !ok {
			return nil, errors.New("the data is not a list: set items to the expression that gives it, such as data.results")
		}
		list = l
	} else {
		l, err := evalLoop(s.items, map[string]any{"data": data})
		if err != nil {
			return nil, fmt.Errorf("items: %v", err)
		}
		list = l
	}
	value, err := compileSourceExpr(s.value)
	if err != nil {
		return nil, fmt.Errorf("value: %v", err)
	}
	label, err := compileSourceExpr(s.label)
	if err != nil {
		return nil, fmt.Errorf("label: %v", err)
	}
	out := []sourceEntry{}
	seen := map[string]bool{}
	for i, it := range list {
		if len(out) == maxSurveyChoices {
			break
		}
		v := it
		if value != nil {
			if v, err = expr.Run(value, map[string]any{"item": it}); err != nil {
				return nil, fmt.Errorf("value, item %d: %v", i, err)
			}
		}
		vs, ok := choiceString(v)
		if !ok {
			return nil, fmt.Errorf("value gives %s for item %d, not a string or a number: set value, such as item.name", kindOf(v), i)
		}
		if vs == "" || seen[vs] {
			continue
		}
		seen[vs] = true
		ls := vs
		if label != nil {
			l, err := expr.Run(label, map[string]any{"item": it, "value": vs})
			if err != nil {
				return nil, fmt.Errorf("label, item %d: %v", i, err)
			}
			if ls, ok = choiceString(l); !ok || ls == "" {
				ls = vs
			}
		}
		out = append(out, sourceEntry{Value: vs, Label: ls, Item: it})
	}
	return out, nil
}

// filter keeps the entries the source's filter is true for.
func (s *choiceSource) filter(entries []sourceEntry, answered, user map[string]any) ([]sourceEntry, error) {
	prog, err := compileSourceExpr(s.flt)
	if err != nil || prog == nil {
		return entries, err
	}
	out := []sourceEntry{}
	for _, e := range entries {
		res, err := expr.Run(prog, map[string]any{"item": e.Item, "value": e.Value, "label": e.Label, "answers": answered, "user": user})
		if err != nil {
			return nil, fmt.Errorf("the source's filter: %v", err)
		}
		if truthy(res) {
			out = append(out, e)
		}
	}
	return out, nil
}

func compileSourceExpr(src string) (*vm.Program, error) {
	if src == "" {
		return nil, nil
	}
	return expr.Compile(src, expr.AllowUndefinedVariables())
}

// choiceString is a choice's text: a string, or a number or boolean as
// written in JSON.
func choiceString(v any) (string, bool) {
	switch x := v.(type) {
	case string:
		return x, true
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64), true
	case bool, json.Number:
		return fmt.Sprint(x), true
	}
	rv := reflect.ValueOf(v)
	switch rv.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64, reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return fmt.Sprint(v), true
	}
	return "", false
}

func kindOf(v any) string {
	switch v.(type) {
	case nil:
		return "nothing"
	case map[string]any:
		return "an object"
	case []any:
		return "a list"
	}
	return fmt.Sprintf("%T", v)
}

// jobSourceEntries reads the list a job published.
func jobSourceEntries(c *Ctx, s *choiceSource, job int64) ([]sourceEntry, error) {
	var artifacts map[string]any
	if err := c.DB().QueryRow(c, "SELECT artifacts FROM unified_jobs WHERE id = $1", job).Scan(&artifacts); err != nil {
		return nil, fmt.Errorf("job %d: %v", job, err)
	}
	return s.read(artifacts)
}

// testChoiceSourceView lists a source as its admins set it up: a fresh call
// for an http source, with the answers given; a sample of the items, to
// write the expressions.
func testChoiceSourceView(c *Ctx, w http.ResponseWriter, r *http.Request, obj Obj) {
	body, err := api.ReadBody(r)
	if err != nil {
		api.WriteError(w, err)
		return
	}
	answered, ok := body["answers"].(map[string]any)
	if !ok && body["answers"] != nil {
		api.WriteError(w, api.FieldError("answers", "Expected an object of answers by variable."))
		return
	}
	if answered == nil {
		answered = map[string]any{}
	}
	src := sourceFrom(obj)
	user := requesterEnv(c)
	out := Obj{"count": 0, "choices": []choiceOption{}, "sample": []any{}}
	var entries []sourceEntry
	if src.kind == sourceHTTP {
		var target string
		var data any
		if target, err = src.target(answered, user); err == nil {
			out["url"] = target
			if data, err = src.call(c, target); err == nil {
				if entries, err = src.read(data); err != nil {
					out["reply"] = excerpt(data)
				}
			}
		}
	} else {
		entries, err = src.entries(c, answered, user, false)
	}
	if err == nil {
		entries, err = src.filter(entries, answered, user)
	}
	if err != nil {
		out["error"] = err.Error()
		api.WriteJSON(w, 200, out)
		return
	}
	choices, sample := []choiceOption{}, []any{}
	for i, e := range entries {
		if i < 50 {
			choices = append(choices, choiceOption{Value: e.Value, Label: e.Label})
		}
		if i < 3 {
			sample = append(sample, e.Item)
		}
	}
	out["count"], out["choices"], out["sample"] = len(entries), choices, sample
	api.WriteJSON(w, 200, out)
}

// excerpt is the start of a reply that the expressions couldn't read.
func excerpt(data any) string {
	b, _ := json.MarshalIndent(data, "", "  ")
	if len(b) > 2000 {
		return string(b[:2000]) + "\n…"
	}
	return string(b)
}

// refreshChoiceSourceView runs a job source's template now, or drops an
// http source's cached lists.
func refreshChoiceSourceView(c *Ctx, w http.ResponseWriter, r *http.Request, obj Obj) {
	if str(obj["kind"]) == sourceHTTP {
		if _, err := c.DB().Exec(c, "DELETE FROM choice_source_lists WHERE source_id = $1", obj["id"]); err != nil {
			api.WriteError(w, err)
			return
		}
		api.WriteJSON(w, 200, Obj{"job": nil})
		return
	}
	if err := runningRefresh(c, obj); err != nil {
		api.WriteError(w, err)
		return
	}
	job, err := refreshChoiceSource(c, obj)
	if err != nil {
		api.WriteError(w, err)
		return
	}
	api.WriteJSON(w, 202, Obj{"job": job})
}

func runningRefresh(c *Ctx, obj Obj) error {
	last, ok := api.ID(obj["last_job"])
	if !ok {
		return nil
	}
	var running bool
	c.DB().QueryRow(c, "SELECT status IN ('new', 'pending', 'waiting', 'running') FROM unified_jobs WHERE id = $1", last).Scan(&running)
	if running {
		return api.Detail(409, fmt.Sprintf("Job %d is refreshing the list already.", last))
	}
	return nil
}

// refreshChoiceSource launches a job source's template, as the user of c.
func refreshChoiceSource(c *Ctx, obj Obj) (int64, error) {
	id, _ := api.ID(obj["id"])
	jtID, ok := api.ID(obj["job_template"])
	if !ok {
		return 0, api.Detail(400, "The source has no job template.")
	}
	sys := *c
	sys.User = rbac.System()
	jt, err := api.Get(&sys, api.Lookup("job_templates"), jtID)
	if err != nil {
		return 0, err
	}
	job, _, err := LaunchJobTemplate(c, jt, Obj{}, launchChoiceSource, nil)
	if err != nil {
		c.DB().Exec(c, "UPDATE choice_sources SET last_error = $2 WHERE id = $1", id, fmt.Sprintf("The refresh couldn't start: %v", err))
		return 0, err
	}
	_, err = c.DB().Exec(c, `UPDATE choice_sources SET last_job_id = $2,
		next_refresh = CASE WHEN refresh_minutes > 0 THEN now() + refresh_minutes * interval '1 minute' END WHERE id = $1`, id, job)
	return job, err
}

var sourceCleanup struct {
	sync.Mutex
	last time.Time
}

// ChoiceSourceTick refreshes the job sources that are due, and drops http
// sources' lists too old to fall back on. It runs on the dispatcher leader.
func ChoiceSourceTick(ctx context.Context, m *jobs.Manager) {
	rows, err := m.DB().Query(ctx, `SELECT s.id FROM choice_sources s WHERE s.kind = 'job' AND s.refresh_minutes > 0 AND s.next_refresh <= now()
		AND s.job_template_id IS NOT NULL AND NOT EXISTS (SELECT 1 FROM unified_jobs j WHERE j.id = s.last_job_id AND j.status IN ('new', 'pending', 'waiting', 'running'))
		ORDER BY s.next_refresh LIMIT 20`)
	if err != nil {
		return
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if rows.Scan(&id) == nil {
			ids = append(ids, id)
		}
	}
	rows.Close()
	c := &api.Ctx{Context: ctx, App: m.App, User: rbac.System(), Extra: map[string]any{}}
	for _, id := range ids {
		obj, err := api.Get(c, api.Lookup("choice_sources"), id)
		if err != nil {
			continue
		}
		if _, err := refreshChoiceSource(c, obj); err != nil {
			slog.Error("choice source refresh failed", "source", id, "err", err)
			m.DB().Exec(ctx, "UPDATE choice_sources SET next_refresh = now() + refresh_minutes * interval '1 minute' WHERE id = $1", id)
		}
	}
	sourceCleanup.Lock()
	due := time.Since(sourceCleanup.last) > time.Hour
	if due {
		sourceCleanup.last = time.Now()
	}
	sourceCleanup.Unlock()
	if due {
		m.DB().Exec(ctx, "DELETE FROM choice_source_lists WHERE key <> '' AND fetched < $1", time.Now().Add(-staleSourceLists))
	}
}

// ChoiceSourceJobFinished publishes the list a refresh job made, or keeps
// the last one and says why.
func ChoiceSourceJobFinished(ctx context.Context, m *jobs.Manager, job jobs.Row) {
	if job.Str("launch_type") != launchChoiceSource {
		return
	}
	c := &api.Ctx{Context: ctx, App: m.App, User: rbac.System(), Extra: map[string]any{}}
	rows, err := m.DB().Query(ctx, "SELECT id FROM choice_sources WHERE last_job_id = $1", job.ID())
	if err != nil {
		return
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if rows.Scan(&id) == nil {
			ids = append(ids, id)
		}
	}
	rows.Close()
	for _, id := range ids {
		obj, err := api.Get(c, api.Lookup("choice_sources"), id)
		if err != nil {
			continue
		}
		kept := "the list is still the last one published"
		if obj["last_refresh"] == nil {
			kept = "the source has no list yet"
		}
		if status := job.Str("status"); status != "successful" {
			m.DB().Exec(ctx, "UPDATE choice_sources SET last_error = $2 WHERE id = $1", id,
				fmt.Sprintf("Job %d %s; %s.", job.ID(), status, kept))
			continue
		}
		entries, err := jobSourceEntries(c, sourceFrom(obj), job.ID())
		if err != nil {
			m.DB().Exec(ctx, "UPDATE choice_sources SET last_error = $2 WHERE id = $1", id,
				fmt.Sprintf("Job %d published nothing that reads as a list (%v); %s.", job.ID(), err, kept))
			continue
		}
		err = c.WithTx(func(c *Ctx) error {
			if err := storeSourceList(c, id, "", entries); err != nil {
				return err
			}
			_, err := c.DB().Exec(c, "UPDATE choice_sources SET last_refresh = now(), last_refresh_job_id = $2, last_error = '' WHERE id = $1", id, job.ID())
			return err
		})
		if err != nil {
			slog.Error("storing a choice source's list", "source", id, "err", err)
		}
	}
}
