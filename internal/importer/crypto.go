package importer

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// awxKey derives AWX's per-field key: sha512(SECRET_KEY + pk + field), split
// into a 32-byte HMAC key and a 32-byte AES-256 key (AWX's Fernet256).
func awxKey(secret string, pk *int64, field string) (signing, enc []byte) {
	h := sha512.New()
	h.Write([]byte(secret))
	if pk != nil {
		h.Write([]byte(strconv.FormatInt(*pk, 10)))
	}
	h.Write([]byte(field))
	sum := h.Sum(nil)
	return sum[:32], sum[32:]
}

// DecryptAWX decrypts a value written by AWX's encrypt_field
// ("$encrypted$UTF8$AESCBC$<base64 fernet token>"). Other values are returned unchanged.
func DecryptAWX(secret string, pk *int64, field, value string) (string, error) {
	if !strings.HasPrefix(value, "$encrypted$") {
		return value, nil
	}
	raw := strings.TrimPrefix(strings.TrimPrefix(value, "$encrypted$"), "UTF8$")
	algo, b64, ok := strings.Cut(raw, "$")
	if !ok || algo != "AESCBC" {
		return "", fmt.Errorf("unsupported AWX encryption %q", algo)
	}
	token, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return "", fmt.Errorf("decoding encrypted value: %w", err)
	}
	data, err := base64.URLEncoding.DecodeString(string(token))
	if err != nil {
		return "", fmt.Errorf("decoding fernet token: %w", err)
	}
	if len(data) < 1+8+16+16+32 || data[0] != 0x80 || (len(data)-1-8-16-32)%aes.BlockSize != 0 {
		return "", errors.New("malformed fernet token")
	}
	signing, enc := awxKey(secret, pk, field)
	mac := hmac.New(sha256.New, signing)
	mac.Write(data[:len(data)-32])
	if !hmac.Equal(mac.Sum(nil), data[len(data)-32:]) {
		return "", errors.New("invalid token: wrong SECRET_KEY or corrupted value")
	}
	block, err := aes.NewCipher(enc)
	if err != nil {
		return "", err
	}
	iv := data[9:25]
	ct := append([]byte{}, data[25:len(data)-32]...)
	cipher.NewCBCDecrypter(block, iv).CryptBlocks(ct, ct)
	pad := int(ct[len(ct)-1])
	if pad == 0 || pad > aes.BlockSize || pad > len(ct) {
		return "", errors.New("invalid padding")
	}
	for _, b := range ct[len(ct)-pad:] {
		if int(b) != pad {
			return "", errors.New("invalid padding")
		}
	}
	return string(ct[:len(ct)-pad]), nil
}
