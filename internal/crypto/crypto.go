// Package crypto encrypts secrets at rest with envelope encryption.
//
// Secrets are encrypted with AES-256-GCM data keys (DEKs). DEKs are stored in
// the database wrapped by a master key (KEK) that never touches the database:
// a local secret, HashiCorp Vault Transit or Azure Key Vault (see package kms).
//
// Ciphertexts are encoded as "$encrypted$v1$<dek id>$<base64(nonce|ciphertext)>".
package crypto

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/jackc/pgx/v5/pgxpool"
)

const prefix = "$encrypted$v1$"

// Placeholder is returned by the API instead of secret values.
const Placeholder = "$encrypted$"

// KEK wraps and unwraps data keys.
type KEK interface {
	// ID identifies the master key (provider and key); stored next to wrapped DEKs.
	ID() string
	Wrap(ctx context.Context, dek []byte) (string, error)
	Unwrap(ctx context.Context, wrapped string) ([]byte, error)
}

// Box encrypts and decrypts secrets using the database keyring.
type Box struct {
	mu     sync.RWMutex
	keys   map[string]cipher.AEAD
	active string
	kek    KEK
	db     *pgxpool.Pool
}

func newAEAD(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

// Open loads the keyring, creating the first data key if none exists.
func Open(ctx context.Context, db *pgxpool.Pool, kek KEK) (*Box, error) {
	b := &Box{keys: map[string]cipher.AEAD{}, kek: kek, db: db}
	if err := b.load(ctx); err != nil {
		return nil, err
	}
	if b.active == "" {
		if _, err := b.Rotate(ctx); err != nil {
			return nil, err
		}
	}
	return b, nil
}

func (b *Box) load(ctx context.Context) error {
	rows, err := b.db.Query(ctx, "SELECT id, wrapped, kek_id, active FROM data_keys ORDER BY created")
	if err != nil {
		return err
	}
	defer rows.Close()
	keys := map[string]cipher.AEAD{}
	active := ""
	for rows.Next() {
		var id, wrapped, kekID string
		var isActive bool
		if err := rows.Scan(&id, &wrapped, &kekID, &isActive); err != nil {
			return err
		}
		if kekID != b.kek.ID() {
			return fmt.Errorf("data key %s is wrapped by master key %q but the configured master key is %q (use 'awxnext keys rewrap' to migrate)", id, kekID, b.kek.ID())
		}
		raw, err := b.kek.Unwrap(ctx, wrapped)
		if err != nil {
			return fmt.Errorf("unwrap data key %s: %w", id, err)
		}
		aead, err := newAEAD(raw)
		if err != nil {
			return err
		}
		keys[id] = aead
		if isActive {
			active = id
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	b.mu.Lock()
	b.keys, b.active = keys, active
	b.mu.Unlock()
	return nil
}

// Rotate creates a new active data key. Existing ciphertexts stay readable.
func (b *Box) Rotate(ctx context.Context) (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	wrapped, err := b.kek.Wrap(ctx, raw)
	if err != nil {
		return "", fmt.Errorf("wrap data key: %w", err)
	}
	sum := sha256.Sum256(raw)
	id := hex.EncodeToString(sum[:6])
	tx, err := b.db.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer tx.Rollback(ctx)
	// Serialize rotation across replicas.
	if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(727275)"); err != nil {
		return "", err
	}
	var existing int
	tx.QueryRow(ctx, "SELECT count(*) FROM data_keys WHERE active").Scan(&existing)
	if existing > 0 && b.active == "" {
		// Another replica created the first key concurrently; use it.
		tx.Rollback(ctx)
		return "", b.load(ctx)
	}
	if _, err := tx.Exec(ctx, "UPDATE data_keys SET active = false WHERE active"); err != nil {
		return "", err
	}
	if _, err := tx.Exec(ctx, "INSERT INTO data_keys (id, wrapped, kek_id, active) VALUES ($1, $2, $3, true)", id, wrapped, b.kek.ID()); err != nil {
		return "", err
	}
	if err := tx.Commit(ctx); err != nil {
		return "", err
	}
	return id, b.load(ctx)
}

// Rewrap re-wraps every data key with a new master key. The Box keeps working with the new KEK.
func (b *Box) Rewrap(ctx context.Context, to KEK) error {
	b.mu.RLock()
	ids := make([]string, 0, len(b.keys))
	for id := range b.keys {
		ids = append(ids, id)
	}
	b.mu.RUnlock()
	tx, err := b.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	for _, id := range ids {
		var wrapped string
		if err := tx.QueryRow(ctx, "SELECT wrapped FROM data_keys WHERE id = $1 FOR UPDATE", id).Scan(&wrapped); err != nil {
			return err
		}
		raw, err := b.kek.Unwrap(ctx, wrapped)
		if err != nil {
			return err
		}
		nw, err := to.Wrap(ctx, raw)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, "UPDATE data_keys SET wrapped = $2, kek_id = $3 WHERE id = $1", id, nw, to.ID()); err != nil {
			return err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	b.kek = to
	return nil
}

// EscrowKey is an unwrapped data key in an export bundle.
type EscrowKey struct {
	ID     string `json:"id"`
	Key    string `json:"key"`
	Active bool   `json:"active"`
}

// Export returns all data keys unwrapped, for offline escrow. Handle with care.
func (b *Box) Export(ctx context.Context) ([]byte, error) {
	rows, err := b.db.Query(ctx, "SELECT id, wrapped, active FROM data_keys ORDER BY created")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var keys []EscrowKey
	for rows.Next() {
		var k EscrowKey
		var wrapped string
		if err := rows.Scan(&k.ID, &wrapped, &k.Active); err != nil {
			return nil, err
		}
		raw, err := b.kek.Unwrap(ctx, wrapped)
		if err != nil {
			return nil, err
		}
		k.Key = base64.StdEncoding.EncodeToString(raw)
		keys = append(keys, k)
	}
	return json.MarshalIndent(map[string]any{"version": 1, "keys": keys}, "", "  ")
}

// Import wraps escrowed data keys with kek and stores them, replacing existing rows with the same id.
func Import(ctx context.Context, db *pgxpool.Pool, kek KEK, bundle []byte) (int, error) {
	var in struct {
		Version int         `json:"version"`
		Keys    []EscrowKey `json:"keys"`
	}
	if err := json.Unmarshal(bundle, &in); err != nil {
		return 0, err
	}
	if in.Version != 1 {
		return 0, fmt.Errorf("unsupported escrow version %d", in.Version)
	}
	tx, err := db.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)
	for _, k := range in.Keys {
		raw, err := base64.StdEncoding.DecodeString(k.Key)
		if err != nil || len(raw) != 32 {
			return 0, fmt.Errorf("invalid key %s", k.ID)
		}
		wrapped, err := kek.Wrap(ctx, raw)
		if err != nil {
			return 0, err
		}
		if k.Active {
			if _, err := tx.Exec(ctx, "UPDATE data_keys SET active = false WHERE active AND id <> $1", k.ID); err != nil {
				return 0, err
			}
		}
		if _, err := tx.Exec(ctx, `INSERT INTO data_keys (id, wrapped, kek_id, active) VALUES ($1, $2, $3, $4)
			ON CONFLICT (id) DO UPDATE SET wrapped = EXCLUDED.wrapped, kek_id = EXCLUDED.kek_id, active = EXCLUDED.active`,
			k.ID, wrapped, kek.ID(), k.Active); err != nil {
			return 0, err
		}
	}
	return len(in.Keys), tx.Commit(ctx)
}

func IsEncrypted(s string) bool { return strings.HasPrefix(s, prefix) }

// Encrypt encrypts plain with the active data key. Empty and already-encrypted values are returned as-is.
func (b *Box) Encrypt(plain string) (string, error) {
	if plain == "" || IsEncrypted(plain) {
		return plain, nil
	}
	b.mu.RLock()
	id, aead := b.active, b.keys[b.active]
	b.mu.RUnlock()
	if aead == nil {
		return "", errors.New("no active data key")
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	ct := aead.Seal(nonce, nonce, []byte(plain), []byte(id))
	return prefix + id + "$" + base64.StdEncoding.EncodeToString(ct), nil
}

// Decrypt reverses Encrypt. Values that are not encrypted are returned as-is.
func (b *Box) Decrypt(s string) (string, error) {
	if !IsEncrypted(s) {
		return s, nil
	}
	rest := strings.TrimPrefix(s, prefix)
	id, data, ok := strings.Cut(rest, "$")
	if !ok {
		return "", errors.New("malformed ciphertext")
	}
	b.mu.RLock()
	aead := b.keys[id]
	b.mu.RUnlock()
	if aead == nil && b.db != nil {
		// Another replica may have rotated keys since we loaded the keyring.
		if err := b.load(context.Background()); err == nil {
			b.mu.RLock()
			aead = b.keys[id]
			b.mu.RUnlock()
		}
	}
	if aead == nil {
		return "", fmt.Errorf("ciphertext encrypted with unknown data key %s", id)
	}
	raw, err := base64.StdEncoding.DecodeString(data)
	if err != nil {
		return "", err
	}
	ns := aead.NonceSize()
	if len(raw) < ns {
		return "", errors.New("malformed ciphertext")
	}
	pt, err := aead.Open(nil, raw[:ns], raw[ns:], []byte(id))
	if err != nil {
		return "", err
	}
	return string(pt), nil
}

// LocalKEK derives a master key from a secret string (AWXNEXT_SECRET_KEY).
type LocalKEK struct {
	id   string
	aead cipher.AEAD
}

func NewLocalKEK(secret string) (*LocalKEK, error) {
	if secret == "" {
		return nil, errors.New("secret key is empty")
	}
	sum := sha256.Sum256([]byte("awxnext-kek:" + secret))
	aead, err := newAEAD(sum[:])
	if err != nil {
		return nil, err
	}
	fp := sha256.Sum256(sum[:])
	return &LocalKEK{id: "local:" + hex.EncodeToString(fp[:4]), aead: aead}, nil
}

func (k *LocalKEK) ID() string { return k.id }

func (k *LocalKEK) Wrap(_ context.Context, dek []byte) (string, error) {
	nonce := make([]byte, k.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(k.aead.Seal(nonce, nonce, dek, []byte(k.id))), nil
}

func (k *LocalKEK) Unwrap(_ context.Context, wrapped string) ([]byte, error) {
	raw, err := base64.StdEncoding.DecodeString(wrapped)
	if err != nil {
		return nil, err
	}
	ns := k.aead.NonceSize()
	if len(raw) < ns {
		return nil, errors.New("malformed wrapped key")
	}
	return k.aead.Open(nil, raw[:ns], raw[ns:], []byte(k.id))
}
