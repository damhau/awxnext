package credtypes

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"testing"

	"golang.org/x/crypto/ssh"
)

func TestKeyEncrypted(t *testing.T) {
	rsaKey, err := rsa.GenerateKey(rand.Reader, 1024)
	if err != nil {
		t.Fatal(err)
	}
	_, edKey, _ := ed25519.GenerateKey(rand.Reader)
	enc := func(b *pem.Block) string { return string(pem.EncodeToMemory(b)) }
	openssh, _ := ssh.MarshalPrivateKey(edKey, "")
	opensshEnc, _ := ssh.MarshalPrivateKeyWithPassphrase(edKey, "", []byte("pw"))
	pkcs1 := &pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(rsaKey)}
	// Deprecated in Go, but users still have legacy encrypted PEM keys.
	pkcs1Enc, _ := x509.EncryptPEMBlock(rand.Reader, pkcs1.Type, pkcs1.Bytes, []byte("pw"), x509.PEMCipherAES256)
	pkcs8, _ := x509.MarshalPKCS8PrivateKey(rsaKey)

	for name, tc := range map[string]struct {
		key  string
		want bool
	}{
		"openssh":              {enc(openssh), false},
		"openssh encrypted":    {enc(opensshEnc), true},
		"pkcs1":                {enc(pkcs1), false},
		"pkcs1 encrypted":      {enc(pkcs1Enc), true},
		"pkcs8":                {enc(&pem.Block{Type: "PRIVATE KEY", Bytes: pkcs8}), false},
		"pkcs8 encrypted":      {enc(&pem.Block{Type: "ENCRYPTED PRIVATE KEY", Bytes: []byte("opaque")}), true},
		"certificate then key": {enc(&pem.Block{Type: "CERTIFICATE", Bytes: []byte("c")}) + enc(pkcs1Enc), true},
		"not a key":            {"not a key", false},
		"empty":                {"", false},
	} {
		if got := KeyEncrypted(tc.key); got != tc.want {
			t.Errorf("%s: KeyEncrypted = %v, want %v", name, got, tc.want)
		}
	}
}
