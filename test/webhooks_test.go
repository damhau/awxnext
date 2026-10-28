package test

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

func postRaw(t *testing.T, path string, body []byte, headers map[string]string) (int, map[string]any, http.Header) {
	t.Helper()
	req, _ := http.NewRequest("POST", srv.URL+"/api/v2/"+path, bytes.NewReader(body))
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	var m map[string]any
	json.Unmarshal(b, &m)
	return res.StatusCode, m, res.Header
}

func sign(key string, body []byte) string {
	h := hmac.New(sha256.New, []byte(key))
	h.Write(body)
	return "sha256=" + hex.EncodeToString(h.Sum(nil))
}

func TestInboundWebhooks(t *testing.T) {
	f := newJobFixture(t)
	a := f.a
	rec, hook := newHookServer(t)
	token := must(t, a.post("credentials/", map[string]any{"name": uniq("gh"), "organization": f.org, "credential_type": credType(t, a, "github_token"),
		"inputs": map[string]any{"token": "ghp_secret"}}), 201).id()
	ssh := must(t, a.post("credentials/", map[string]any{"name": uniq("ssh"), "organization": f.org, "credential_type": credType(t, a, "ssh"), "inputs": map[string]any{}}), 201).id()

	jt := f.template(t, "site.yml", nil)
	must(t, a.patch(fmt.Sprintf("job_templates/%d/", jt), map[string]any{"webhook_credential": token}), 400)
	must(t, a.patch(fmt.Sprintf("job_templates/%d/", jt), map[string]any{"webhook_service": "github", "webhook_credential": ssh}), 400)
	r := must(t, a.patch(fmt.Sprintf("job_templates/%d/", jt), map[string]any{"webhook_service": "github", "webhook_credential": token}), 200)
	if r.Body["related"].(map[string]any)["webhook_receiver"] != fmt.Sprintf("/api/v2/job_templates/%d/github/", jt) {
		t.Fatalf("receiver link: %v", r.Body["related"])
	}
	key := must(t, a.get(fmt.Sprintf("job_templates/%d/webhook_key/", jt)), 200).Body["webhook_key"].(string)
	if len(key) < 40 {
		t.Fatalf("webhook key: %q", key)
	}
	payload, _ := json.Marshal(map[string]any{"action": "opened", "pull_request": map[string]any{"head": map[string]any{"sha": "abc123"}, "statuses_url": hook.URL + "/statuses/abc123"}})
	path := fmt.Sprintf("job_templates/%d/github/", jt)
	if code, _, _ := postRaw(t, path, payload, map[string]string{"X-GitHub-Event": "pull_request", "X-Hub-Signature-256": "sha256=00", "X-GitHub-Delivery": "d1"}); code != 403 {
		t.Fatalf("bad signature accepted: %d", code)
	}
	headers := map[string]string{"X-GitHub-Event": "pull_request", "X-Hub-Signature-256": sign(key, payload), "X-GitHub-Delivery": "d1", "Content-Type": "application/json"}
	code, body, _ := postRaw(t, path, payload, headers)
	if code != 202 || body["message"] != "Job queued." {
		t.Fatalf("webhook: %d %v", code, body)
	}
	jobID := int64(body["job"].(float64))
	if code, body, _ := postRaw(t, path, payload, headers); code != 202 || !strings.Contains(fmt.Sprint(body["message"]), "previously received") {
		t.Fatalf("duplicate delivery: %d %v", code, body)
	}
	job := waitJob(t, a, fmt.Sprintf("jobs/%d/", jobID))
	mustStatus(t, f, job, "successful")
	if job.Body["launch_type"] != "webhook" || job.Body["webhook_guid"] != "d1" || job.Body["webhook_service"] != "github" ||
		!strings.Contains(job.Body["extra_vars"].(string), `"awx_webhook_event_ref": "abc123"`) {
		t.Fatalf("webhook job: %v", job.Body)
	}
	reqs := rec.wait(t, 2)
	if reqs[0].Body["state"] != "pending" || reqs[1].Body["state"] != "success" || reqs[0].Auth != ":" {
		t.Fatalf("status updates: %+v", reqs)
	}

	// Rotating the key invalidates the old one.
	newKey := must(t, a.post(fmt.Sprintf("job_templates/%d/webhook_key/", jt), nil), 201).Body["webhook_key"].(string)
	headers["X-GitHub-Delivery"] = "d2"
	if code, _, _ := postRaw(t, path, payload, headers); code != 403 {
		t.Fatalf("old key still valid: %d", code)
	}
	headers["X-Hub-Signature-256"] = sign(newKey, payload)
	if code, _, _ := postRaw(t, path, payload, headers); code != 202 {
		t.Fatalf("new key rejected: %d", code)
	}

	// GitLab uses a shared token.
	gl := f.template(t, "site.yml", map[string]any{"webhook_service": "gitlab"})
	glKey := a.get(fmt.Sprintf("job_templates/%d/webhook_key/", gl)).Body["webhook_key"].(string)
	push, _ := json.Marshal(map[string]any{"checkout_sha": "def456"})
	if code, _, _ := postRaw(t, fmt.Sprintf("job_templates/%d/gitlab/", gl), push, map[string]string{"X-Gitlab-Event": "Push Hook", "X-Gitlab-Token": "nope"}); code != 403 {
		t.Fatalf("gitlab bad token: %d", code)
	}
	if code, _, _ := postRaw(t, fmt.Sprintf("job_templates/%d/gitlab/", gl), push, map[string]string{"X-Gitlab-Event": "Push Hook", "X-Gitlab-Token": glKey}); code != 202 {
		t.Fatalf("gitlab: %d", code)
	}
	if code, _, _ := postRaw(t, fmt.Sprintf("job_templates/%d/github/", gl), push, map[string]string{}); code != 404 {
		t.Fatalf("wrong service endpoint: %d", code)
	}

	// Generic webhooks map payload values with JSONPath.
	must(t, a.post("job_templates/", map[string]any{"name": uniq("g"), "project": f.project, "inventory": f.inv, "playbook": "site.yml",
		"webhook_service": "generic", "webhook_mapping": map[string]any{"bogus": "$.x"}}), 400)
	gen := f.template(t, "site.yml", map[string]any{"webhook_service": "generic", "webhook_mapping": map[string]any{
		"filter": map[string]any{"$.action": "deploy"}, "extra_vars": map[string]any{"who": "$.release['author']"}, "limit": "$.targets[0]"}})
	genKey := a.get(fmt.Sprintf("job_templates/%d/webhook_key/", gen)).Body["webhook_key"].(string)
	ignored, _ := json.Marshal(map[string]any{"action": "build"})
	code, body, _ = postRaw(t, fmt.Sprintf("job_templates/%d/webhook/", gen), ignored, map[string]string{"Authorization": "Bearer " + genKey})
	if code != 200 || !strings.Contains(fmt.Sprint(body["message"]), "ignored") {
		t.Fatalf("filter: %d %v", code, body)
	}
	deploy, _ := json.Marshal(map[string]any{"action": "deploy", "release": map[string]any{"author": "ci-bot"}, "targets": []any{"h3"}})
	code, body, _ = postRaw(t, fmt.Sprintf("job_templates/%d/webhook/", gen), deploy, map[string]string{"X-Hub-Signature-256": sign(genKey, deploy)})
	if code != 202 {
		t.Fatalf("generic: %d %v", code, body)
	}
	gj := waitJob(t, a, fmt.Sprintf("jobs/%d/", int64(body["job"].(float64))))
	mustStatus(t, f, gj, "successful")
	if out := f.stdout(t, gj.id()); gj.Body["limit"] != "h3" || !strings.Contains(out, "hello ci-bot from h3") {
		t.Fatalf("generic mapping: limit=%v\n%s", gj.Body["limit"], out)
	}

	// Workflows accept webhooks too.
	wf := must(t, a.post("workflow_job_templates/", map[string]any{"name": uniq("wf"), "organization": f.org, "webhook_service": "generic"}), 201).id()
	must(t, a.post(fmt.Sprintf("workflow_job_templates/%d/workflow_nodes/", wf), map[string]any{"unified_job_template": jt}), 201)
	wfKey := a.get(fmt.Sprintf("workflow_job_templates/%d/webhook_key/", wf)).Body["webhook_key"].(string)
	code, body, _ = postRaw(t, fmt.Sprintf("workflow_job_templates/%d/webhook/", wf), []byte(`{}`), map[string]string{"X-Webhook-Token": wfKey})
	if code != 202 {
		t.Fatalf("workflow webhook: %d %v", code, body)
	}
	if r := waitJob(t, a, fmt.Sprintf("workflow_jobs/%d/", int64(body["job"].(float64)))); r.Body["launch_type"] != "webhook" || r.Body["status"] != "successful" {
		t.Fatalf("workflow webhook job: %v", r.Body)
	}
}

