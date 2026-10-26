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
	"github.com/damhau/awxnext/internal/server"
)

func usage() {
	fmt.Fprintf(os.Stderr, `Usage: awxnext <command> [flags]

Commands:
  serve             run the API server and dispatcher
  migrate           apply database migrations
  createsuperuser   create (or update) a superuser
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
