package resources

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/damhau/awxnext/internal/api"
	galaxyv3 "github.com/damhau/awxnext/internal/galaxy"
	"github.com/damhau/awxnext/internal/jobs"
	"github.com/damhau/awxnext/internal/netguard"
	"github.com/damhau/awxnext/internal/rbac"
	"github.com/damhau/awxnext/internal/version"
)

// Remotes (M13-04) mirror collections of another Galaxy server, as Private
// Automation Hub's do: a collection_remote template lists them in a
// requirements file, and each collection_sync job copies the versions in
// their ranges, with their dependencies and signatures, into the remote's
// own repository. Jobs install from it through its managed credential.

const (
	// HubRemoteLocalAddressesSetting lists the local addresses remotes'
	// servers may be at.
	HubRemoteLocalAddressesSetting = "HUB_REMOTE_ALLOWED_LOCAL_ADDRESSES"
	originRemote                   = "remote"
	originImport                   = "import"
	maxRemoteRequirements          = 1000
)

var repositoryNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)

// remoteRequirement is a collection a remote mirrors: every version in the
// range.
type remoteRequirement struct {
	Namespace, Name, Spec string
}

func (r remoteRequirement) fqcn() string { return r.Namespace + "." + r.Name }

// parseRemoteRequirements reads a remote's requirements file: a
// requirements.yml's collections, each a name or a name and a version
// range, from the remote's own server.
func parseRemoteRequirements(text string) ([]remoteRequirement, error) {
	var doc any
	if err := yaml.Unmarshal([]byte(text), &doc); err != nil {
		return nil, fmt.Errorf("The requirements file is not YAML: %v", err)
	}
	var list []any
	switch d := doc.(type) {
	case map[string]any:
		for k, v := range d {
			switch k {
			case "collections":
				l, ok := v.([]any)
				if !ok && v != nil {
					return nil, errors.New("collections: is a list of collections.")
				}
				list = l
			case "roles":
				if v != nil {
					return nil, errors.New("A remote mirrors collections, not roles: remove roles:.")
				}
			default:
				return nil, fmt.Errorf("Unknown key %s: a requirements file lists collections: only.", k)
			}
		}
	case nil:
	default:
		return nil, errors.New("List the collections under collections:, as a requirements.yml does.")
	}
	if len(list) == 0 {
		return nil, errors.New("The requirements file lists no collections.")
	}
	if len(list) > maxRemoteRequirements {
		return nil, fmt.Errorf("The requirements file lists more than %d collections.", maxRemoteRequirements)
	}
	var out []remoteRequirement
	seen := map[string]bool{}
	for i, item := range list {
		var name, spec string
		switch e := item.(type) {
		case string:
			name = e
		case map[string]any:
			for k, v := range e {
				switch k {
				case "name":
					name, _ = v.(string)
				case "version":
					switch x := v.(type) {
					case string:
						spec = x
					case int:
						spec = fmt.Sprint(x)
					default:
						return nil, fmt.Errorf("Collection %d: quote its version range.", i+1)
					}
				case "type":
					if v != "galaxy" {
						return nil, fmt.Errorf("Collection %d: a remote mirrors collections of its Galaxy server only, not of type %v.", i+1, v)
					}
				case "source":
					return nil, fmt.Errorf("Collection %d: a remote mirrors from the server of its credential: remove source:, or make a remote for that server.", i+1)
				default:
					return nil, fmt.Errorf("Collection %d: unknown key %s (name, version).", i+1, k)
				}
			}
		default:
			return nil, fmt.Errorf("Collection %d: give its name, or its name and version.", i+1)
		}
		ns, n, ok := strings.Cut(strings.TrimSpace(name), ".")
		if !ok || !validGalaxyName(ns) || !validGalaxyName(n) {
			return nil, fmt.Errorf("Collection %d: %q is not a collection name such as community.general.", i+1, name)
		}
		if spec = strings.TrimSpace(spec); spec == "" {
			spec = "*"
		}
		if !validVersionSpec(spec) {
			return nil, fmt.Errorf("Collection %s: %q is not a version range such as >=1.0.0,<2.0.0.", name, spec)
		}
		r := remoteRequirement{ns, n, spec}
		if seen[r.fqcn()] {
			return nil, fmt.Errorf("Collection %s is listed twice: join its ranges with a comma.", r.fqcn())
		}
		seen[r.fqcn()] = true
		out = append(out, r)
	}
	return out, nil
}

