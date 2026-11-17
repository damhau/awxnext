package test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/damhau/awxnext/internal/hubsign"
	"github.com/damhau/awxnext/internal/resources"
)

// artifactMember reads a member of a collection artifact.
func artifactMember(t *testing.T, data []byte, name string) []byte {
	t.Helper()
	gz, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if h.Name == name {
			b, _ := io.ReadAll(tr)
			return b
		}
	}
}

// hubKeyring downloads the hub's public keys, as anyone may: keyring.asc
// or keyring.gpg.
func hubKeyring(t *testing.T, file string) []byte {
	t.Helper()
	status, _, raw := galaxyGet(t, "", "/api/galaxy/"+file)
	if status != 200 {
		t.Fatalf("GET %s: %d %s", file, status, raw)
	}
	return raw
}

func keyFingerprints(t *testing.T, keys []byte) []string {
	t.Helper()
	list, err := hubsign.ReadKeys(string(keys))
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range list {
		out = append(out, hubsign.Fingerprint(e))
	}
	return out
}

func activeSigningKey(t *testing.T, a *client) map[string]any {
	t.Helper()
	r := must(t, a.get("collection_signing_keys/?active=true"), 200)
	if r.count() != 1 {
		t.Fatalf("active keys: %s", r.Raw)
	}
	return r.results()[0]
}

