package auth

import (
	"context"
	"crypto/pbkdf2"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/crypto/bcrypt"

	"github.com/damhau/awxnext/internal/api"
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

// PasswordPolicy checks a local password against AWX's LOCAL_PASSWORD_MIN_*
// settings, in AWX's order and words: length, digits, uppercase
// characters, then special characters (anything not a letter or digit).
func PasswordPolicy(get func(string) int, pw string) error {
	count := func(f func(rune) bool) int {
		n := 0
		for _, r := range pw {
			if f(r) {
				n++
			}
		}
		return n
	}
	if n := get("LOCAL_PASSWORD_MIN_LENGTH"); n > 0 && utf8.RuneCountInString(pw) < n {
		return fmt.Errorf("Password must be at least %d characters long.", n)
	}
	if n := get("LOCAL_PASSWORD_MIN_DIGITS"); n > 0 && count(unicode.IsDigit) < n {
		return fmt.Errorf("Password must contain at least %d digits.", n)
	}
	if n := get("LOCAL_PASSWORD_MIN_UPPER"); n > 0 && count(unicode.IsUpper) < n {
		return fmt.Errorf("Password must contain at least %d uppercase characters.", n)
	}
	special := func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsNumber(r) }
	if n := get("LOCAL_PASSWORD_MIN_SPECIAL"); n > 0 && count(special) < n {
		return fmt.Errorf("Password must contain at least %d special characters.", n)
	}
	return nil
}

// EnsureSuperuser is createsuperuser: it creates the superuser, or makes
// an existing user one with this password. The password rules of local
// accounts apply.
func EnsureSuperuser(ctx context.Context, app *api.App, username, email, password string) error {
	if err := PasswordPolicy(func(k string) int { return app.Settings.Int(ctx, k) }, password); err != nil {
		return err
	}
	hash, err := HashPassword(password)
	if err != nil {
		return err
	}
	_, err = app.DB.Exec(ctx, `INSERT INTO users (username, email, password, is_superuser) VALUES ($1, $2, $3, true)
		ON CONFLICT (username) DO UPDATE SET password = EXCLUDED.password, is_superuser = true, is_active = true`, username, email, hash)
	return err
}