func validateRemoteRequirements(c *Ctx, v any, data Obj) error {
	_, err := parseRemoteRequirements(str(v))
	return err
}

// remoteCandidates are the versions in the range, lowest first: pre-releases
// only when the range names them, as ansible-galaxy picks them.
func remoteCandidates(versions []string, spec string) []string {
	var out []string
	for _, v := range versions {
		sv, ok := parseSemver(v)
		if !ok || len(sv.pre) > 0 && !pinsVersion(spec, v) {
			continue
		}
		if versionMeets(v, spec) && !slices.Contains(out, v) {
			out = append(out, v)
		}
	}
	sort.Slice(out, func(i, j int) bool { return compareSemver(out[i], out[j]) < 0 })
	return out
}

func pinsVersion(spec, v string) bool {
	for _, part := range strings.Split(spec, ",") {
		if op, req, ok := parseVersionReq(part); ok && op == "==" && compareSemver(req, v) == 0 {
			return true
		}
	}
	return false
}

// remoteRepositoryName is the repository a remote syncs into: the one it
// names, or one from its name.
func remoteRepositoryName(c *Ctx, given, name string) (string, error) {
	repo := given
	if repo == "" {
		repo = strings.Trim(slugRe.ReplaceAllString(strings.ToLower(name), "-"), "-_")
		if len(repo) > 64 {
			repo = strings.TrimRight(repo[:64], "-_")
		}
	}
	if !repositoryNameRe.MatchString(repo) {
		return "", api.FieldError("repository", "Give a repository name of lowercase letters, digits, - and _, up to 64.")
	}
	if repo == hubPublished || curatedOnly(repo) || countQuery(c, "SELECT count(*) FROM collection_repositories WHERE name = $1", repo) > 0 {
		return "", api.FieldError("repository", fmt.Sprintf("The hub has a repository %s: give the remote another.", repo))
	}
	return repo, nil
}

// RemoteCredentialPath is where clients install a remote's repository from.
func RemoteCredentialPath(repo string) string { return "/api/galaxy/content/" + repo + "/" }

func remoteCredentialName(repo string) string { return HubCredentialName + ": " + repo }

// requireRemoteServer checks a remote's credential: a Galaxy credential of
// another server, not one of awxnext's own repositories.
func requireRemoteServer(c *Ctx, v any) error {
	if err := requireCredentialKind(c, "credential", v, "galaxy"); err != nil {
		return err
	}
	id, ok := api.ID(v)
	if !ok {
		return nil
	}
	var u string
	c.DB().QueryRow(c, "SELECT coalesce(inputs->>'url', '') FROM credentials WHERE id = $1", id).Scan(&u)
	if p, err := url.Parse(u); err != nil || (p.Scheme != "http" && p.Scheme != "https") || p.Host == "" {
		return api.FieldError("credential", "The credential's URL is not another server's: a remote mirrors from an http:// or https:// Galaxy server.")
	}
	return nil
}

// LaunchCollectionSync starts a sync of a remote.
func LaunchCollectionSync(c *Ctx, remote Obj, launchType string) (int64, error) {
	id, _ := api.ID(remote["id"])
	values := map[string]any{
		"type":                    "collection_sync",
		"name":                    str(remote["name"]),
		"description":             str(remote["description"]),
		"unified_job_template_id": id,
		"organization_id":         remote["organization"],
		"credential_id":           remote["credential"],
		"launch_type":             launchType,
	}
	if !c.User.Anonymous() {
		values["created_by_id"] = c.User.UserID
		values["modified_by_id"] = c.User.UserID
	}
	jid, err := jobs.Create(c, c.DB(), values)
	if err != nil {
		return 0, err
	}
	jobs.Wake(c, c.DB())
	return jid, nil
}

