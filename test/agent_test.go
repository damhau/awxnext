package test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/damhau/awxnext/internal/agent"
	"github.com/damhau/awxnext/internal/resources"
)

func waitInstance(t *testing.T, a *client, id int64, state string) resp {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		r := a.get(fmt.Sprintf("instances/%d/", id))
		if r.Body["node_state"] == state {
			return r
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("instance %d never became %s", id, state)
	return resp{}
}

func TestRemoteAgent(t *testing.T) {
	f := newJobFixture(t)
	a := f.a
	host := uniq("exec")
	inst := must(t, a.post("instances/", map[string]any{"hostname": host, "node_type": "execution"}), 201).id()
	user, _ := newUser(t, a)
	must(t, user.post(fmt.Sprintf("instances/%d/agent_token/", inst), nil), 403)
	tok := must(t, a.post(fmt.Sprintf("instances/%d/agent_token/", inst), nil), 201).Body["token"].(string)
	ig := must(t, a.post("instance_groups/", map[string]any{"name": uniq("remote")}), 201).id()
	must(t, a.post(fmt.Sprintf("instance_groups/%d/instances/", ig), map[string]any{"id": inst}), 204)

	// A wrong token is refused.
	ctxBad, cancelBad := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancelBad()
	go agent.RunAgent(ctxBad, agent.Config{URL: srv.URL, Token: "nope", Runtime: "none", WorkDir: t.TempDir()})

	ctx, cancel := context.WithCancel(context.Background())
	stopped := make(chan struct{})
	go func() {
		agent.RunAgent(ctx, agent.Config{URL: srv.URL, Token: tok, Runtime: "none", RunnerCommand: "ansible-runner", WorkDir: t.TempDir(), Capacity: 10, Version: "test"})
		close(stopped)
	}()
	defer func() { cancel(); <-stopped }()
	r := waitInstance(t, a, inst, "ready")
	if r.Body["capacity"] != float64(10) || r.Body["agent_runtime"] != "none" || r.Body["version"] != "test" {
		t.Fatalf("registered instance: %v", r.Body)
	}

	jt := f.template(t, "site.yml", nil)
	must(t, a.post(fmt.Sprintf("job_templates/%d/instance_groups/", jt), map[string]any{"id": ig}), 204)
	job := f.launch(t, a, jt, nil)
	mustStatus(t, f, job, "successful")
	if job.Body["execution_node"] != host || job.Body["controller_node"] != "test-node" || job.Body["instance_group"] != float64(ig) {
		t.Fatalf("placement: exec=%v ctl=%v ig=%v", job.Body["execution_node"], job.Body["controller_node"], job.Body["instance_group"])
	}
	if out := f.stdout(t, job.id()); !strings.Contains(out, "hello nobody from h1") {
		t.Fatalf("remote stdout:\n%s", out)
	}
	if r := a.get(fmt.Sprintf("jobs/%d/job_host_summaries/", job.id())); r.count() != 3 {
		t.Fatalf("host summaries: %d", r.count())
	}

	// Cancel reaches the remote worker.
	slow := f.template(t, "slow.yml", nil)
	must(t, a.post(fmt.Sprintf("job_templates/%d/instance_groups/", slow), map[string]any{"id": ig}), 204)
	sj := must(t, a.post(fmt.Sprintf("job_templates/%d/launch/", slow), nil), 201).id()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if a.get(fmt.Sprintf("jobs/%d/", sj)).Body["status"] == "running" && strings.Contains(f.stdout(t, sj), "pause") {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	start := time.Now()
	must(t, a.post(fmt.Sprintf("jobs/%d/cancel/", sj), nil), 202)
	if r := waitJob(t, a, fmt.Sprintf("jobs/%d/", sj)); r.Body["status"] != "canceled" || time.Since(start) > 20*time.Second {
		t.Fatalf("remote cancel: %v after %v", r.Body["status"], time.Since(start))
	}

	cancel()
	<-stopped
	waitInstance(t, a, inst, "unavailable")
}

// fakeKube is a minimal Kubernetes API that "runs" job pods by starting the shim locally.
type fakeKube struct {
	t       *testing.T
	mu      sync.Mutex
	pods    map[string]map[string]any
	deleted []string
	dir     string
}

func (k *fakeKube) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Authorization") != "Bearer kube-token" {
		w.WriteHeader(401)
		return
	}
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/") // api v1 namespaces NS pods [NAME]
	if len(parts) < 5 {
		w.WriteHeader(404)
		return
	}
	switch {
	case r.Method == "POST" && parts[4] == "pods":
		b, _ := io.ReadAll(r.Body)
		var pod map[string]any
		json.Unmarshal(b, &pod)
		meta := pod["metadata"].(map[string]any)
		name := meta["generateName"].(string) + "x1"
		k.mu.Lock()
		k.pods[name] = pod
		k.mu.Unlock()
		worker := pod["spec"].(map[string]any)["containers"].([]any)[0].(map[string]any)
		cmd := worker["command"].([]any)
		url := cmd[len(cmd)-1].(string)
		var token string
		for _, e := range worker["env"].([]any) {
			if em := e.(map[string]any); em["name"] == "AWXNEXT_SHIM_TOKEN" {
				token = em["value"].(string)
			}
		}
		go agent.RunShim(context.Background(), agent.Config{Runtime: "none", ExactWorkDir: true, WorkDir: filepath.Join(k.dir, name),
			RunnerCommand: "ansible-runner", Token: token}, url)
		w.WriteHeader(201)
		json.NewEncoder(w).Encode(map[string]any{"metadata": map[string]any{"name": name}})
	case r.Method == "POST" && parts[4] == "secrets":
		w.WriteHeader(201)
		w.Write([]byte(`{}`))
	case r.Method == "GET" && len(parts) == 6:
		w.Write([]byte(`{"status": {"phase": "Running"}}`))
	case r.Method == "DELETE":
		k.mu.Lock()
		k.deleted = append(k.deleted, parts[len(parts)-1])
		k.mu.Unlock()
		w.Write([]byte(`{}`))
	default:
		w.WriteHeader(404)
	}
}

