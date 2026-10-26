package auth

import (
	"crypto/pbkdf2"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"strconv"
	"strings"

	"golang.org/x/crypto/bcrypt"
)

// HashPassword hashes a password with bcrypt.
func HashPassword(pw string) (string, error) {
	b, err := bcrypt.GenerateFromPassword([]byte(pw), bcrypt.DefaultCost)
	if err != nil {
		return "", err
	}
	return "bcrypt$" + string(b), nil
}

// CheckPassword verifies bcrypt hashes and Django pbkdf2_sha256 hashes (imported from AWX).
func CheckPassword(hash, pw string) bool {
	switch {
	case strings.HasPrefix(hash, "bcrypt$"):
		return bcrypt.CompareHashAndPassword([]byte(strings.TrimPrefix(hash, "bcrypt$")), []byte(pw)) == nil
	case strings.HasPrefix(hash, "pbkdf2_sha256$"):
		parts := strings.Split(hash, "$")
		if len(parts) != 4 {
			return false
		}
		iter, err := strconv.Atoi(parts[1])
		if err != nil {
			return false
		}
		want, err := base64.StdEncoding.DecodeString(parts[3])
		if err != nil {
			return false
		}
		got, err := pbkdf2.Key(sha256.New, pw, []byte(parts[2]), iter, len(want))
		if err != nil {
			return false
		}
		return subtle.ConstantTimeCompare(got, want) == 1
	}
	return false
}
