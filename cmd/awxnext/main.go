// Command awxnext is the AWX Next control plane.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/damhau/awxnext/internal/api"
	"github.com/damhau/awxnext/internal/auth"
	"github.com/damhau/awxnext/internal/config"
	"github.com/damhau/awxnext/internal/crypto"
	"github.com/damhau/awxnext/internal/kms"
	"github.com/damhau/awxnext/internal/server"
)

func usage() {
	fmt.Fprintf(os.Stderr, `Usage: awxnext <command> [flags]

Commands:
  serve             run the API server and dispatcher
  migrate           apply database migrations
  createsuperuser   create (or update) a superuser
  keys rotate       create a new active data encryption key
  keys export       print all data keys unwrapped (escrow; store offline!)
  keys import FILE  restore escrowed data keys, wrapped with the configured master key
  keys rewrap       re-wrap data keys from the AWXNEXT_OLD_* master key to the current one
  version           print the version
`)
	os.Exit(2)
}

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo})))
	cfg := config.Load()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	var err error
	switch os.Args[1] {
	case "serve":
		err = serve(ctx, cfg)
	case "migrate":
		_, err = server.Setup(ctx, cfg)
	case "createsuperuser":
		err = createSuperuser(ctx, cfg, os.Args[2:])
	case "keys":
		err = keys(ctx, cfg, os.Args[2:])
	case "version":
		fmt.Println(api.ProductVersion)
	default:
		usage()
	}
	if err != nil {
		slog.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func serve(ctx context.Context, cfg *config.Config) error {
	app, err := server.Setup(ctx, cfg)
	if err != nil {
		return err
	}
	srv := &http.Server{
		Addr:              cfg.Listen,
		Handler:           server.Handler(app),
		ReadHeaderTimeout: 10 * time.Second,
	}
	errc := make(chan error, 1)
	go func() {
		slog.Info("listening", "addr", cfg.Listen, "version", api.ProductVersion)
		errc <- srv.ListenAndServe()
	}()
	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	shutdown, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	return srv.Shutdown(shutdown)
}

func createSuperuser(ctx context.Context, cfg *config.Config, args []string) error {
	fs := flag.NewFlagSet("createsuperuser", flag.ExitOnError)
	username := fs.String("username", "admin", "username")
	password := fs.String("password", os.Getenv("AWXNEXT_ADMIN_PASSWORD"), "password (or AWXNEXT_ADMIN_PASSWORD)")
	email := fs.String("email", "", "email")
	fs.Parse(args)
	if *password == "" {
		return errors.New("a password is required")
	}
	app, err := server.Setup(ctx, cfg)
	if err != nil {
		return err
	}
	hash, err := auth.HashPassword(*password)
	if err != nil {
		return err
	}
	_, err = app.DB.Exec(ctx, `INSERT INTO users (username, email, password, is_superuser) VALUES ($1, $2, $3, true)
		ON CONFLICT (username) DO UPDATE SET password = EXCLUDED.password, is_superuser = true, is_active = true`,
		*username, *email, hash)
	if err == nil {
		fmt.Printf("superuser %q ready\n", *username)
	}
	return err
}

func keys(ctx context.Context, cfg *config.Config, args []string) error {
	if len(args) == 0 {
		usage()
	}
	switch args[0] {
	case "rotate":
		app, err := server.Setup(ctx, cfg)
		if err != nil {
			return err
		}
		id, err := app.Crypto.Rotate(ctx)
		if err == nil {
			fmt.Printf("new active data key %s\n", id)
		}
		return err
	case "export":
		app, err := server.Setup(ctx, cfg)
		if err != nil {
			return err
		}
		out, err := app.Crypto.Export(ctx)
		if err == nil {
			fmt.Println(string(out))
		}
		return err
	case "import":
		if len(args) < 2 {
			return errors.New("usage: awxnext keys import FILE")
		}
		bundle, err := os.ReadFile(args[1])
		if err != nil {
			return err
		}
		pool, kek, err := server.OpenKeyless(ctx, cfg)
		if err != nil {
			return err
		}
		n, err := crypto.Import(ctx, pool, kek, bundle)
		if err == nil {
			fmt.Printf("imported %d data keys\n", n)
		}
		return err
	case "rewrap":
		pool, newKEK, err := server.OpenKeyless(ctx, cfg)
		if err != nil {
			return err
		}
		oldKEK, err := kms.FromEnv("AWXNEXT_OLD_", "")
		if err != nil {
			return fmt.Errorf("old master key (AWXNEXT_OLD_*): %w", err)
		}
		box, err := crypto.Open(ctx, pool, oldKEK)
		if err != nil {
			return err
		}
		if err := box.Rewrap(ctx, newKEK); err != nil {
			return err
		}
		fmt.Printf("data keys re-wrapped with %s\n", newKEK.ID())
		return nil
	}
	usage()
	return nil
}
