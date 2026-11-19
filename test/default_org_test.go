package test

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/damhau/awxnext/internal/api"
	"github.com/damhau/awxnext/internal/crypto"
	"github.com/damhau/awxnext/internal/resources"
	"github.com/damhau/awxnext/internal/settings"
)

func TestDefaultOrganization(t *testing.T) {
	ctx := context.Background()
	start := func(pool *pgxpool.Pool) {
		t.Helper()
		box, err := crypto.Open(ctx, pool, localKEK(t, "default-org"))
		if err != nil {
			t.Fatal(err)
		}
		if err := resources.Bootstrap(ctx, &api.App{DB: pool, Cfg: app.Cfg, Crypto: box, Settings: settings.NewStore(pool)}); err != nil {
			t.Fatal(err)
		}
	}
	names := func(pool *pgxpool.Pool) []string {
		t.Helper()
		rows, err := pool.Query(ctx, "SELECT name FROM organizations ORDER BY name")
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for rows.Next() {
			var n string
			rows.Scan(&n)
			out = append(out, n)
		}
		return out
	}

	fresh := freshDB(t)
	start(fresh)
	if got := names(fresh); len(got) != 1 || got[0] != "Default" {
		t.Fatalf("organizations of a new install: %v", got)
	}
	var galaxy string
	if err := fresh.QueryRow(ctx, `SELECT c.name FROM organization_galaxy_credentials g JOIN credentials c ON c.id = g.credential_id
		JOIN organizations o ON o.id = g.organization_id WHERE o.name = 'Default'`).Scan(&galaxy); err != nil || galaxy != "Ansible Galaxy" {
		t.Fatalf("Default's Galaxy credential: %q %v", galaxy, err)
	}
	var roles int
	fresh.QueryRow(ctx, `SELECT count(*) FROM object_roles WHERE content_type = 'organization'
		AND object_id = (SELECT id FROM organizations WHERE name = 'Default')`).Scan(&roles)
	if roles == 0 {
		t.Fatal("Default has no object roles")
	}
	start(fresh)
	if got := names(fresh); len(got) != 1 {
		t.Fatalf("organizations after a second start: %v", got)
	}
	if _, err := fresh.Exec(ctx, "DELETE FROM organizations"); err != nil {
		t.Fatal(err)
	}
	start(fresh)
	if got := names(fresh); len(got) != 1 || got[0] != "Default" {
		t.Fatalf("organizations after deleting every one: %v", got)
	}

	used := freshDB(t)
	if _, err := used.Exec(ctx, "INSERT INTO organizations (name) VALUES ('Ops')"); err != nil {
		t.Fatal(err)
	}
	start(used)
	if got := names(used); len(got) != 1 || got[0] != "Ops" {
		t.Fatalf("organizations of an install that has one: %v", got)
	}
}