func registerCollectionRemotes() {
	var shared []*Field
	for _, f := range ujtFields() {
		if f.Name != "execution_environment" {
			shared = append(shared, f)
		}
	}
	remotes := api.Register(&Resource{
		Name:          "collection_remotes",
		Type:          "collection_remote",
		Table:         "unified_job_templates",
		Discriminator: true,
		Verbose:       "Collection Remote",
		Ordering:      "name",
		OrgColumn:     "organization_id",
		Fields: append([]*Field{
			{Name: "organization", Type: api.TFK, Ref: "organizations", Required: true,
				Help: "The organization whose members and jobs read the remote's repository, and that owns the namespaces its syncs create."},
			{Name: "credential", Type: api.TFK, Ref: "credentials", Required: true,
				Help: "An Ansible Galaxy/Automation Hub API Token credential: the server's URL, its token, and for Red Hat's Automation Hub its SSO URL."},
			{Name: "requirements_file", Column: "collection_remote_requirements", Type: api.TText, Required: true, Validate: validateRemoteRequirements,
				Help: "The collections to mirror, as a requirements.yml lists them: each name, with a version range of the versions to mirror."},
			{Name: "sync_dependencies", Column: "collection_sync_dependencies", Type: api.TBool, Default: true,
				Help: "Mirror the collections' dependencies too: the highest version in the range each mirrored version asks for."},
			{Name: "signature_validation_credential", Type: api.TFK, Ref: "credentials", Nullable: true,
				Help: "A GPG Public Key credential: syncs keep only the signatures its keys verify, and only the versions that have one."},
			{Name: "proxy_url", Column: "scm_proxy", Type: api.TSecret, Default: "", Validate: validateProxyURL,
				Help: "The HTTP proxy syncs go through, such as http://user:password@proxy.example.com:3128. Empty: the server's HTTPS_PROXY, if any."},
			{Name: "tls_validation", Column: "scm_verify_ssl", Type: api.TBool, Default: true, Help: "Check the server's TLS certificate."},
			{Name: "repository", Column: "collection_repository", Type: api.TString, Default: "", CreateOnly: true, MaxLength: 64,
				Help: "The repository the remote syncs into, which clients install from at /api/galaxy/content/<repository>/. Empty: one from the remote's name."},
			{Name: "galaxy_credential", Type: api.TFK, Ref: "credentials", ReadOnly: true, Virtual: true, Nullable: true,
				Help: "The managed credential of the remote's repository: add it to an organization's Galaxy credentials for its jobs to install from it."},
		}, shared...),
		View: func(c *Ctx, arg ArgFunc) string {
			if c.User.Superuser || c.User.Auditor {
				return "TRUE"
			}
			return remoteReaders(c, arg)
		},
		CanAdd:    func(c *Ctx, data Obj) error { return requireSuperuser(c) },
		CanChange: func(c *Ctx, old, data Obj) error { return requireSuperuser(c) },
		CanDelete: func(c *Ctx, obj Obj) error { return requireSuperuser(c) },
		Perm: func(c *Ctx, obj Obj, perm string) (bool, error) {
			return perm == "view" || c.User.Superuser, nil
		},
		Capabilities: func(c *Ctx, obj Obj, _ map[string]bool) map[string]bool {
			su := c.User.Superuser
			return map[string]bool{"edit": su, "delete": su, "start": su, "schedule": su, "copy": false}
		},
		BeforeSave: func(c *Ctx, old, data Obj) error {
			if err := requireRemoteServer(c, data["credential"]); err != nil {
				return err
			}
			if err := requireCredentialKind(c, "signature_validation_credential", data["signature_validation_credential"], "cryptography"); err != nil {
				return err
			}
			if old != nil {
				return nil
			}
			repo, err := remoteRepositoryName(c, str(data["repository"]), str(data["name"]))
			if err != nil {
				return err
			}
			data["repository"], data["status"] = repo, "never updated"
			return nil
		},
		AfterCreate: func(c *Ctx, obj Obj) error {
			id, _ := api.ID(obj["id"])
			repo := str(obj["repository"])
			if _, err := c.DB().Exec(c, "INSERT INTO collection_repositories (name, description, remote_id) VALUES ($1, $2, $3)",
				repo, "Collections mirrored by the remote "+str(obj["name"])+".", id); err != nil {
				return err
			}
			var cred int64
			if err := c.DB().QueryRow(c, `INSERT INTO credentials (name, description, credential_type_id, inputs, managed, organization_id)
				SELECT $1, $2, id, jsonb_build_object('url', $3::text), true, $4 FROM credential_types WHERE namespace = 'galaxy_api_token' AND managed
				RETURNING id`, remoteCredentialName(repo), "awxnext's collection hub, the repository of the remote "+str(obj["name"])+": each install gets a read token of its own.",
				RemoteCredentialPath(repo), obj["organization"]).Scan(&cred); err != nil {
				return err
			}
			return rbac.EnsureObjectRoles(c, c.DB(), "credential", cred)
		},
		AfterUpdate: func(c *Ctx, old, obj Obj) error {
			if fmt.Sprint(old["organization"]) == fmt.Sprint(obj["organization"]) {
				return nil
			}
			_, err := c.DB().Exec(c, "UPDATE credentials SET organization_id = $2, modified = now() WHERE managed AND inputs->>'url' = $1",
				RemoteCredentialPath(str(obj["repository"])), obj["organization"])
			return err
		},
		BeforeDelete: func(c *Ctx, obj Obj) error {
			if countQuery(c, "SELECT count(*) FROM unified_jobs WHERE unified_job_template_id = $1 AND status IN ('pending','waiting','running')", obj["id"]) > 0 {
				return api.Detail(409, "Resource is being used by running jobs.")
			}
			return deleteRemoteRepository(c, obj)
		},
		Summary: func(c *Ctx, objs []Obj) error {
			paths := make([]string, len(objs))
			for i, o := range objs {
				paths[i] = RemoteCredentialPath(str(o["repository"]))
			}
			creds := map[string]int64{}
			rows, err := c.DB().Query(c, "SELECT inputs->>'url', id FROM credentials WHERE managed AND inputs->>'url' = ANY($1)", paths)
			if err != nil {
				return err
			}
			for rows.Next() {
				var p string
				var id int64
				rows.Scan(&p, &id)
				creds[p] = id
			}
			rows.Close()
			for i, o := range objs {
				o["galaxy_credential"] = nil
				if id, ok := creds[paths[i]]; ok {
					o["galaxy_credential"] = id
					o["related"].(Obj)["galaxy_credential"] = api.Lookup("credentials").URL(id)
				}
			}
			return ujtSummary(c, objs)
		},
	})
	remotes.SubLists = append([]*SubList{
		{Name: "collection_syncs", Target: "collection_syncs", ReadOnly: true, SkipView: true,
			Filter: func(arg ArgFunc, pid int64) string { return "t.unified_job_template_id = " + arg(pid) }},
		{Name: "schedules", Target: "schedules", ParentField: "unified_job_template",
			Filter: func(arg ArgFunc, pid int64) string { return "t.unified_job_template_id = " + arg(pid) }},
	}, notificationSubs("collectionremote", false)...)
	remotes.Actions = []*api.Action{{Name: "sync", Methods: []string{"GET", "POST"}, Handler: func(c *Ctx, w http.ResponseWriter, r *http.Request, obj Obj) {
		if r.Method == "GET" {
			api.WriteJSON(w, 200, Obj{"can_sync": c.User.Superuser})
			return
		}
		if !c.User.Superuser {
			api.WriteError(w, api.ErrForbidden)
			return
		}
		id, err := LaunchCollectionSync(c, obj, "manual")
		if err != nil {
			api.WriteError(w, err)
			return
		}
		job, err := api.Get(c, api.Lookup("collection_syncs"), id)
		if err != nil {
			api.WriteError(w, err)
			return
		}
		out, err := api.SerializeOne(c, api.Lookup("collection_syncs"), job)
		if err != nil {
			api.WriteError(w, err)
			return
		}
		out["collection_sync"] = id
		api.WriteJSON(w, 202, out)
	}}}
	scheduleLaunchers["collection_remote"] = func(c *Ctx, remote Obj, s Obj) (int64, error) {
		return LaunchCollectionSync(c, remote, "scheduled")
	}

	registerJobResource(jobSpec{
		name: "collection_syncs", typ: "collection_sync", verbose: "Collection Sync",
		templateField: "collection_remote", templateRes: "collection_remotes", eventsName: "events", eventsRes: "collection_sync_events",
		visible: func(c *Ctx, arg ArgFunc) string {
			return "t.unified_job_template_id IN (SELECT t.id FROM unified_job_templates t WHERE t.type = 'collection_remote' AND (" + remoteReaders(c, arg) + "))"
		},
		canManage: func(c *Ctx, job Obj) (bool, error) { return c.User.Superuser, nil },
		extra: []*Field{
			{Name: "credential", Type: api.TFK, Ref: "credentials", ReadOnly: true, Nullable: true},
		},
		omit: []string{"execution_environment"},
	})
	registerEventResource("collection_sync_events", "collection_sync_event", "collection_sync", "collection_sync")
}

