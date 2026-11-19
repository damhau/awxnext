package resources

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	"github.com/damhau/awxnext/internal/agent"
	"github.com/damhau/awxnext/internal/api"
	"github.com/damhau/awxnext/internal/hubdocs"
	"github.com/damhau/awxnext/internal/jobexec"
	"github.com/damhau/awxnext/internal/runnercmd"
)

// The docs of a collection version's modules and plugins, read as
// galaxy-importer does, with ansible-doc, but in the background after the
// import: the upload doesn't wait for an execution environment's image.
// ansible-doc imports some of the collection's Python code (doc fragments,
// filter and test plugins), so it runs where jobs run on this node, in a
// container or as a job user, else in a pod when the node is in a
// Kubernetes cluster, and as the server's own user only when
// HUB_COLLECTION_DOCS allows it.

const (
	docsTimeout   = 10 * time.Minute
	docsStale     = time.Hour
	docsMaxOutput = 64 << 20
	readmeMax     = 512 << 10

	docsIsolated   = "isolated"
	docsServerUser = "server_user"
	docsOff        = "off"
)

// CollectionDocs reads the docs of the versions waiting for them, one at a
// time on each node, the newest first.
type CollectionDocs struct {
	app  *api.App
	node string
	kick chan struct{}
	Poll time.Duration
}

// Docs reads collection docs on this node; kickDocs wakes it.
var Docs *CollectionDocs

func NewCollectionDocs(app *api.App, node string) *CollectionDocs {
	return &CollectionDocs{app: app, node: node, kick: make(chan struct{}, 1), Poll: time.Minute}
}

func kickDocs() {
	if d := Docs; d != nil {
		select {
		case d.kick <- struct{}{}:
		default:
		}
	}
}

func (d *CollectionDocs) Run(ctx context.Context) {
	t := time.NewTicker(d.Poll)
	defer t.Stop()
	for {
		for ctx.Err() == nil {
			id, err := d.claim(ctx)
			if err != nil {
				if ctx.Err() == nil {
					slog.Warn("collection docs: claiming a version", "err", err)
				}
				break
			}
			if id == 0 {
				break
			}
			d.read(ctx, id)
		}
		select {
		case <-ctx.Done():
			return
		case <-d.kick:
		case <-t.C:
		}
	}
}

func (d *CollectionDocs) claim(ctx context.Context) (int64, error) {
	if _, err := d.app.DB.Exec(ctx, `UPDATE collection_versions SET docs_state = 'pending'
		WHERE docs_state = 'reading' AND docs_started < now() - make_interval(secs => $1)`, docsStale.Seconds()); err != nil {
		return 0, err
	}
	var id int64
	err := d.app.DB.QueryRow(ctx, `UPDATE collection_versions SET docs_state = 'reading', docs_started = now(), docs_message = ''
		WHERE id = (SELECT id FROM collection_versions WHERE docs_state = 'pending' ORDER BY id DESC LIMIT 1 FOR UPDATE SKIP LOCKED)
		RETURNING id`).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, nil
	}
	return id, err
}

type pluginDoc struct {
	typ, name, short, err string
	doc                   json.RawMessage
}

type docsOutcome struct {
	state, message string
	plugins        []pluginDoc
}

func (d *CollectionDocs) read(ctx context.Context, id int64) {
	var ns, name, storage, location string
	var org int64
	var meta map[string]any
	err := d.app.DB.QueryRow(ctx, `SELECT t.name, c.name, v.storage, v.location, t.organization_id, v.metadata FROM collection_versions v
		JOIN collections c ON c.id = v.collection_id JOIN collection_namespaces t ON t.id = c.namespace_id WHERE v.id = $1`, id).
		Scan(&ns, &name, &storage, &location, &org, &meta)
	if errors.Is(err, pgx.ErrNoRows) {
		return
	}
	var data []byte
	if err == nil {
		data, err = loadArtifact(ctx, d.app, d.app.DB, id, storage, location)
	}
	if err != nil {
		if ctx.Err() == nil {
			slog.Warn("collection docs: loading the artifact", "version", id, "err", err)
			d.store(ctx, id, "", "", docsOutcome{state: "failed", message: "The artifact couldn't be loaded: " + err.Error()})
		}
		return
	}
	readmeName, readme := artifactReadme(data, str(meta["readme"]))
	out := d.docs(ctx, id, ns+"."+name, org, data)
	if ctx.Err() != nil {
		return
	}
	d.store(ctx, id, readmeName, readme, out)
}

