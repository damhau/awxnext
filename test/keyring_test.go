package test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/damhau/awxnext/internal/crypto"
	"github.com/damhau/awxnext/internal/db"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// freshDB creates an empty, migrated database for tests that need isolation.
func freshDB(t *testing.T) *pgxpool.Pool {
	t.Helper()
	setup(t)
	ctx := context.Background()
	cfg := app.DB.Config().ConnConfig
	name := fmt.Sprintf("awxnext_kr_%d", time.Now().UnixNano())
	admin, err := pgx.Connect(ctx, fmt.Sprintf("postgres://%s@%s:%d/postgres?sslmode=disable", cfg.User, cfg.Host, cfg.Port))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+name); err != nil {
		t.Fatal(err)
	}
	pool, err := db.Connect(ctx, fmt.Sprintf("postgres://%s@%s:%d/%s?sslmode=disable", cfg.User, cfg.Host, cfg.Port, name))
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		pool.Close()
		admin.Exec(ctx, "DROP DATABASE "+name+" WITH (FORCE)")
		admin.Close(ctx)
	})
	return pool
}

func localKEK(t *testing.T, secret string) crypto.KEK {
	k, err := crypto.NewLocalKEK(secret)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func TestKeyringRotationAndReplicas(t *testing.T) {
	pool := freshDB(t)
	ctx := context.Background()
	kek := localKEK(t, "master-a")
	a, err := crypto.Open(ctx, pool, kek)
	if err != nil {
		t.Fatal(err)
	}
	b, err := crypto.Open(ctx, pool, kek) // second replica
	if err != nil {
		t.Fatal(err)
	}
	old, _ := a.Encrypt("hunter2")
	if _, err := a.Rotate(ctx); err != nil {
		t.Fatal(err)
	}
	fresh, _ := a.Encrypt("hunter3")
	if strings.Split(old, "$")[3] == strings.Split(fresh, "$")[3] {
		t.Fatal("rotation did not change the data key")
	}
	for _, box := range []*crypto.Box{a, b} {
		for ct, want := range map[string]string{old: "hunter2", fresh: "hunter3"} {
			got, err := box.Decrypt(ct)
			if err != nil || got != want {
				t.Fatalf("decrypt: %q %v", got, err)
			}
		}
	}
	var n int
	pool.QueryRow(ctx, "SELECT count(*) FROM data_keys WHERE wrapped LIKE '%hunter%'").Scan(&n)
	if n != 0 {
		t.Fatal("plaintext found in data_keys")
	}
}

func TestKeyringWrongMasterKey(t *testing.T) {
	pool := freshDB(t)
	ctx := context.Background()
	if _, err := crypto.Open(ctx, pool, localKEK(t, "right")); err != nil {
		t.Fatal(err)
	}
	_, err := crypto.Open(ctx, pool, localKEK(t, "wrong"))
	if err == nil || !strings.Contains(err.Error(), "rewrap") {
		t.Fatalf("expected a helpful master key mismatch error, got %v", err)
	}
}

func TestKeyringEscrowRecovery(t *testing.T) {
	pool := freshDB(t)
	ctx := context.Background()
	box, err := crypto.Open(ctx, pool, localKEK(t, "lost-key"))
	if err != nil {
		t.Fatal(err)
	}
	ct, _ := box.Encrypt("precious")
	bundle, err := box.Export(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// The original master key is lost: wipe the wrapped keys and restore from escrow with a new one.
	pool.Exec(ctx, "DELETE FROM data_keys")
	if _, err := crypto.Import(ctx, pool, localKEK(t, "new-key"), bundle); err != nil {
		t.Fatal(err)
	}
	restored, err := crypto.Open(ctx, pool, localKEK(t, "new-key"))
	if err != nil {
		t.Fatal(err)
	}
	if got, err := restored.Decrypt(ct); err != nil || got != "precious" {
		t.Fatalf("recovered decrypt: %q %v", got, err)
	}
}

func TestKeyringRewrap(t *testing.T) {
	pool := freshDB(t)
	ctx := context.Background()
	box, err := crypto.Open(ctx, pool, localKEK(t, "old"))
	if err != nil {
		t.Fatal(err)
	}
	ct, _ := box.Encrypt("migrate-me")
	if err := box.Rewrap(ctx, localKEK(t, "new")); err != nil {
		t.Fatal(err)
	}
	if _, err := crypto.Open(ctx, pool, localKEK(t, "old")); err == nil {
		t.Fatal("old master key still accepted after rewrap")
	}
	nb, err := crypto.Open(ctx, pool, localKEK(t, "new"))
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := nb.Decrypt(ct); got != "migrate-me" {
		t.Fatalf("decrypt after rewrap: %q", got)
	}
}
