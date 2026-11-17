package hubsign

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/ProtonMail/go-crypto/openpgp"
	"github.com/ProtonMail/go-crypto/openpgp/packet"
)

func TestSignAndVerify(t *testing.T) {
	key, err := Generate("awxnext collection hub", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(key.Fingerprint) != 40 || key.UserID != "awxnext collection hub" || !strings.Contains(key.PublicKey, "BEGIN PGP PUBLIC KEY BLOCK") {
		t.Fatalf("key: %+v", key)
	}
	data := []byte(`{"collection_info": {"namespace": "acme"}}`)
	sig, err := Sign(key.PrivateKey, data)
	if err != nil {
		t.Fatal(err)
	}
	armored, signer, err := Verify(key.PublicKey, data, sig)
	if err != nil || signer.Fingerprint != key.Fingerprint || signer.UserID != key.UserID || !strings.Contains(armored, "BEGIN PGP SIGNATURE") {
		t.Fatalf("verify: %v %+v", err, signer)
	}
	if list, err := ReadKeys(signer.PublicKey); err != nil || len(list) != 1 || Fingerprint(list[0]) != key.Fingerprint {
		t.Fatalf("signer's public key: %v", err)
	}
	if _, _, err := Verify(key.PublicKey, []byte("changed"), sig); err == nil {
		t.Fatal("a signature of other data verified")
	}
	other, err := Generate("someone else", "else@example.com")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := Verify(other.PublicKey, data, sig); err == nil || !strings.Contains(err.Error(), "none of the public keys") {
		t.Fatalf("verified with another key: %v", err)
	}
	for _, bad := range []string{"", "not armored", key.PublicKey} {
		if _, _, err := Verify(key.PublicKey, data, bad); err == nil {
			t.Fatalf("verified %q", bad)
		}
	}
	// OpenPGP.js and go-crypto's default salt breaks ansible-galaxy.
	keys, _ := ReadKeys(key.PrivateKey)
	salt := true
	var salted bytes.Buffer
	if err := openpgp.ArmoredDetachSign(&salted, keys[0], bytes.NewReader(data), &packet.Config{NonDeterministicSignaturesViaNotation: &salt}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Verify(key.PublicKey, data, salted.String()); err == nil || !strings.Contains(err.Error(), "binary notation") {
		t.Fatalf("a salted signature: %v", err)
	}
	both, err := ArmoredKeyring(key.PublicKey, other.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	if list, err := ReadKeys(both); err != nil || len(list) != 2 {
		t.Fatalf("armored keyring: %v %d", err, len(list))
	}
	if _, err := ReadKeys("garbage"); err == nil {
		t.Fatal("read garbage as keys")
	}
}

// TestGPG checks the signatures and keyrings with gpg, as ansible-galaxy
// calls it.
func TestGPG(t *testing.T) {
	if _, err := exec.LookPath("gpg"); err != nil {
		t.Skip("gpg not installed")
	}
	key, err := Generate("awxnext collection hub", "")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	home := filepath.Join(dir, "gnupg")
	if err := os.Mkdir(home, 0o700); err != nil {
		t.Fatal(err)
	}
	manifest := filepath.Join(dir, "MANIFEST.json")
	os.WriteFile(manifest, []byte(`{"format": 1}`), 0o644)
	keyring, err := Keyring(key.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	krPath := filepath.Join(dir, "hub.gpg")
	os.WriteFile(krPath, keyring, 0o600)
	verify := func(sig string) string {
		t.Helper()
		cmd := exec.Command("gpg", "--status-fd=1", "--verify", "--batch", "--no-tty", "--no-default-keyring", "--keyring="+krPath, "-", manifest)
		cmd.Env = append(os.Environ(), "GNUPGHOME="+home)
		cmd.Stdin = strings.NewReader(sig)
		var out bytes.Buffer
		cmd.Stdout, cmd.Stderr = &out, &out
		cmd.Run()
		return out.String()
	}
	b, _ := os.ReadFile(manifest)
	sig, err := Sign(key.PrivateKey, b)
	if err != nil {
		t.Fatal(err)
	}
	// ansible-galaxy decodes gpg's status as UTF-8.
	if out := verify(sig); !strings.Contains(out, "VALIDSIG "+key.Fingerprint) || !utf8.ValidString(out) {
		t.Fatalf("gpg:\n%q", out)
	}
	os.WriteFile(manifest, []byte(`{"format": 2}`), 0o644)
	if out := verify(sig); !strings.Contains(out, "BADSIG") {
		t.Fatalf("gpg on a changed file:\n%s", out)
	}
}