// remoteReaders is the condition on remotes (alias t) whose repository the
// principal reads: the members and auditors of their organization.
func remoteReaders(c *Ctx, arg ArgFunc) string {
	a := rbac.ArgFunc(arg)
	s := rbac.Scope{ID: "t.organization_id", Org: "t.organization_id"}
	return "(" + c.User.Predicate(a, "organization", "member_organization", s) + " OR " + c.User.Predicate(a, "organization", "audit_organization", s) + ")"
}

// deleteRemoteRepository deletes a remote's repository, the versions only
// it held, and its managed credential.
func deleteRemoteRepository(c *Ctx, remote Obj) error {
	id, _ := api.ID(remote["id"])
	var versions []int64
	rows, err := c.DB().Query(c, `SELECT rc.version_id FROM collection_repository_contents rc JOIN collection_repositories r ON r.id = rc.repository_id
		WHERE r.remote_id = $1`, id)
	if err != nil {
		return err
	}
	for rows.Next() {
		var v int64
		rows.Scan(&v)
		versions = append(versions, v)
	}
	rows.Close()
	if _, err := c.DB().Exec(c, "DELETE FROM collection_repositories WHERE remote_id = $1", id); err != nil {
		return err
	}
	if err := deleteUnheldVersions(c, c.DB(), versions); err != nil {
		return err
	}
	_, err = c.DB().Exec(c, "DELETE FROM credentials WHERE managed AND inputs->>'url' = $1", RemoteCredentialPath(str(remote["repository"])))
	return err
}