// store saves what was read, unless someone asked to read the version
// again meanwhile.
func (d *CollectionDocs) store(ctx context.Context, id int64, readmeName, readme string, out docsOutcome) {
	err := func() error {
		tx, err := d.app.DB.Begin(ctx)
		if err != nil {
			return err
		}
		defer tx.Rollback(ctx)
		tag, err := tx.Exec(ctx, `UPDATE collection_versions SET readme_name = $2, readme = $3, docs_state = $4, docs_message = $5, docs_read = now()
			WHERE id = $1 AND docs_state = 'reading'`, id, readmeName, readme, out.state, out.message)
		if err != nil || tag.RowsAffected() == 0 {
			return err
		}
		if _, err := tx.Exec(ctx, "DELETE FROM collection_plugin_docs WHERE version_id = $1", id); err != nil {
			return err
		}
		rows := make([][]any, 0, len(out.plugins))
		for _, p := range out.plugins {
			var doc any
			if len(p.doc) > 0 {
				doc = p.doc
			}
			rows = append(rows, []any{id, p.typ, p.name, p.short, p.err, doc})
		}
		if _, err := tx.CopyFrom(ctx, pgx.Identifier{"collection_plugin_docs"},
			[]string{"version_id", "plugin_type", "name", "short_description", "error", "doc"}, pgx.CopyFromRows(rows)); err != nil {
			return err
		}
		level := "INFO"
		if out.state != "read" {
			level = "WARNING"
		}
		msg, _ := json.Marshal([]ImportMessage{{Level: level, Message: "Docs: " + out.message, Time: float64(time.Now().UnixMicro()) / 1e6}})
		if _, err := tx.Exec(ctx, `UPDATE collection_imports SET messages = coalesce(messages, '[]'::jsonb) || $2::jsonb, modified = now()
			WHERE id = (SELECT id FROM collection_imports WHERE collection_version_id = $1 ORDER BY id DESC LIMIT 1)`, id, msg); err != nil {
			return err
		}
		return tx.Commit(ctx)
	}()
	if err != nil && ctx.Err() == nil {
		slog.Warn("collection docs: storing them", "version", id, "err", err)
	}
}

// artifactReadme is the README collection_info names, read from the
// artifact up to readmeMax.
func artifactReadme(data []byte, name string) (string, string) {
	if name == "" {
		name = "README.md"
	}
	want, ok := memberPath(name)
	if !ok {
		return "", ""
	}
	gz, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return "", ""
	}
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if err != nil {
			return "", ""
		}
		if p, ok := memberPath(h.Name); !ok || p != want || h.Typeflag != tar.TypeReg {
			continue
		}
		b, _ := io.ReadAll(io.LimitReader(tr, readmeMax))
		return want, strings.ToValidUTF8(string(b), "�")
	}
}

// docsPlace is where ansible-doc runs: a command and its environment, or a
// pod.
type docsPlace struct {
	args, env []string
	cleanup   func()
	stop      func(cmd *exec.Cmd)
	pod       *agent.PodRequest
	where     string
	group     bool
}

// DocsPodCredential connects docs pods to a cluster other than the one this
// node runs in, for tests: without it, pods are made only in this node's
// cluster, with its service account.
var DocsPodCredential *agent.KubeCredential

