// Package crypto encrypts secrets at rest with AES-256-GCM.
//
// Ciphertexts are encoded as "$encrypted$v1$<key id>$<base64(nonce|ciphertext)>"
// so keys can be rotated later.
package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"strings"
)

const prefix = "$encrypted$v1$"

// Placeholder is returned by the API instead of secret values.
const Placeholder = "$encrypted$"

type Box struct {
	keyID string
	aead  cipher.AEAD
}

// New derives an AES-256 key from the secret key.
func New(secret string) (*Box, error) {
	if secret == "" {
		return nil, errors.New("secret key is empty")
	}
	sum := sha256.Sum256([]byte(secret))
	block, err := aes.NewCipher(sum[:])
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	kid := sha256.Sum256(sum[:])
	return &Box{keyID: hex.EncodeToString(kid[:4]), aead: aead}, nil
}

func IsEncrypted(s string) bool { return strings.HasPrefix(s, prefix) }

func (b *Box) Encrypt(plain string) (string, error) {
	if plain == "" || IsEncrypted(plain) {
		return plain, nil
	}
	nonce := make([]byte, b.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	ct := b.aead.Seal(nonce, nonce, []byte(plain), []byte(b.keyID))
	return prefix + b.keyID + "$" + base64.StdEncoding.EncodeToString(ct), nil
}

func (b *Box) Decrypt(s string) (string, error) {
	if !IsEncrypted(s) {
		return s, nil
	}
	rest := strings.TrimPrefix(s, prefix)
	kid, data, ok := strings.Cut(rest, "$")
	if !ok {
		return "", errors.New("malformed ciphertext")
	}
	if kid != b.keyID {
		return "", errors.New("ciphertext encrypted with unknown key " + kid)
	}
	raw, err := base64.StdEncoding.DecodeString(data)
	if err != nil {
		return "", err
	}
	ns := b.aead.NonceSize()
	if len(raw) < ns {
		return "", errors.New("malformed ciphertext")
	}
	pt, err := b.aead.Open(nil, raw[:ns], raw[ns:], []byte(kid))
	if err != nil {
		return "", err
	}
	return string(pt), nil
}
