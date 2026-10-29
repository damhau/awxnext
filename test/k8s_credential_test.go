package test

import (
	"fmt"
	"strings"
	"testing"
)

const k8sPlaybook = `- hosts: all
  gather_facts: false
  tasks:
    - ansible.builtin.debug:
        msg: "host={{ lookup('env', 'K8S_AUTH_HOST') }} keylen={{ lookup('env', 'K8S_AUTH_API_KEY') | length }} verify={{ lookup('env', 'K8S_AUTH_VERIFY_SSL') }} cafile={{ lookup('env', 'K8S_AUTH_SSL_CA_CERT') }}"
      run_once: true
    - ansible.builtin.debug:
        msg: "ca=[{{ lookup('file', lookup('env', 'K8S_AUTH_SSL_CA_CERT')) }}]"
      when: lookup('env', 'K8S_AUTH_SSL_CA_CERT') != ''
      run_once: true
`

// A job with an OpenShift or Kubernetes bearer token gets the CA data as a
// file (K8S_AUTH_SSL_CA_CERT), as AWX injects it.
func TestKubernetesTokenCA(t *testing.T) {
	f := newJobFixture(t)
	a := f.a
	repo := gitRepo(t, map[string]string{"k8s.yml": k8sPlaybook})
	f.project = must(t, a.post("projects/", map[string]any{"name": uniq("k8s"), "organization": f.org, "scm_type": "git", "scm_url": repo}), 201).id()
	f.waitProject(t)
	ct := credType(t, a, "kubernetes_bearer_token")
	if inj := a.get(fmt.Sprintf("credential_types/%d/", ct)).Body["injectors"]; fmt.Sprint(inj) != "map[]" {
		t.Fatalf("injectors (none, as in AWX: injected in code): %v", inj)
	}
	ca := "-----BEGIN CERTIFICATE-----\nMIIBfake\n-----END CERTIFICATE-----"
	run := func(inputs map[string]any) string {
		t.Helper()
		cred := must(t, a.post("credentials/", map[string]any{"name": uniq("k8s"), "organization": f.org, "credential_type": ct, "inputs": inputs}), 201).id()
		jt := f.template(t, "k8s.yml", nil)
		must(t, a.post(fmt.Sprintf("job_templates/%d/credentials/", jt), map[string]any{"id": cred}), 204)
		job := f.launch(t, a, jt, nil)
		mustStatus(t, f, job, "successful")
		return f.stdout(t, job.id())
	}
	base := map[string]any{"host": "https://k8s.example.com:6443", "bearer_token": "tok-12345"}
	with := func(extra map[string]any) map[string]any {
		m := map[string]any{}
		for k, v := range base {
			m[k] = v
		}
		for k, v := range extra {
			m[k] = v
		}
		return m
	}

	out := run(with(map[string]any{"verify_ssl": true, "ssl_ca_cert": ca}))
	if !strings.Contains(out, "host=https://k8s.example.com:6443 keylen=9 verify=True cafile=/") || !strings.Contains(out, "ca=["+strings.ReplaceAll(ca, "\n", `\n`)+"]") {
		t.Fatalf("with CA data:\n%s", out)
	}
	// Without CA data, verification stays on against the system's CAs.
	if out := run(with(map[string]any{"verify_ssl": true})); !strings.Contains(out, `verify=True cafile="`) || strings.Contains(out, "ca=[") {
		t.Fatalf("without CA data:\n%s", out)
	}
	// Without verification, no CA file.
	if out := run(with(map[string]any{"verify_ssl": false, "ssl_ca_cert": ca})); !strings.Contains(out, "verify=False cafile=") || strings.Contains(out, "ca=[") {
		t.Fatalf("without verification:\n%s", out)
	}
}