func TestProvisioningCallback(t *testing.T) {
	f := newJobFixture(t)
	a := f.a
	must(t, a.post("hosts/", map[string]any{"name": "cbhost", "inventory": f.inv, "variables": "ansible_host: 127.0.0.1"}), 201)
	jt := f.template(t, "site.yml", map[string]any{"host_config_key": "s3cret", "ask_variables_on_launch": true})
	path := fmt.Sprintf("job_templates/%d/callback/", jt)

	if r := must(t, a.get(path), 200); fmt.Sprint(r.Body["matching_hosts"]) != "[cbhost]" || r.Body["host_config_key"] != "s3cret" {
		t.Fatalf("callback info: %v", r.Body)
	}
	if code, _, _ := postRaw(t, path, []byte(`{}`), map[string]string{"Content-Type": "application/json"}); code != 403 {
		t.Fatalf("missing key: %d", code)
	}
	if code, _, _ := postRaw(t, path, []byte(`{"host_config_key": "wrong"}`), map[string]string{"Content-Type": "application/json"}); code != 403 {
		t.Fatalf("wrong key: %d", code)
	}
	code, body, hdr := postRaw(t, path, []byte(`{"host_config_key": "s3cret", "extra_vars": {"who": "callback", "ansible_connection": "ssh"}}`),
		map[string]string{"Content-Type": "application/json"})
	if code != 201 || !strings.HasPrefix(hdr.Get("Location"), "/api/v2/jobs/") {
		t.Fatalf("callback: %d %v", code, body)
	}
	job := waitJob(t, a, hdr.Get("Location")[len("/api/v2/"):])
	mustStatus(t, f, job, "successful")
	if job.Body["limit"] != "cbhost" || job.Body["launch_type"] != "callback" || strings.Contains(job.Body["extra_vars"].(string), "ansible_connection") {
		t.Fatalf("callback job: %v", job.Body)
	}
	if out := f.stdout(t, job.id()); !strings.Contains(out, "hello callback from cbhost") {
		t.Fatalf("stdout:\n%s", out)
	}
	// Form-encoded requests work as well (curl --data).
	form := url.Values{"host_config_key": {"s3cret"}}.Encode()
	code, _, hdr = postRaw(t, path, []byte(form), map[string]string{"Content-Type": "application/x-www-form-urlencoded"})
	if code != 201 {
		t.Fatalf("form callback: %d", code)
	}
	waitJob(t, a, hdr.Get("Location")[len("/api/v2/"):])

	// No matching host.
	inv2 := must(t, a.post("inventories/", map[string]any{"name": uniq("inv"), "organization": f.org}), 201).id()
	jt2 := f.template(t, "site.yml", map[string]any{"host_config_key": "k", "inventory": inv2})
	if code, body, _ := postRaw(t, fmt.Sprintf("job_templates/%d/callback/", jt2), []byte(`{"host_config_key": "k"}`), map[string]string{"Content-Type": "application/json"}); code != 400 || body["msg"] != "No matching host could be found!" {
		t.Fatalf("no match: %d %v", code, body)
	}
	_ = time.Second
}