// remoteSync is a run of a collection_sync job.
type remoteSync struct {
	app      *api.App
	out      *jobs.Output
	client   *galaxyv3.Client
	mirror   *hubMirror
	deps     bool
	limit    int64
	versions map[string][]string
	done     map[string]mirrorResult
	problems []string
	added    int
	present  int
	skipped  []string
	sigs     int
}

// mirrorResult is what became of a version a sync mirrors.
type mirrorResult int

const (
	mirrorFailed mirrorResult = iota
	mirrorSkipped
	mirrorKept
)

func (s *remoteSync) line(format string, args ...any) { s.out.Line(fmt.Sprintf(format, args...)) }

func (s *remoteSync) problem(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	s.problems = append(s.problems, msg)
	s.line("  ERROR: %s", msg)
}

// skip lists a version among the skipped ones, once: a version the
// re-check takes out is skipped again when its range comes.
func (s *remoteSync) skip(key string) {
	if !slices.Contains(s.skipped, key) {
		s.skipped = append(s.skipped, key)
	}
}

// recheck holds the versions already in the repository to the keys, as
// new ones are, before any requirement is judged: the keys may be new, or
// changed, since they came.
func (s *remoteSync) recheck(ctx context.Context) error {
	m := s.mirror
	held, err := m.heldVersions(ctx)
	if err != nil || len(held) == 0 {
		return err
	}
	what := fmt.Sprintf("%d versions", len(held))
	if len(held) == 1 {
		what = "1 version"
	}
	s.line("Checking the repository's %s with the keys of %s:", what, m.keysName)
	var out []int64
	dropped, failed := 0, 0
	for _, h := range held {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		kept, n, err := m.recheckVersion(ctx, h)
		switch {
		case err != nil:
			failed++
			s.problem("%s: %v", h.key, err)
		case !kept:
			out = append(out, h.id)
			s.skip(h.key)
			s.line("  %s: taken out of the repository, %s", h.key, m.unsigned())
		case n == 1:
			s.line("  %s: 1 signature dropped, which the keys don't verify", h.key)
		case n > 1:
			s.line("  %s: %d signatures dropped, which the keys don't verify", h.key, n)
		}
		dropped += n
	}
	if len(out) == 0 {
		if dropped == 0 && failed == 0 {
			s.line("  Each has a signature the keys verify.")
		}
		return nil
	}
	return deleteUnheldVersions(ctx, s.app.DB, out)
}