// versionSignatures are the signatures the hub serves in a version's v3
// detail.
func versionSignatures(t *testing.T, token, ns, name, version string) []map[string]any {
	t.Helper()
	status, body, raw := galaxyGet(t, "Token "+token, fmt.Sprintf("/api/galaxy/v3/plugin/ansible/content/published/collections/index/%s/%s/versions/%s/", ns, name, version))
	if status != 200 {
		t.Fatalf("version detail: %d %s", status, raw)
	}
	list, ok := body["signatures"].([]any)
	if !ok {
		t.Fatalf("signatures: %s", raw)
	}
	var out []map[string]any
	for _, s := range list {
		if m, ok := s.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out
}

func signedBy(sigs []map[string]any) []string {
	var out []string
	for _, s := range sigs {
		out = append(out, fmt.Sprint(s["pubkey_fingerprint"]))
	}
	return out
}

func TestHubSigning(t *testing.T) {
	a := adminClient(t)
	org := newOrg(t, a)
	ns := galaxyName("sig")
	nsID := must(t, a.post("collection_namespaces/", map[string]any{"name": ns, "organization": org}), 201).id()
	uploaderC, uploaderID := newUser(t, a)
	namespaceRole(t, a, "CollectionNamespace Upload", uploaderID, nsID)
	approverC, approverID := newUser(t, a)
	namespaceRole(t, a, "CollectionNamespace Approve", approverID, nsID)
	readerC, readerID := newUser(t, a)
	must(t, a.post("role_user_assignments/", map[string]any{"role_definition": roleDef(t, "Organization Member"), "user": readerID, "object_id": org}), 201)
	stranger, _ := newUser(t, a)
	upPAT, readPAT := writePAT(t, uploaderC), patOf(t, readerC)

	data := collectionTarball(t, ns, "tools", "1.0.0", nil, nil)
	manifest := artifactMember(t, data, "MANIFEST.json")
	publish(t, upPAT, data)
	vid := versionID(t, ns, "tools", "1.0.0")
	sigList := fmt.Sprintf("collection_versions/%d/signatures/", vid)
	if n := must(t, a.get(sigList), 200).count(); n != 0 {
		t.Fatalf("%d signatures before approval", n)
	}

	// Approval signs MANIFEST.json with the active key.
	v := must(t, approverC.post(fmt.Sprintf("collection_versions/%d/approve/", vid), nil), 200)
	key := activeSigningKey(t, a)
	fp := fmt.Sprint(key["fingerprint"])
	if _, ok := key["private_key"]; ok || !strings.Contains(fmt.Sprint(key["public_key"]), "BEGIN PGP PUBLIC KEY BLOCK") || len(fp) != 40 {
		t.Fatalf("signing key: %v", key)
	}
	summary, _ := v.Body["summary_fields"].(map[string]any)["signatures"].([]any)
	if len(summary) != 1 || fmt.Sprint(summary[0].(map[string]any)["created_by"]) != approverC.user ||
		summary[0].(map[string]any)["origin"] != "awxnext" {
		t.Fatalf("summary signatures: %v", v.Body["summary_fields"])
	}
	sigs := versionSignatures(t, readPAT, ns, "tools", "1.0.0")
	if len(sigs) != 1 || sigs[0]["pubkey_fingerprint"] != fp || sigs[0]["signing_service"] != "ansible-default" || sigs[0]["pulp_created"] == nil {
		t.Fatalf("served signatures: %v", sigs)
	}
	asc, gpg := hubKeyring(t, "keyring.asc"), hubKeyring(t, "keyring.gpg")
	if got := keyFingerprints(t, asc); len(got) == 0 || got[0] != fp {
		t.Fatalf("keyring.asc: %v", got)
	}
	if got := keyFingerprints(t, gpg); len(got) == 0 || got[0] != fp {
		t.Fatalf("keyring.gpg: %v", got)
	}
	if _, signer, err := hubsign.Verify(string(asc), manifest, fmt.Sprint(sigs[0]["signature"])); err != nil || signer.Fingerprint != fp {
		t.Fatalf("the hub's signature: %v", err)
	}

	// A signature made elsewhere, uploaded by an approver.
	ext, err := hubsign.Generate("External Signer", "signer@example.com")
	if err != nil {
		t.Fatal(err)
	}
	extSig, err := hubsign.Sign(ext.PrivateKey, manifest)
	if err != nil {
		t.Fatal(err)
	}
	upload := map[string]any{"signature": extSig, "public_key": ext.PublicKey}
	must(t, readerC.post(sigList, upload), 403)
	must(t, uploaderC.post(sigList, upload), 403)
	if r := stranger.post(sigList, upload); r.Status != 403 && r.Status != 404 {
		t.Fatalf("a stranger uploaded: %d %s", r.Status, r.Raw)
	}
	other, err := hubsign.Generate("Someone Else", "")
	if err != nil {
		t.Fatal(err)
	}
	otherData, _ := hubsign.Sign(ext.PrivateKey, []byte("other data"))
	for _, bad := range []struct {
		body map[string]any
		want string
	}{
		{map[string]any{"signature": extSig}, "public key"},
		{map[string]any{"signature": extSig, "public_key": other.PublicKey}, "none of the public keys"},
		{map[string]any{"signature": otherData, "public_key": ext.PublicKey}, "doesn't verify"},
		{map[string]any{"signature": "not a signature", "public_key": ext.PublicKey}, "not an armored OpenPGP signature"},
		{map[string]any{"signature": extSig, "public_key": "not a key"}, "no OpenPGP public key"},
	} {
		if r := must(t, approverC.post(sigList, bad.body), 400); !strings.Contains(r.Raw, bad.want) {
			t.Fatalf("want %q: %s", bad.want, r.Raw)
		}
	}
	up := must(t, approverC.post(sigList, upload), 201)
	if up.Body["origin"] != "upload" || up.Body["pubkey_fingerprint"] != ext.Fingerprint || up.Body["signer"] != "External Signer <signer@example.com>" ||
		fmt.Sprint(up.Body["created_by"]) != fmt.Sprint(approverID) || up.Body["signing_key"] != nil {
		t.Fatalf("uploaded: %s", up.Raw)
	}
	if r := must(t, approverC.post(sigList, upload), 400); !strings.Contains(r.Raw, "already has a signature by key "+ext.Fingerprint) {
		t.Fatalf("duplicate: %s", r.Raw)
	}
	sigs = versionSignatures(t, readPAT, ns, "tools", "1.0.0")
	if len(sigs) != 2 || sigs[1]["pubkey_fingerprint"] != ext.Fingerprint || sigs[1]["signing_service"] != nil {
		t.Fatalf("served signatures: %v", sigs)
	}
	if _, _, err := hubsign.Verify(ext.PublicKey, manifest, fmt.Sprint(sigs[1]["signature"])); err != nil {
		t.Fatalf("the uploaded signature: %v", err)
	}
	if r := must(t, readerC.get(sigList), 200); r.count() != 2 {
		t.Fatalf("the reader's signatures: %s", r.Raw)
	}
	if r := stranger.get(fmt.Sprintf("collection_version_signatures/%d/", up.id())); r.Status != 403 && r.Status != 404 {
		t.Fatalf("a stranger read a signature: %d", r.Status)
	}
	must(t, readerC.delete(fmt.Sprintf("collection_version_signatures/%d/", up.id())), 403)
	must(t, approverC.delete(fmt.Sprintf("collection_version_signatures/%d/", up.id())), 204)
	if sigs := versionSignatures(t, readPAT, ns, "tools", "1.0.0"); len(sigs) != 1 {
		t.Fatalf("after the delete: %v", sigs)
	}

	// sign signs a published version again, from its artifact when the hub
	// didn't keep MANIFEST.json (versions stored before signing).
	if _, err := app.DB.Exec(t.Context(), "DELETE FROM collection_version_signatures WHERE collection_version_id = $1", vid); err != nil {
		t.Fatal(err)
	}
	if _, err := app.DB.Exec(t.Context(), "UPDATE collection_versions SET manifest_json = NULL WHERE id = $1", vid); err != nil {
		t.Fatal(err)
	}
	must(t, readerC.post(fmt.Sprintf("collection_versions/%d/sign/", vid), nil), 403)
	must(t, approverC.post(fmt.Sprintf("collection_versions/%d/sign/", vid), nil), 200)
	sigs = versionSignatures(t, readPAT, ns, "tools", "1.0.0")
	if len(sigs) != 1 {
		t.Fatalf("signed again: %v", sigs)
	}
	if _, _, err := hubsign.Verify(string(asc), manifest, fmt.Sprint(sigs[0]["signature"])); err != nil {
		t.Fatalf("signed again from the artifact: %v", err)
	}
	publish(t, upPAT, collectionTarball(t, ns, "tools", "1.1.0", nil, nil))
	v11 := versionID(t, ns, "tools", "1.1.0")
	if r := must(t, approverC.post(fmt.Sprintf("collection_versions/%d/sign/", v11), nil), 400); !strings.Contains(r.Raw, "isn't published") {
		t.Fatalf("signed a staged version: %s", r.Raw)
	}

	// Rotation: the new key signs from then on, every published version at
	// once; the old key's signatures stay until it is deleted.
	must(t, approverC.post("collection_signing_keys/", map[string]any{"name": "awxnext hub 2"}), 403)
	must(t, readerC.get("collection_signing_keys/"), 200)
	k2 := must(t, a.post("collection_signing_keys/", map[string]any{"name": "awxnext hub 2", "email": "hub@example.com"}), 201)
	fp2 := fmt.Sprint(k2.Body["fingerprint"])
	if k2.Body["active"] != true || fp2 == fp || k2.Body["algorithm"] != "RSA 3072" {
		t.Fatalf("new key: %s", k2.Raw)
	}
	old := must(t, a.get(fmt.Sprintf("collection_signing_keys/%v/", key["id"])), 200)
	if old.Body["active"] != false || old.Body["retired"] == nil {
		t.Fatalf("old key: %s", old.Raw)
	}
	if got := signedBy(versionSignatures(t, readPAT, ns, "tools", "1.0.0")); fmt.Sprint(got) != fmt.Sprint([]string{fp, fp2}) {
		t.Fatalf("signed by %v after the rotation", got)
	}
	if got := keyFingerprints(t, hubKeyring(t, "keyring.gpg")); len(got) < 2 || got[0] != fp2 || got[1] != fp {
		t.Fatalf("keyring after the rotation: %v", got)
	}
	must(t, approverC.post(fmt.Sprintf("collection_versions/%d/approve/", v11), nil), 200)
	sigs = versionSignatures(t, readPAT, ns, "tools", "1.1.0")
	if len(sigs) != 1 || sigs[0]["pubkey_fingerprint"] != fp2 {
		t.Fatalf("approved after the rotation: %v", sigs)
	}
	if r := must(t, a.get(fmt.Sprintf("collection_signing_keys/%d/signatures/", k2.id())), 200); r.count() < 2 {
		t.Fatalf("the new key's signatures: %s", r.Raw)
	}
	must(t, a.patch(fmt.Sprintf("collection_signing_keys/%d/", k2.id()), map[string]any{"name": "renamed"}), 400)
	must(t, a.patch(fmt.Sprintf("collection_signing_keys/%d/", k2.id()), map[string]any{"description": "Signs from November"}), 200)
	if r := must(t, a.delete(fmt.Sprintf("collection_signing_keys/%d/", k2.id())), 400); !strings.Contains(r.Raw, "active key") {
		t.Fatalf("deleted the active key: %s", r.Raw)
	}
	must(t, approverC.delete(fmt.Sprintf("collection_signing_keys/%v/", key["id"])), 403)
	must(t, a.delete(fmt.Sprintf("collection_signing_keys/%v/", key["id"])), 204)
	if got := signedBy(versionSignatures(t, readPAT, ns, "tools", "1.0.0")); fmt.Sprint(got) != fmt.Sprint([]string{fp2}) {
		t.Fatalf("signed by %v once the old key is deleted", got)
	}
	if got := keyFingerprints(t, hubKeyring(t, "keyring.asc")); len(got) != 1 || got[0] != fp2 {
		t.Fatalf("keyring once the old key is deleted: %v", got)
	}
	// A key made without signing what is published only signs what comes.
	k3 := must(t, a.post("collection_signing_keys/", map[string]any{"sign_published": false}), 201)
	if got := signedBy(versionSignatures(t, readPAT, ns, "tools", "1.0.0")); fmt.Sprint(got) != fmt.Sprint([]string{fp2}) {
		t.Fatalf("signed by %v after a key that signs nothing published", got)
	}
	if k3.Body["name"] != "awxnext collection hub" {
		t.Fatalf("default name: %s", k3.Raw)
	}
}

func TestHubSigningProjectFields(t *testing.T) {
	a := adminClient(t)
	org := newOrg(t, a)
	keys, err := hubsign.Generate("project test", "")
	if err != nil {
		t.Fatal(err)
	}
	gpgCred := must(t, a.post("credentials/", map[string]any{"name": uniq("keys"), "organization": org,
		"credential_type": credType(t, a, "gpg_public_key"), "inputs": map[string]any{"gpg_public_key": keys.PublicKey}}), 201).id()
	machine := must(t, a.post("credentials/", map[string]any{"name": uniq("machine"), "organization": org,
		"credential_type": credType(t, a, "ssh"), "inputs": map[string]any{"username": "u"}}), 201).id()
	base := func() map[string]any {
		return map[string]any{"name": uniq("signed"), "organization": org, "scm_type": "git", "scm_url": "https://example.com/repo.git"}
	}
	with := func(extra map[string]any) map[string]any {
		b := base()
		for k, v := range extra {
			b[k] = v
		}
		return b
	}
	must(t, a.post("projects/", with(map[string]any{"collection_signature_validation_credential": machine})), 400)
	for _, n := range []int{0, 11} {
		must(t, a.post("projects/", with(map[string]any{"collection_signature_count": n})), 400)
	}
	p := must(t, a.post("projects/", with(map[string]any{"collection_signature_validation_credential": gpgCred, "collection_signature_count": 2})), 201)
	if fmt.Sprint(p.Body["collection_signature_validation_credential"]) != fmt.Sprint(gpgCred) || fmt.Sprint(p.Body["collection_signature_count"]) != "2" {
		t.Fatalf("project: %s", p.Raw)
	}
	if def := must(t, a.post("projects/", base()), 201); fmt.Sprint(def.Body["collection_signature_count"]) != "1" || def.Body["collection_signature_validation_credential"] != nil {
		t.Fatalf("defaults: %s", def.Raw)
	}
	// Setting the credential takes its use role; keeping it doesn't.
	user, uid := newUser(t, a)
	must(t, a.post("role_user_assignments/", map[string]any{"role_definition": roleDef(t, "Project Admin"), "user": uid, "object_id": p.id()}), 201)
	must(t, user.patch(fmt.Sprintf("projects/%d/", p.id()), map[string]any{"collection_signature_count": 1}), 200)
	other := must(t, a.post("credentials/", map[string]any{"name": uniq("keys"), "organization": org,
		"credential_type": credType(t, a, "gpg_public_key"), "inputs": map[string]any{"gpg_public_key": keys.PublicKey}}), 201).id()
	must(t, user.patch(fmt.Sprintf("projects/%d/", p.id()), map[string]any{"collection_signature_validation_credential": other}), 403)
	must(t, a.post("role_user_assignments/", map[string]any{"role_definition": roleDef(t, "Credential Use"), "user": uid, "object_id": other}), 201)
	must(t, user.patch(fmt.Sprintf("projects/%d/", p.id()), map[string]any{"collection_signature_validation_credential": other}), 200)
	// Deleting the credential clears it, as used_by says.
	used := must(t, a.get(fmt.Sprintf("credentials/%d/used_by/", other)), 200)
	if !strings.Contains(used.Raw, "collection_signature_validation_credential") {
		t.Fatalf("used_by: %s", used.Raw)
	}
	must(t, a.delete(fmt.Sprintf("credentials/%d/", other)), 204)
	if r := must(t, a.get(fmt.Sprintf("projects/%d/", p.id())), 200); r.Body["collection_signature_validation_credential"] != nil {
		t.Fatalf("after the credential's delete: %s", r.Raw)
	}
}

// TestHubSigningAnsibleGalaxy checks the hub's signatures with
// ansible-galaxy and gpg: install --keyring and verify pass on a signed
// collection and fail on a tampered one, each for its reason.
func TestHubSigningAnsibleGalaxy(t *testing.T) {
	requireAnsible(t)
	for _, tool := range []string{"ansible-galaxy", "gpg"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skip(tool + " not installed")
		}
	}
	a := adminClient(t)
	org := newOrg(t, a)
	reader, readerID := newUser(t, a)
	must(t, a.post("role_user_assignments/", map[string]any{"role_definition": roleDef(t, "Organization Member"), "user": readerID, "object_id": org}), 201)
	pat := patOf(t, reader)
	ns := galaxyName("gpg")
	must(t, a.post("collection_namespaces/", map[string]any{"name": ns, "organization": org}), 201)
	fqcn := ns + ".tools"
	data := collectionTarball(t, ns, "tools", "1.0.0", nil, map[string]string{"plugins/modules/hello.py": "# hello\n"})
	hubPublish(t, data)
	vid := versionID(t, ns, "tools", "1.0.0")
	hubKey := hubKeyring(t, "keyring.gpg")

	// Each step its own ansible-galaxy: its cache keeps a version's
	// signatures for a day.
	client := func(keys ...[]byte) (*ansibleGalaxy, string) {
		t.Helper()
		g := newAnsibleGalaxy(t, srv.URL+"/api/galaxy/content/published/", pat)
		var armored []string
		for _, k := range keys {
			armored = append(armored, string(k))
		}
		kr, err := hubsign.Keyring(armored...)
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(g.dir, "keyring.gpg")
		if err := os.WriteFile(path, kr, 0o600); err != nil {
			t.Fatal(err)
		}
		return g, path
	}
	g, kr := client(hubKey)
	dest := filepath.Join(g.dir, "installed")
	g.mustRun("collection", "install", fqcn, "-p", dest, "--keyring", kr, "--required-valid-signature-count", "+1")
	if v := installedVersion(t, dest, ns, "tools"); v != "1.0.0" {
		t.Fatalf("installed %q", v)
	}
	verify := func(g *ansibleGalaxy, kr, dest, count string) (string, error) {
		return g.run("collection", "verify", fqcn, "-p", dest, "--keyring", kr, "--required-valid-signature-count", count)
	}
	if out, err := verify(g, kr, dest, "+1"); err != nil || !strings.Contains(out, "Successfully verified that checksums for '"+fqcn+":1.0.0' match the remote collection") {
		t.Fatalf("verify: %v\n%s", err, out)
	}

	// An installed file changed: the signature holds, the checksums don't.
	installed := filepath.Join(dest, "ansible_collections", ns, "tools")
	module := filepath.Join(installed, "plugins", "modules", "hello.py")
	os.WriteFile(module, []byte("# changed\n"), 0o644)
	if out, err := verify(g, kr, dest, "+1"); err == nil || !strings.Contains(out, "contains modified content") || !strings.Contains(out, "plugins/modules/hello.py") {
		t.Fatalf("verify with a changed file: %v\n%s", err, out)
	}
	os.WriteFile(module, []byte("# hello\n"), 0o644)
	// The installed MANIFEST.json changed: its signature fails.
	manifestPath := filepath.Join(installed, "MANIFEST.json")
	signed, _ := os.ReadFile(manifestPath)
	os.WriteFile(manifestPath, bytes.Replace(signed, []byte("A test collection"), []byte("A changed collection"), 1), 0o644)
	if out, err := verify(g, kr, dest, "+1"); err == nil || !strings.Contains(out, "Signature verification failed for '"+fqcn+"': no successful signatures") {
		t.Fatalf("verify with a changed MANIFEST.json: %v\n%s", err, out)
	}
	os.WriteFile(manifestPath, signed, 0o644)

	// A signature made elsewhere, uploaded: both count.
	ext, err := hubsign.Generate("External Signer", "signer@example.com")
	if err != nil {
		t.Fatal(err)
	}
	extSig, err := hubsign.Sign(ext.PrivateKey, artifactMember(t, data, "MANIFEST.json"))
	if err != nil {
		t.Fatal(err)
	}
	must(t, a.post(fmt.Sprintf("collection_versions/%d/signatures/", vid), map[string]any{"signature": extSig, "public_key": ext.PublicKey}), 201)
	if got := signedBy(versionSignatures(t, pat, ns, "tools", "1.0.0")); len(got) != 2 || got[1] != ext.Fingerprint {
		t.Fatalf("signed by %v", got)
	}
	g2, kr2 := client(hubKey)
	dest2 := filepath.Join(g2.dir, "installed")
	out, err := g2.run("collection", "install", fqcn, "-p", dest2, "--keyring", kr2, "--required-valid-signature-count", "+2")
	if err == nil || !strings.Contains(out, "Signature verification failed for '"+fqcn+"': fewer successful signatures than required") ||
		!strings.Contains(out, "Not installing "+fqcn+" because GnuPG signature verification failed") {
		t.Fatalf("two signatures required, the hub's key only: %v\n%s", err, out)
	}
	g3, kr3 := client(hubKey, []byte(ext.PublicKey))
	dest3 := filepath.Join(g3.dir, "installed")
	g3.mustRun("collection", "install", fqcn, "-p", dest3, "--keyring", kr3, "--required-valid-signature-count", "+2")
	if out, err := verify(g3, kr3, dest3, "+2"); err != nil || !strings.Contains(out, "Successfully verified") {
		t.Fatalf("verify with two signatures: %v\n%s", err, out)
	}

	// After a rotation, the old key and the new one each verify.
	k2 := must(t, a.post("collection_signing_keys/", map[string]any{"name": "rotated"}), 201)
	for _, keys := range [][]byte{hubKey, []byte(fmt.Sprint(k2.Body["public_key"]))} {
		g, kr := client(keys)
		g.mustRun("collection", "install", fqcn, "-p", filepath.Join(g.dir, "installed"), "--keyring", kr, "--required-valid-signature-count", "+1")
	}

	// The hub serves an artifact other than the one signed, its checksum
	// matching: install refuses it with the keyring, and installs it
	// without, the signature the only difference.
	tampered := collectionTarball(t, ns, "tools", "1.0.0", nil, map[string]string{"plugins/modules/hello.py": "# tampered\n"})
	if _, err := app.DB.Exec(t.Context(), "UPDATE collection_version_data SET data = $2 WHERE version_id = $1", vid, tampered); err != nil {
		t.Fatal(err)
	}
	if _, err := app.DB.Exec(t.Context(), "UPDATE collection_versions SET sha256 = $2, size = $3 WHERE id = $1", vid, sha256Hex(tampered), len(tampered)); err != nil {
		t.Fatal(err)
	}
	g4, kr4 := client(hubKeyring(t, "keyring.gpg"))
	dest4 := filepath.Join(g4.dir, "installed")
	out, err = g4.run("collection", "install", fqcn, "-p", dest4, "--keyring", kr4)
	if err == nil || !strings.Contains(out, "Not installing "+fqcn+" because GnuPG signature verification failed") || installedVersion(t, dest4, ns, "tools") != "" {
		t.Fatalf("installed a tampered artifact: %v\n%s", err, out)
	}
	g5, _ := client(hubKey)
	dest5 := filepath.Join(g5.dir, "installed")
	g5.mustRun("collection", "install", fqcn, "-p", dest5)
	if b, _ := os.ReadFile(filepath.Join(dest5, "ansible_collections", ns, "tools", "plugins", "modules", "hello.py")); string(b) != "# tampered\n" {
		t.Fatalf("installed without a keyring: %q", b)
	}
}

