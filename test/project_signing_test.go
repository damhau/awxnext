package test

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/ProtonMail/go-crypto/openpgp"
	"github.com/ProtonMail/go-crypto/openpgp/armor"
	"github.com/ProtonMail/go-crypto/openpgp/packet"
)

// signKey is a GPG key like the one a project maintainer signs with.
type signKey struct{ e *openpgp.Entity }

// signConfig dates keys and signatures an hour back: a key "created in the
// future" is expired to OpenPGP, and a host clock can step back.
var signConfig = &packet.Config{Algorithm: packet.PubKeyAlgoEdDSA, Time: func() time.Time { return time.Now().Add(-time.Hour) }}

func newSignKey(t *testing.T) signKey {
	t.Helper()
	e, err := openpgp.NewEntity("awxnext test", "", "signer@example.com", signConfig)
	if err != nil {
		t.Fatal(err)
	}
	return signKey{e}
}

func (k signKey) public(t *testing.T) string {
	var b bytes.Buffer
	w, _ := armor.Encode(&b, openpgp.PublicKeyType, nil)
	if err := k.e.Serialize(w); err != nil {
		t.Fatal(err)
	}
	w.Close()
	return b.String()
}

// signed adds what `ansible-sign project gpg-sign` writes for these files:
// the sorted sha256sum manifest and its armored detached signature.
func (k signKey) signed(t *testing.T, files map[string]string) map[string]string {
	t.Helper()
	names := make([]string, 0, len(files))
	for f := range files {
		names = append(names, f)
	}
	sort.Strings(names)
	var manifest strings.Builder
	for _, f := range names {
		h := sha256.Sum256([]byte(files[f]))
		manifest.WriteString(hex.EncodeToString(h[:]) + "  " + f + "\n")
	}
	var sig bytes.Buffer
	if err := openpgp.ArmoredDetachSign(&sig, k.e, strings.NewReader(manifest.String()), signConfig); err != nil {
		t.Fatal(err)
	}
	out := map[string]string{".ansible-sign/sha256sum.txt": manifest.String(), ".ansible-sign/sha256sum.txt.sig": sig.String()}
	for f, c := range files {
		out[f] = c
	}
	return out
}