func runCollectionSync(ctx context.Context, rc *jobs.RunContext) jobs.Result {
	app := rc.M.App
	var name, reqText, proxy, repo string
	var org int64
	var cred, keysCred *int64
	var deps, verify bool
	if err := app.DB.QueryRow(ctx, `SELECT name, coalesce(organization_id, 0), credential_id, signature_validation_credential_id, collection_remote_requirements,
		collection_sync_dependencies, scm_proxy, scm_verify_ssl, collection_repository FROM unified_job_templates WHERE id = $1 AND type = 'collection_remote'`,
		rc.Job.Int("unified_job_template_id")).Scan(&name, &org, &cred, &keysCred, &reqText, &deps, &proxy, &verify, &repo); err != nil {
		return jobs.Result{Status: "error", Explanation: "The job's remote is gone."}
	}
	fail := func(format string, args ...any) jobs.Result {
		msg := fmt.Sprintf(format, args...)
		rc.Out.Line("ERROR: " + msg)
		return jobs.Result{Status: "failed", Explanation: msg}
	}
	if cred == nil {
		return fail("The remote has no credential: give it its server's Galaxy credential.")
	}
	reqs, err := parseRemoteRequirements(reqText)
	if err != nil {
		return fail("%v", err)
	}
	server, err := ResolveCredential(ctx, app, *cred)
	if err != nil {
		return fail("Reading the remote's credential: %v", err)
	}
	m := &hubMirror{app: app, org: org, origin: originRemote, by: rc.Job.Int("created_by_id")}
	if err := app.DB.QueryRow(ctx, "SELECT id, name FROM collection_repositories WHERE remote_id = $1", rc.Job.Int("unified_job_template_id")).
		Scan(&m.repoID, &m.repo); err != nil {
		return fail("The remote's repository %s is gone.", repo)
	}
	if keysCred != nil {
		keys, err := ResolveCredential(ctx, app, *keysCred)
		if err != nil {
			return fail("Reading the remote's signature validation credential: %v", err)
		}
		m.keys, _ = keys.Inputs["gpg_public_key"].(string)
		m.keysName = keys.Name
	}
	if proxy != "" {
		if proxy, err = app.Crypto.Decrypt(proxy); err != nil {
			return fail("Decrypting the remote's proxy: %v", err)
		}
	}
	httpClient, err := remoteHTTPClient(ctx, app, proxy, verify)
	if err != nil {
		return fail("%v", err)
	}
	input := func(k string) string { v, _ := server.Inputs[k].(string); return v }
	s := &remoteSync{
		app: app, out: rc.Out, mirror: m, deps: deps,
		client: &galaxyv3.Client{HTTP: httpClient, URL: input("url"), AuthURL: input("auth_url"), Token: input("token"),
			UserAgent: "awxnext/" + version.Get().Version},
		limit:    min(int64(app.Settings.Int(ctx, "HUB_MAX_UPLOAD_SIZE")), maxUploadLimit),
		versions: map[string][]string{}, done: map[string]mirrorResult{},
	}
	rc.Out.Line(fmt.Sprintf("Syncing the remote %s into the repository %s.", name, m.repo))
	if m.keys != "" {
		if err := s.recheck(ctx); err != nil {
			if ctx.Err() != nil {
				return jobs.Result{Status: "canceled"}
			}
			return fail("Checking the repository's versions: %v", err)
		}
	}
	if err := s.client.Connect(ctx); err != nil {
		return fail("%v", err)
	}
	signed := "signatures as the server gives them"
	if m.keys != "" {
		signed = "only versions signed by the keys of " + m.keysName
	}
	what := fmt.Sprintf("%d collections", len(reqs))
	if len(reqs) == 1 {
		what = "1 collection"
	}
	if deps {
		what += ", dependencies included"
	}
	s.line("Server %s: %s, %s.", s.client.Root(), what, signed)
	for _, r := range reqs {
		if ctx.Err() != nil {
			return jobs.Result{Status: "canceled"}
		}
		s.require(ctx, r, true, "")
	}
	if ctx.Err() != nil {
		return jobs.Result{Status: "canceled"}
	}
	s.line("Synced: %d versions added, %d already in the repository, %d skipped, %d signatures added.", s.added, s.present, len(s.skipped), s.sigs)
	if len(s.skipped) > 0 {
		s.line("Skipped, with no signature that verifies with the keys of %s: %s.", m.keysName, strings.Join(s.skipped, ", "))
	}
	if len(s.problems) > 0 {
		explanation := fmt.Sprintf("%d problems: %s", len(s.problems), strings.Join(s.problems, "; "))
		if len(s.problems) == 1 {
			explanation = s.problems[0]
		}
		return jobs.Result{Status: "failed", Explanation: explanation}
	}
	return jobs.Result{Status: "successful"}
}