func TestContainerGroup(t *testing.T) {
	f := newJobFixture(t)
	a := f.a
	k := &fakeKube{t: t, pods: map[string]map[string]any{}, dir: t.TempDir()}
	kube := httptest.NewServer(k)
	defer kube.Close()
	cred := must(t, a.post("credentials/", map[string]any{"name": uniq("k8s"), "organization": f.org, "credential_type": credType(t, a, "kubernetes_bearer_token"),
		"inputs": map[string]any{"host": kube.URL, "bearer_token": "kube-token", "verify_ssl": false}}), 201).id()
	ig := must(t, a.post("instance_groups/", map[string]any{"name": uniq("pods"), "is_container_group": true, "credential": cred,
		"pod_spec_override": "apiVersion: v1\nkind: Pod\nmetadata:\n  namespace: jobs\n  labels:\n    team: ops\nspec:\n  containers:\n    - image: ignored\n      resources:\n        limits:\n          memory: 1Gi\n"}), 201).id()
	jt := f.template(t, "site.yml", nil)
	must(t, a.post(fmt.Sprintf("job_templates/%d/instance_groups/", jt), map[string]any{"id": ig}), 204)
	job := f.launch(t, a, jt, nil)
	mustStatus(t, f, job, "successful")
	if out := f.stdout(t, job.id()); !strings.Contains(out, "hello nobody from h2") {
		t.Fatalf("pod stdout:\n%s", out)
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	if len(k.pods) != 1 || len(k.deleted) != 1 {
		t.Fatalf("pods created %d, deleted %v", len(k.pods), k.deleted)
	}
	for _, pod := range k.pods {
		meta := pod["metadata"].(map[string]any)
		labels := meta["labels"].(map[string]any)
		spec := pod["spec"].(map[string]any)
		worker := spec["containers"].([]any)[0].(map[string]any)
		init := spec["initContainers"].([]any)[0].(map[string]any)
		if meta["namespace"] != "jobs" || labels["team"] != "ops" || labels["ansible-awx-job-id"] != fmt.Sprint(job.id()) ||
			worker["image"] != "quay.io/ansible/awx-ee:latest" || spec["restartPolicy"] != "Never" ||
			worker["resources"].(map[string]any)["limits"] == nil || !strings.HasSuffix(fmt.Sprint(init["image"]), "awxnext:latest") {
			t.Fatalf("pod spec: %v", pod)
		}
	}
}

// /instances/N/health_check/: an execution node's agent reports its
// resources again, this control node probes itself, and an agent that does
// not answer marks its node unavailable.
func TestInstanceHealthCheck(t *testing.T) {
	a := adminClient(t)
	resources.HealthCheckTimeout = time.Second
	t.Cleanup(func() { resources.HealthCheckTimeout = 15 * time.Second })
	waitChecked := func(id int64) resp {
		t.Helper()
		deadline := time.Now().Add(15 * time.Second)
		for time.Now().Before(deadline) {
			r := must(t, a.get(fmt.Sprintf("instances/%d/", id)), 200)
			if r.Body["health_check_pending"] == false {
				return r
			}
			time.Sleep(50 * time.Millisecond)
		}
		t.Fatalf("health check of instance %d never ended", id)
		return resp{}
	}

	// This control node.
	ctl := must(t, a.get("instances/?hostname=test-node"), 200).results()[0]
	ctlID := int64(ctl["id"].(float64))
	if r := must(t, a.post(fmt.Sprintf("instances/%d/health_check/", ctlID), nil), 200); r.Body["msg"] != "Health check is running for test-node." {
		t.Errorf("control node: %s", r.Raw)
	}
	r := must(t, a.get(fmt.Sprintf("instances/%d/health_check/", ctlID)), 200)
	if r.Body["errors"] != "" || r.Body["cpu"].(float64) < 1 || r.Body["memory"].(float64) <= 0 || r.Body["last_health_check"] == nil || len(r.Body) != 11 {
		t.Errorf("control node result: %s", r.Raw)
	}

	// An execution node whose agent answers.
	inst := must(t, a.post("instances/", map[string]any{"hostname": uniq("exec"), "node_type": "execution"}), 201).id()
	tok := must(t, a.post(fmt.Sprintf("instances/%d/agent_token/", inst), nil), 201).Body["token"].(string)
	ctx, cancel := context.WithCancel(context.Background())
	stopped := make(chan struct{})
	go func() {
		agent.RunAgent(ctx, agent.Config{URL: srv.URL, Token: tok, Runtime: "none", WorkDir: t.TempDir(), Capacity: 7, Version: "test"})
		close(stopped)
	}()
	waitInstance(t, a, inst, "ready")
	app.DB.Exec(t.Context(), "UPDATE instances SET capacity = 0, errors = 'stale', last_health_check = now() - interval '1 hour' WHERE id = $1", inst)
	must(t, a.post(fmt.Sprintf("instances/%d/health_check/", inst), nil), 200)
	if r := waitChecked(inst); r.Body["errors"] != "" || r.Body["capacity"] != float64(7) {
		t.Errorf("answered health check: %v %v", r.Body["errors"], r.Body["capacity"])
	}

	// The same node once its agent is gone.
	cancel()
	<-stopped
	waitInstance(t, a, inst, "unavailable")
	must(t, a.post(fmt.Sprintf("instances/%d/health_check/", inst), nil), 200)
	if r := must(t, a.post(fmt.Sprintf("instances/%d/health_check/", inst), nil), 200); !strings.Contains(fmt.Sprint(r.Body["msg"]), "already in progress") {
		t.Errorf("second check while pending: %s", r.Raw)
	}
	if r := waitChecked(inst); r.Body["capacity"] != float64(0) || !strings.Contains(fmt.Sprint(r.Body["errors"]), "did not answer") {
		t.Errorf("unanswered health check: %v %v", r.Body["errors"], r.Body["capacity"])
	}

	hop := must(t, a.post("instances/", map[string]any{"hostname": uniq("hop"), "node_type": "hop"}), 201).id()
	must(t, a.post(fmt.Sprintf("instances/%d/health_check/", hop), nil), 400)
	auditor, auditorID := newUser(t, a)
	must(t, a.patch(fmt.Sprintf("users/%d/", auditorID), map[string]any{"is_system_auditor": true}), 200)
	must(t, auditor.get(fmt.Sprintf("instances/%d/health_check/", ctlID)), 200)
	must(t, auditor.post(fmt.Sprintf("instances/%d/health_check/", ctlID), nil), 403)
}