// latestUpdate waits for the project's newest update to finish.
func latestUpdate(t *testing.T, a *client, pid int64) resp {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if r := a.get(fmt.Sprintf("project_updates/?unified_job_template=%d&order_by=-id&page_size=1", pid)).results(); len(r) > 0 {
			return waitJob(t, a, fmt.Sprintf("project_updates/%d/", int64(r[0]["id"].(float64))))
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("project %d never updated", pid)
	return resp{}
}

func TestProjectSignatureValidation(t *testing.T) {
	a := adminClient(t)
	allowFileURLs(t, a)
	org := newOrg(t, a)
	key := newSignKey(t)
	gpgCred := func(public string) int64 {
		return must(t, a.post("credentials/", map[string]any{"name": uniq("gpg"), "organization": org,
			"credential_type": credType(t, a, "gpg_public_key"), "inputs": map[string]any{"gpg_public_key": public}}), 201).id()
	}
	cred := gpgCred(key.public(t))
	content := map[string]string{
		"MANIFEST.in":              "include site.yml\nrecursive-include roles *.yml\n",
		"site.yml":                 helloPlaybook,
		"roles/web/tasks/main.yml": "- ansible.builtin.debug:\n    msg: web\n",
	}
	project := func(repo string, cred int64) int64 {
		body := map[string]any{"name": uniq("signed"), "organization": org, "scm_type": "git", "scm_url": repo}
		if cred != 0 {
			body["signature_validation_credential"] = cred
		}
		return must(t, a.post("projects/", body), 201).id()
	}
	failedWith := func(name string, pu resp, msg string) {
		t.Helper()
		if pu.Body["status"] != "failed" || !strings.Contains(pu.Body["job_explanation"].(string), msg) {
			t.Fatalf("%s: %v %v", name, pu.Body["status"], pu.Body["job_explanation"])
		}
		evs := must(t, a.get(fmt.Sprintf("project_updates/%d/events/?event=runner_on_failed", pu.id())), 200).results()
		if len(evs) != 1 || evs[0]["play"] != "Perform project signature/checksum verification" ||
			!strings.Contains(evs[0]["stdout"].(string), msg) {
			t.Fatalf("%s: failed events %v", name, evs)
		}
	}

	// Signed: both verification tasks pass and the project moves to the commit.
	repo := gitRepo(t, key.signed(t, content))
	pid := project(repo, cred)
	pu := latestUpdate(t, a, pid)
	if pu.Body["status"] != "successful" {
		t.Fatalf("signed project: %v %v\n%s", pu.Body["status"], pu.Body["job_explanation"], a.get(fmt.Sprintf("project_updates/%d/stdout/?format=txt", pu.id())).Raw)
	}
	signedRev := pu.Body["scm_revision"]
	if sf := must(t, a.get(fmt.Sprintf("projects/%d/", pid)), 200).Body["summary_fields"].(map[string]any); sf["signature_validation_credential"] == nil {
		t.Errorf("summary_fields: %v", sf)
	}
	var tasks []string
	for _, e := range must(t, a.get(fmt.Sprintf("project_updates/%d/events/?event=runner_on_ok", pu.id())), 200).results() {
		tasks = append(tasks, e["task"].(string))
	}
	if strings.Join(tasks, "|") != "Verify project content using GPG signature|Verify project content against checksum manifest" {
		t.Errorf("verification tasks: %v", tasks)
	}
	if out := a.get(fmt.Sprintf("project_updates/%d/stdout/?format=txt", pu.id())).Raw; !strings.Contains(out, "PLAY [Perform project signature/checksum verification]") {
		t.Errorf("stdout: %s", out)
	}

	// Modified after signing: the update fails and the project stays at the
	// signed revision, which jobs keep running.
	commitFiles(t, repo, map[string]string{"site.yml": helloPlaybook + "    - ansible.builtin.command: id\n"}, "unsigned change")
	up := must(t, a.post(fmt.Sprintf("projects/%d/update/", pid), nil), 202)
	pu = waitJob(t, a, fmt.Sprintf("project_updates/%d/", int64(up.Body["project_update"].(float64))))
	failedWith("modified", pu, "Checksum mismatch: site.yml")
	p := must(t, a.get(fmt.Sprintf("projects/%d/", pid)), 200).Body
	if p["status"] != "failed" || p["scm_revision"] != signedRev {
		t.Errorf("project after a failed verification: %v %v, want revision %v", p["status"], p["scm_revision"], signedRev)
	}

	// A file the manifest does not list.
	commitFiles(t, repo, key.signed(t, content), "re-signed")
	commitFiles(t, repo, map[string]string{"roles/web/tasks/extra.yml": "[]\n"}, "extra file")
	up = must(t, a.post(fmt.Sprintf("projects/%d/update/", pid), nil), 202)
	failedWith("added", waitJob(t, a, fmt.Sprintf("project_updates/%d/", int64(up.Body["project_update"].(float64)))),
		"'added': ['roles/web/tasks/extra.yml']")

	// Unsigned: no manifest, no signature.
	failedWith("unsigned", latestUpdate(t, a, project(gitRepo(t, content), cred)), "Expected file not found: .ansible-sign/sha256sum.txt.sig")

	// Signed with another key.
	other := newSignKey(t)
	failedWith("other key", latestUpdate(t, a, project(gitRepo(t, other.signed(t, content)), cred)), "GPG signature verification failed")

	// The manifest edited to match a change: its signature no longer does.
	forged := key.signed(t, content)
	forged["site.yml"] = "- hosts: all\n  tasks: [{command: id}]\n"
	forged[".ansible-sign/sha256sum.txt"] = strings.Replace(forged[".ansible-sign/sha256sum.txt"],
		manifestSum(content["site.yml"]), manifestSum(forged["site.yml"]), 1)
	failedWith("forged manifest", latestUpdate(t, a, project(gitRepo(t, forged), cred)), "GPG signature verification failed")

	// Without a signature validation credential nothing is verified.
	plain := latestUpdate(t, a, project(gitRepo(t, content), 0))
	if plain.Body["status"] != "successful" {
		t.Errorf("unverified project: %v %v", plain.Body["status"], plain.Body["job_explanation"])
	}
	for _, e := range must(t, a.get(fmt.Sprintf("project_updates/%d/events/?page_size=100", plain.id())), 200).results() {
		if e["play"] == "Perform project signature/checksum verification" {
			t.Errorf("unverified project ran %v", e["task"])
		}
	}
}

func manifestSum(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}