// remoteHTTPClient reaches remotes' servers through the proxy, refusing
// local addresses the setting doesn't allow.
func remoteHTTPClient(ctx context.Context, app *api.App, proxy string, verify bool) (*http.Client, error) {
	var addrs []string
	app.Settings.Decode(ctx, HubRemoteLocalAddressesSetting, &addrs)
	allowed, _ := netguard.ParseAllowed(addrs)
	g := netguard.Guard{Allowed: allowed, Setting: HubRemoteLocalAddressesSetting}
	t := &http.Transport{Proxy: http.ProxyFromEnvironment, ForceAttemptHTTP2: true,
		TLSClientConfig: &tls.Config{InsecureSkipVerify: !verify}} //nolint:gosec // the remote's tls_validation
	if proxy != "" {
		u, err := url.Parse(proxy)
		if err != nil {
			return nil, errors.New("the remote's proxy URL doesn't parse")
		}
		t.Proxy = http.ProxyURL(u)
	}
	return &http.Client{Transport: g.Transport(t, nil)}, nil
}

// require mirrors the versions of a collection in a range: all of them for
// a requirement, the highest that passes the signature check for a
// dependency. A range with no version that passed, in this sync or already
// in the repository, is a problem.
func (s *remoteSync) require(ctx context.Context, r remoteRequirement, all bool, of string) {
	what := r.fqcn() + " " + r.Spec
	if of != "" {
		what += " (a dependency of " + of + ")"
	}
	versions, ok := s.versions[r.fqcn()]
	if !ok {
		var err error
		versions, err = s.client.Versions(ctx, r.Namespace, r.Name)
		if errors.Is(err, galaxyv3.ErrNotFound) {
			s.line("%s:", what)
			s.problem("The server has no collection %s.", r.fqcn())
			return
		}
		if err != nil {
			s.line("%s:", what)
			s.problem("Listing the versions of %s: %v", r.fqcn(), err)
			return
		}
		s.versions[r.fqcn()] = versions
	}
	picked := remoteCandidates(versions, r.Spec)
	if len(picked) == 0 {
		s.line("%s:", what)
		s.problem("No version of %s on the server is in the range %s.", r.fqcn(), r.Spec)
		return
	}
	results := map[mirrorResult]int{}
	if all {
		s.line("%s: %s", what, strings.Join(picked, ", "))
		for _, v := range picked {
			if ctx.Err() != nil {
				return
			}
			results[s.mirrorVersion(ctx, r.Namespace, r.Name, v)]++
		}
	} else {
		top := len(picked) - 1
		for top > 0 && s.done[r.fqcn()+" "+picked[top]] == mirrorSkipped {
			top--
		}
		s.line("%s: %s", what, picked[top])
		for i := top; i >= 0 && results[mirrorKept]+results[mirrorFailed] == 0; i-- {
			if ctx.Err() != nil {
				return
			}
			results[s.mirrorVersion(ctx, r.Namespace, r.Name, picked[i])]++
		}
	}
	if results[mirrorKept] > 0 || results[mirrorFailed] > 0 {
		return
	}
	held, err := s.mirror.versionsHeld(ctx, r.Namespace, r.Name)
	if err != nil {
		s.problem("%s: %v", what, err)
		return
	}
	if len(remoteCandidates(held, r.Spec)) == 0 {
		s.problem("%s is unmet: no version in its range has a signature that verifies with the keys of %s.", what, s.mirror.keysName)
	}
}

