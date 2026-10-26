// Package server assembles the application: database, migrations, resources and routes.
package server

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"runtime"
	"sync"

	"github.com/damhau/awxnext/internal/agent"
	"github.com/damhau/awxnext/internal/api"
	"github.com/damhau/awxnext/internal/auth"
	"github.com/damhau/awxnext/internal/config"
	"github.com/damhau/awxnext/internal/crypto"
	"github.com/damhau/awxnext/internal/db"
	"github.com/damhau/awxnext/internal/jobs"
	"github.com/damhau/awxnext/internal/kms"
	"github.com/damhau/awxnext/internal/metrics"
	"github.com/damhau/awxnext/internal/resources"
	"github.com/damhau/awxnext/internal/runners/ansible"
	"github.com/damhau/awxnext/internal/runners/gitsync"
	"github.com/damhau/awxnext/internal/runners/system"
	"github.com/damhau/awxnext/internal/settings"
	"github.com/damhau/awxnext/internal/tracing"
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

var processMetrics sync.Once

// registerProcessMetrics exposes Go runtime and database pool gauges.
func registerProcessMetrics(app *api.App) {
	processMetrics.Do(func() {
		reg := metrics.Default
		reg.NewGaugeFunc("awxnext_goroutines", "Number of goroutines.", func() float64 { return float64(runtime.NumGoroutine()) })
		reg.NewGaugeFunc("awxnext_heap_alloc_bytes", "Bytes of allocated heap objects.", func() float64 {
			var ms runtime.MemStats
			runtime.ReadMemStats(&ms)
			return float64(ms.HeapAlloc)
		})
		reg.NewGaugeFunc("awxnext_db_pool_acquired_connections", "Database connections in use.", func() float64 { return float64(app.DB.Stat().AcquiredConns()) })
		reg.NewGaugeFunc("awxnext_db_pool_idle_connections", "Idle database connections.", func() float64 { return float64(app.DB.Stat().IdleConns()) })
		reg.NewGaugeFunc("awxnext_db_pool_max_connections", "Maximum database connections.", func() float64 { return float64(app.DB.Stat().MaxConns()) })
		reg.NewGaugeFunc("awxnext_db_pool_wait_seconds_total", "Time spent waiting for a database connection.", func() float64 {
			return app.DB.Stat().AcquireDuration().Seconds()
		})
	})
}

// ConfigureLogging installs the default slog handler (text or JSON).
func ConfigureLogging(cfg *config.Config) {
	var level slog.Level
	if err := level.UnmarshalText([]byte(cfg.LogLevel)); err != nil {
		level = slog.LevelInfo
	}
	opts := &slog.HandlerOptions{Level: level}
	var h slog.Handler = slog.NewTextHandler(os.Stderr, opts)
	if cfg.LogFormat == "json" {
		h = slog.NewJSONHandler(os.Stderr, opts)
	}
	slog.SetDefault(slog.New(tracing.LogHandler(h)))
}

// Handler installs routes and returns the HTTP handler.
func Handler(app *api.App) http.Handler {
	registerProcessMetrics(app)
	app.HandlePublic("GET", "/metrics", func(c *api.Ctx, w http.ResponseWriter, r *http.Request) {
		if tok := app.Cfg.MetricsToken; tok != "" && subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+tok)) != 1 {
			api.WriteError(w, api.ErrUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		metrics.Default.Write(w)
	})
	auth.Install(app)
	resources.Install(app)
	agent.Install(app)
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
	system.Register()
	resources.RegisterJobTypes()
	m := jobs.New(app)
	m.TickHooks = append(m.TickHooks, resources.ScheduleTick, resources.WorkflowTick)
	m.StartedHooks = append(m.StartedHooks, resources.JobStartedNotifications, resources.WebhookStatusStarted)
	m.FinishedHooks = append(m.FinishedHooks, resources.JobFinishedNotifications, resources.WebhookStatusFinished)
	resources.Jobs = m
	m.Start(ctx)
	if agent.Default != nil {
		agent.Default.Start(ctx)
	}
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