// TestHubSigningProjectRequirements: a project that requires signatures
// installs a signed collection, and fails on an unsigned one, with the
// keys of a GPG Public Key credential; an install cached without the
// requirement doesn't meet it.
func TestHubSigningProjectRequirements(t *testing.T) {
	f := newJobFixture(t)
	a := f.a
	if _, err := exec.LookPath("gpg"); err != nil {
		t.Skip("gpg not installed")
	}
	ns := galaxyName("req")
	must(t, a.post("collection_namespaces/", map[string]any{"name": ns, "organization": f.org}), 201)
	hubPublish(t, collectionTarball(t, ns, "tools", "1.0.0", nil,
		map[string]string{"roles/hello/tasks/main.yml": "- ansible.builtin.debug:\n    msg: from-signed-role\n"}))
	vid := versionID(t, ns, "tools", "1.0.0")
	cred := must(t, a.get("credentials/?managed=true&name="+url.QueryEscape(resources.HubCredentialName)), 200).results()[0]
	must(t, a.post(fmt.Sprintf("organizations/%d/galaxy_credentials/", f.org), map[string]any{"id": cred["id"]}), 204)
	prev := a.get("settings/jobs/").Body["GALAXY_TASK_ENV"]
	must(t, a.patch("settings/jobs/", map[string]any{"GALAXY_TASK_ENV": map[string]any{"ANSIBLE_GALAXY_CACHE_DIR": t.TempDir()}}), 200)
	t.Cleanup(func() { a.patch("settings/jobs/", map[string]any{"GALAXY_TASK_ENV": prev}) })
	keys := must(t, a.post("credentials/", map[string]any{"name": uniq("hubkeys"), "organization": f.org,
		"credential_type": credType(t, a, "gpg_public_key"), "inputs": map[string]any{"gpg_public_key": string(hubKeyring(t, "keyring.asc"))}}), 201).id()

	repo := gitRepo(t, map[string]string{
		"collections/requirements.yml": fmt.Sprintf("collections:\n  - name: %s.tools\n    version: '>=1.0.0'\n", ns),
		"site.yml":                     "- hosts: all\n  gather_facts: false\n  tasks:\n    - ansible.builtin.include_role:\n        name: " + ns + ".tools.hello\n",
	})
	f.project = must(t, a.post("projects/", map[string]any{"name": uniq("signedreqs"), "organization": f.org, "scm_type": "git", "scm_url": repo,
		"collection_signature_validation_credential": keys}), 201).id()
	f.waitProject(t)
	jt := f.template(t, "site.yml", map[string]any{"limit": "h1"})
	job := f.launch(t, a, jt, nil)
	mustStatus(t, f, job, "successful")
	if out := f.stdout(t, job.id()); !strings.Contains(out, "Collections are installed only with a valid signature from the keys of credential") || !strings.Contains(out, "from-signed-role") {
		t.Fatalf("stdout:\n%s", out)
	}

	// Unsigned: its signatures deleted through the API.
	sigs := must(t, a.get(fmt.Sprintf("collection_versions/%d/signatures/", vid)), 200)
	for _, s := range sigs.results() {
		must(t, a.delete(fmt.Sprintf("collection_version_signatures/%v/", s["id"])), 204)
	}
	job = f.launch(t, a, jt, nil)
	mustStatus(t, f, job, "failed")
	out := f.stdout(t, job.id())
	if !strings.Contains(out, "Signature verification failed for '"+ns+".tools': no successful signatures") ||
		!strings.Contains(fmt.Sprint(job.Body["job_explanation"]), "Project requirements") {
		t.Fatalf("unsigned: %v\n%s", job.Body["job_explanation"], out)
	}

	// Installed without the requirement, then required again: the cached
	// install doesn't count.
	project := fmt.Sprintf("projects/%d/", f.project)
	must(t, a.patch(project, map[string]any{"collection_signature_validation_credential": nil}), 200)
	mustStatus(t, f, f.launch(t, a, jt, nil), "successful")
	must(t, a.patch(project, map[string]any{"collection_signature_validation_credential": keys}), 200)
	job = f.launch(t, a, jt, nil)
	mustStatus(t, f, job, "failed")
	if out := f.stdout(t, job.id()); strings.Contains(out, "Using cached project requirements") || !strings.Contains(out, "no successful signatures") {
		t.Fatalf("required again:\n%s", out)
	}
}