func (s *remoteSync) mirrorVersion(ctx context.Context, ns, name, ver string) mirrorResult {
	key := ns + "." + name + " " + ver
	if r, ok := s.done[key]; ok {
		return r
	}
	s.done[key] = mirrorFailed
	detail, err := s.client.Version(ctx, ns, name, ver)
	if err != nil {
		s.problem("Reading %s: %v", key, err)
		return mirrorFailed
	}
	var sigs []string
	for _, sig := range detail.Signatures {
		sigs = append(sigs, sig.Signature)
	}
	have, err := s.mirror.existing(ctx, ns, name, ver)
	if err != nil {
		s.problem("%s: %v", key, err)
		return mirrorFailed
	}
	var outcome mirrorOutcome
	if have != nil {
		if !strings.EqualFold(have.sha256, detail.Artifact.SHA256) {
			s.problem("%s: the hub has another artifact of it (SHA-256 %s, the server's %s), which it keeps: versions don't change.",
				key, have.sha256, strings.ToLower(detail.Artifact.SHA256))
			return mirrorFailed
		}
		outcome, err = s.mirror.addExisting(ctx, have, sigs)
	} else {
		var data []byte
		if data, err = s.client.Download(ctx, detail, s.limit); err != nil {
			s.problem("Downloading %s: %v", key, err)
			return mirrorFailed
		}
		outcome, err = s.mirror.addArtifact(ctx, data, sigs, &CollectionArtifact{Namespace: ns, Name: name, Version: ver})
	}
	for _, w := range outcome.warnings {
		s.line("  WARNING: %s %s", key, w)
	}
	if err != nil {
		s.problem("%s: %v", key, err)
		return mirrorFailed
	}
	s.sigs += outcome.signatures
	switch {
	case outcome.skipped != "":
		s.skip(key)
		s.done[key] = mirrorSkipped
		s.line("  %s: skipped, %s", key, outcome.skipped)
		return mirrorSkipped
	case outcome.added:
		s.added++
	default:
		s.present++
	}
	s.done[key] = mirrorKept
	s.line("  %s: %s", key, outcome.describe())
	if !s.deps {
		return mirrorKept
	}
	names := make([]string, 0, len(detail.Metadata.Dependencies))
	for dep := range detail.Metadata.Dependencies {
		names = append(names, dep)
	}
	sort.Strings(names)
	for _, dep := range names {
		spec := strings.TrimSpace(detail.Metadata.Dependencies[dep])
		if spec == "" {
			spec = "*"
		}
		dns, dname, ok := strings.Cut(dep, ".")
		if !ok || !validGalaxyName(dns) || !validGalaxyName(dname) || !validVersionSpec(spec) {
			s.problem("%s: its dependency %s %s can't be read.", key, dep, spec)
			continue
		}
		s.require(ctx, remoteRequirement{dns, dname, spec}, false, key)
	}
	return mirrorKept
}
