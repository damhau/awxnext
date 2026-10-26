// Package server assembles the application: database, migrations, resources and routes.
package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/damhau/awxnext/internal/api"
	"github.com/damhau/awxnext/internal/auth"
	"github.com/damhau/awxnext/internal/config"
	"github.com/damhau/awxnext/internal/crypto"
	"github.com/damhau/awxnext/internal/db"
	"github.com/damhau/awxnext/internal/resources"
	"github.com/damhau/awxnext/internal/settings"
)

const devSecretKey = "awxnext-insecure-development-key"

// Setup connects to the database, applies migrations and bootstraps the app.
func Setup(ctx context.Context, cfg *config.Config) (*api.App, error) {
	if cfg.SecretKey == "" {
		if !cfg.DevMode {
			return nil, errors.New("AWXNEXT_SECRET_KEY is required (set AWXNEXT_DEV=1 for an insecure development key)")
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
	box, err := crypto.New(cfg.SecretKey)
	if err != nil {
		return nil, err
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
	return app.Router()
}
