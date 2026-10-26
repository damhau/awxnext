// Package server assembles the application: database, migrations, resources and routes.
package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"

	"github.com/damhau/awxnext/internal/api"
	"github.com/damhau/awxnext/internal/auth"
	"github.com/damhau/awxnext/internal/config"
	"github.com/damhau/awxnext/internal/crypto"
	"github.com/damhau/awxnext/internal/db"
	"github.com/damhau/awxnext/internal/jobs"
	"github.com/damhau/awxnext/internal/kms"
	"github.com/damhau/awxnext/internal/resources"
	"github.com/damhau/awxnext/internal/runners/ansible"
	"github.com/damhau/awxnext/internal/runners/gitsync"
	"github.com/damhau/awxnext/internal/settings"
	"github.com/damhau/awxnext/internal/ws"
	"github.com/jackc/pgx/v5/pgxpool"
)

const devSecretKey = "awxnext-insecure-development-key"

// Setup connects to the database, applies migrations and bootstraps the app.
func Setup(ctx context.Context, cfg *config.Config) (*api.App, error) {
	if cfg.SecretKey == "" && (os.Getenv("AWXNEXT_KMS") == "" || os.Getenv("AWXNEXT_KMS") == "local") {
		if !cfg.DevMode {
			return nil, errors.New("AWXNEXT_SECRET_KEY (or AWXNEXT_KMS) is required (set AWXNEXT_DEV=1 for an insecure development key)")
		}
		slog.Warn("using insecure development secret key")
		cfg.SecretKey = devSecretKey
	}
	if cfg.DevMode {
		if err := db.EnsureDatabase(ctx, cfg.DatabaseURL); err != nil {
			return nil, fmt.Errorf("ensure database: %w", err)
		}
	}
	pool, err := db.Connect(ctx, cfg.DatabaseURL)
	if err != nil {
		return nil, err
	}
	if err := db.Migrate(ctx, pool); err != nil {
		return nil, err
	}
	kek, err := kms.FromEnv("AWXNEXT_", cfg.SecretKey)
	if err != nil {
		return nil, err
	}
	box, err := crypto.Open(ctx, pool, kek)
	if err != nil {
		return nil, fmt.Errorf("keyring: %w", err)
	}
	app := &api.App{DB: pool, Cfg: cfg, Crypto: box, Settings: settings.NewStore(pool)}
	if err := resources.Bootstrap(ctx, app); err != nil {
		return nil, fmt.Errorf("bootstrap: %w", err)
	}
	return app, nil
}

// Handler installs routes and returns the HTTP handler.
func Handler(app *api.App) http.Handler {
	auth.Install(app)
	resources.Install(app)
	ws.Install(app, func() *jobs.Hub {
		if resources.Jobs == nil {
			return nil
		}
		return resources.Jobs.Hub
	})
	return app.Router()
}

// StartJobs registers runners and starts dispatching and running jobs on this node.
func StartJobs(ctx context.Context, app *api.App) *jobs.Manager {
	gitsync.Register()
	ansible.Register()
	m := jobs.New(app)
	resources.Jobs = m
	m.Start(ctx)
	return m
}

// OpenKeyless connects and migrates without opening the keyring (used by key
// management commands), returning the configured master key.
func OpenKeyless(ctx context.Context, cfg *config.Config) (*pgxpool.Pool, crypto.KEK, error) {
	pool, err := db.Connect(ctx, cfg.DatabaseURL)
	if err != nil {
		return nil, nil, err
	}
	if err := db.Migrate(ctx, pool); err != nil {
		return nil, nil, err
	}
	kek, err := kms.FromEnv("AWXNEXT_", cfg.SecretKey)
	return pool, kek, err
}