func (d *CollectionDocs) place(ctx context.Context, id, org int64, fqcn string) (*docsPlace, string, error) {
	mode := d.app.Settings.String(ctx, "HUB_COLLECTION_DOCS")
	if mode == docsOff {
		return nil, "Reading them is off (HUB_COLLECTION_DOCS).", nil
	}
	argv := hubdocs.Argv(fqcn)
	switch rt := d.app.Cfg.ContainerRuntime; rt {
	case "podman", "docker":
		if _, err := exec.LookPath(rt); err != nil {
			return nil, fmt.Sprintf("%s isn't installed on node %s.", rt, d.node), nil
		}
		b := make([]byte, 4)
		rand.Read(b)
		spec := runnercmd.Spec{Runtime: rt, Name: fmt.Sprintf("awxnext_docs_%d_%s", id, hex.EncodeToString(b)), Network: "none"}
		if err := d.docsImage(ctx, &spec, org); err != nil {
			return nil, "", err
		}
		args, cleanup, err := runnercmd.Run(spec, argv)
		if err != nil {
			return nil, "", err
		}
		return &docsPlace{args: args, env: runnercmd.Env(), cleanup: cleanup, stop: func(cmd *exec.Cmd) { runnercmd.Kill(spec, cmd) },
			where: "in the execution environment " + spec.Image}, "", nil
	}
	if iso := jobexec.Default; iso != nil {
		u, err := iso.Acquire(ctx)
		if err != nil {
			return nil, "", err
		}
		dir := iso.Dir(fmt.Sprintf("docs-%d", id))
		return &docsPlace{args: iso.Command(u, dir, argv), env: jobexec.Env(runnercmd.Env(), u, dir), cleanup: func() { iso.Release(u) },
			stop: func(cmd *exec.Cmd) {
				// The helper stops the job user's processes on SIGTERM.
				cmd.Process.Signal(syscall.SIGTERM)
				p := cmd.Process
				time.AfterFunc(30*time.Second, func() { p.Kill() })
			},
			where: fmt.Sprintf("on node %s as job user %s", d.node, u.Name)}, "", nil
	}
	if agent.Default != nil && (DocsPodCredential != nil || agent.InCluster()) {
		var spec runnercmd.Spec
		if err := d.docsImage(ctx, &spec, org); err != nil {
			return nil, "", err
		}
		req := &agent.PodRequest{DocsVersionID: id, Credential: DocsPodCredential, ServiceURL: d.app.Cfg.ServiceURL, ShimImage: d.app.Cfg.ShimImage,
			Spec: agent.WorkSpec{Ident: fmt.Sprintf("docs-%d", id), Kind: agent.KindDocs, Collection: fqcn, Image: spec.Image, Pull: spec.Pull,
				RegistryHost: spec.RegistryHost, RegistryUser: spec.RegistryUser, RegistryPassword: spec.RegistryPassword}}
		d.app.DB.QueryRow(ctx, "SELECT value #>> '{}' FROM settings WHERE key = 'INSTALL_UUID'").Scan(&req.InstallID)
		return &docsPlace{pod: req, cleanup: func() {}, where: "in a pod of the execution environment " + spec.Image}, "", nil
	}
	if mode != docsServerUser {
		return nil, fmt.Sprintf("Node %s runs jobs neither in containers nor as job users, isn't in a Kubernetes cluster, and HUB_COLLECTION_DOCS doesn't let ansible-doc run as the server's user.", d.node), nil
	}
	tmp, err := os.MkdirTemp("", "awxnext-docs-")
	if err != nil {
		return nil, "", err
	}
	env := []string{"TMPDIR=" + tmp}
	for _, e := range runnercmd.Env() {
		if !strings.HasPrefix(e, "TMPDIR=") {
			env = append(env, e)
		}
	}
	return &docsPlace{args: argv, env: env, cleanup: func() { os.RemoveAll(tmp) }, group: true,
		stop:  func(cmd *exec.Cmd) { syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) },
		where: fmt.Sprintf("on node %s as the server's user", d.node)}, "", nil
}

// docsImage is the image of the organization's default execution
// environment, else the default one's.
func (d *CollectionDocs) docsImage(ctx context.Context, spec *runnercmd.Spec, org int64) error {
	spec.Image = d.app.Cfg.DefaultEEImage
	var ee, credID *int64
	d.app.DB.QueryRow(ctx, "SELECT default_environment_id FROM organizations WHERE id = $1", org).Scan(&ee)
	if ee == nil {
		return nil
	}
	if err := d.app.DB.QueryRow(ctx, "SELECT image, pull, credential_id FROM execution_environments WHERE id = $1", *ee).
		Scan(&spec.Image, &spec.Pull, &credID); err != nil || credID == nil {
		return nil
	}
	rc, err := ResolveCredential(ctx, d.app, *credID)
	if err != nil {
		return err
	}
	spec.RegistryHost, _ = rc.Inputs["host"].(string)
	spec.RegistryUser, _ = rc.Inputs["username"].(string)
	spec.RegistryPassword, _ = rc.Inputs["password"].(string)
	return nil
}

type cappedBuffer struct {
	bytes.Buffer
	max  int
	over bool
}

func (b *cappedBuffer) Write(p []byte) (int, error) {
	room := b.max - b.Len()
	if len(p) > room {
		b.over = true
		p = p[:max(room, 0)]
	}
	b.Buffer.Write(p)
	return len(p), nil
}

// docsRun is how ansible-doc's run ended.
type docsRun struct {
	stdout             *cappedBuffer
	stderr             string
	code               int
	timedOut, canceled bool
}

// run runs the script as a command on this node.
func (p *docsPlace) run(ctx context.Context, data []byte) (docsRun, error) {
	cmd := exec.Command(p.args[0], p.args[1:]...)
	cmd.Env = p.env
	cmd.Stdin = bytes.NewReader(data)
	if p.group {
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	}
	r := docsRun{stdout: &cappedBuffer{max: docsMaxOutput}, code: -1}
	stderr := &cappedBuffer{max: 8 << 10}
	cmd.Stdout, cmd.Stderr = r.stdout, stderr
	if err := cmd.Start(); err != nil {
		return r, err
	}
	done := make(chan struct{})
	go func() {
		cmd.Wait()
		close(done)
	}()
	timer := time.NewTimer(docsTimeout)
	defer timer.Stop()
	select {
	case <-done:
	case <-timer.C:
		r.timedOut = true
		p.stop(cmd)
		<-done
	case <-ctx.Done():
		r.canceled = true
		p.stop(cmd)
		<-done
	}
	r.stderr = strings.TrimSpace(stderr.String())
	if cmd.ProcessState != nil {
		r.code = cmd.ProcessState.ExitCode()
	}
	return r, nil
}

// runPod runs the script in a pod, as a container group runs a job: the
// pod's shim streams the artifact in and the docs out, and the pod is
// deleted once it ends. The timeout counts from the pod's creation, as a
// container's counts its image's pull.
func (p *docsPlace) runPod(ctx context.Context, data []byte) (docsRun, error) {
	r := docsRun{stdout: &cappedBuffer{max: docsMaxOutput}, code: -1}
	limit, cancel := context.WithTimeout(ctx, docsTimeout)
	defer cancel()
	w, err := agent.Default.StartPod(limit, *p.pod)
	if err != nil {
		r.canceled = ctx.Err() != nil
		r.timedOut = !r.canceled && limit.Err() != nil
		if r.canceled || r.timedOut {
			return r, nil
		}
		return r, err
	}
	go func() {
		w.Stdin().Write(data)
		w.Stdin().Close()
	}()
	copied := make(chan struct{})
	go func() {
		io.Copy(r.stdout, w.Stdout())
		// Past the limit, the rest is drained so the worker isn't blocked.
		io.Copy(io.Discard, w.Stdout())
		close(copied)
	}()
	done := make(chan string, 1)
	go func() {
		diag, _ := w.Wait()
		done <- diag
	}()
	select {
	case r.stderr = <-done:
	case <-limit.Done():
		r.canceled = ctx.Err() != nil
		r.timedOut = !r.canceled
		w.Kill()
		r.stderr = <-done
	}
	<-copied
	r.stderr = strings.TrimSpace(r.stderr)
	if e, ok := w.(interface{ ExitCode() int }); ok {
		r.code = e.ExitCode()
	}
	return r, nil
}

func (d *CollectionDocs) docs(ctx context.Context, id int64, fqcn string, org int64, data []byte) docsOutcome {
	p, refused, err := d.place(ctx, id, org, fqcn)
	if err != nil {
		return docsOutcome{state: "failed", message: "ansible-doc couldn't start: " + err.Error()}
	}
	if p == nil {
		return docsOutcome{state: "none", message: "Not read. " + refused}
	}
	defer p.cleanup()
	run := p.run
	if p.pod != nil {
		run = p.runPod
	}
	r, err := run(ctx, data)
	if err != nil {
		return docsOutcome{state: "failed", message: fmt.Sprintf("ansible-doc couldn't start %s: %v", p.where, err)}
	}
	if r.canceled {
		return docsOutcome{}
	}
	switch {
	case r.timedOut:
		return docsOutcome{state: "failed", message: fmt.Sprintf("ansible-doc didn't finish %s within %s.", p.where, docsTimeout)}
	case r.code == hubdocs.NoAnsibleDoc:
		return docsOutcome{state: "none", message: "Not read: ansible-doc isn't installed " + p.where + "."}
	case r.code != 0:
		tail := r.stderr
		if tail == "" {
			tail = fmt.Sprintf("exit code %d", r.code)
		}
		return docsOutcome{state: "failed", message: fmt.Sprintf("ansible-doc failed %s: %s", p.where, tail)}
	case r.stdout.over:
		return docsOutcome{state: "failed", message: fmt.Sprintf("ansible-doc's output is larger than %s.", humanBytes(docsMaxOutput))}
	}
	plugins, err := parseDocs(r.stdout.Bytes(), fqcn)
	if err != nil {
		return docsOutcome{state: "failed", message: "ansible-doc's output can't be read: " + err.Error()}
	}
	bad := 0
	for _, pd := range plugins {
		if pd.err != "" {
			bad++
		}
	}
	msg := fmt.Sprintf("ansible-doc read %d modules and plugins %s", len(plugins)-bad, p.where)
	if bad > 0 {
		msg += fmt.Sprintf("; %d have docs it couldn't read", bad)
	}
	return docsOutcome{state: "read", message: msg + ".", plugins: plugins}
}

// parseDocs reads the script's output: ansible-doc's metadata dump by
// plugin type, then fully qualified name.
func parseDocs(out []byte, fqcn string) ([]pluginDoc, error) {
	var dump map[string]map[string]json.RawMessage
	if err := json.Unmarshal(out, &dump); err != nil {
		return nil, err
	}
	var list []pluginDoc
	for typ, plugins := range dump {
		for full, raw := range plugins {
			var entry struct {
				Error string `json:"error"`
				Doc   *struct {
					ShortDescription any `json:"short_description"`
				} `json:"doc"`
				EntryPoints map[string]struct {
					ShortDescription any `json:"short_description"`
				} `json:"entry_points"`
			}
			if err := json.Unmarshal(raw, &entry); err != nil {
				return nil, fmt.Errorf("%s %s: %w", typ, full, err)
			}
			pd := pluginDoc{typ: typ, name: strings.TrimPrefix(full, fqcn+"."), err: entry.Error}
			switch {
			case entry.Doc != nil:
				pd.short = shortText(entry.Doc.ShortDescription)
			case entry.EntryPoints != nil:
				pd.short = shortText(entry.EntryPoints["main"].ShortDescription)
			}
			if entry.Error == "" {
				pd.doc = raw
			}
			list = append(list, pd)
		}
	}
	sort.Slice(list, func(i, j int) bool {
		if list[i].typ != list[j].typ {
			return list[i].typ < list[j].typ
		}
		return list[i].name < list[j].name
	})
	return list, nil
}

func shortText(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case []any:
		var parts []string
		for _, p := range t {
			parts = append(parts, fmt.Sprint(p))
		}
		return strings.Join(parts, " ")
	}
	return ""
}

// versionDocs is a version's README and the index of its docs, or with
// type and name, one module's or plugin's docs.
func versionDocs(c *Ctx, w http.ResponseWriter, r *http.Request, obj Obj) {
	id, _ := api.ID(obj["id"])
	q := r.URL.Query()
	if typ, name := q.Get("type"), q.Get("name"); typ != "" || name != "" {
		var short, failure string
		var doc any
		err := c.DB().QueryRow(c, `SELECT short_description, error, doc FROM collection_plugin_docs WHERE version_id = $1 AND plugin_type = $2 AND name = $3`,
			id, typ, name).Scan(&short, &failure, &doc)
		if errors.Is(err, pgx.ErrNoRows) {
			api.WriteError(w, api.ErrNotFound)
			return
		}
		if err != nil {
			api.WriteError(w, err)
			return
		}
		api.WriteJSON(w, 200, Obj{"type": typ, "name": name, "short_description": short, "error": failure, "doc": doc})
		return
	}
	var readmeName, readme, state, message string
	var read *time.Time
	if err := c.DB().QueryRow(c, "SELECT readme_name, readme, docs_state, docs_message, docs_read FROM collection_versions WHERE id = $1", id).
		Scan(&readmeName, &readme, &state, &message, &read); err != nil {
		api.WriteError(w, err)
		return
	}
	rows, err := c.DB().Query(c, `SELECT plugin_type, name, short_description, error FROM collection_plugin_docs WHERE version_id = $1
		ORDER BY plugin_type, name`, id)
	if err != nil {
		api.WriteError(w, err)
		return
	}
	contents, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (Obj, error) {
		var typ, name, short, failure string
		err := row.Scan(&typ, &name, &short, &failure)
		return Obj{"type": typ, "name": name, "short_description": short, "error": failure}, err
	})
	if err != nil {
		api.WriteError(w, err)
		return
	}
	api.WriteJSON(w, 200, Obj{"state": state, "message": message, "read": read,
		"readme": Obj{"name": readmeName, "text": readme}, "contents": contents})
}

// readVersionDocs reads a version's docs again.
func readVersionDocs(c *Ctx, w http.ResponseWriter, r *http.Request, obj Obj) {
	id, _ := api.ID(obj["id"])
	if _, err := c.DB().Exec(c, "UPDATE collection_versions SET docs_state = 'pending', docs_message = '' WHERE id = $1", id); err != nil {
		api.WriteError(w, err)
		return
	}
	kickDocs()
	api.WriteJSON(w, 202, Obj{"state": "pending"})
}

// galaxyDocsBlob is galaxy_ng's docs-blob of a version: its README, and
// its modules' and plugins' docs as ansible-doc read them.
func galaxyDocsBlob(g *galaxyCall) {
	_, v, err := g.version("t.name = $1 AND c.name = $2 AND v.version = $3",
		chi.URLParam(g.r, "namespace"), chi.URLParam(g.r, "name"), chi.URLParam(g.r, "version"))
	if err != nil {
		g.fail(err)
		return
	}
	if v == nil {
		galaxyNotFound(g.w)
		return
	}
	var readmeName, readme string
	if err := g.DB().QueryRow(g, "SELECT readme_name, readme FROM collection_versions WHERE id = $1", v.id).Scan(&readmeName, &readme); err != nil {
		g.fail(err)
		return
	}
	rows, err := g.DB().Query(g, `SELECT plugin_type, name, doc FROM collection_plugin_docs WHERE version_id = $1 AND error = ''
		ORDER BY plugin_type, name`, v.id)
	if err != nil {
		g.fail(err)
		return
	}
	contents, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (Obj, error) {
		var typ, name string
		var doc map[string]any
		err := row.Scan(&typ, &name, &doc)
		ds := Obj{}
		for _, k := range []string{"doc", "examples", "return", "metadata"} {
			if v, ok := doc[k]; ok {
				ds[k] = v
			}
		}
		if typ == "role" {
			ds = Obj{"entry_points": doc["entry_points"]}
		}
		return Obj{"content_name": name, "content_type": typ, "doc_strings": ds, "readme_file": nil, "readme_html": nil}, err
	})
	if err != nil {
		g.fail(err)
		return
	}
	var collReadme any
	if readmeName != "" {
		collReadme = Obj{"name": readmeName, "html": "<pre>" + html.EscapeString(readme) + "</pre>"}
	}
	api.WriteJSON(g.w, 200, Obj{"docs_blob": Obj{"collection_readme": collReadme, "documentation_files": []any{}, "contents": contents}})
}
