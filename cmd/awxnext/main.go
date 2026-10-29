// Command awxnext is the AWX Next control plane.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/damhau/awxnext/internal/agent"
	"github.com/damhau/awxnext/internal/api"
	"github.com/damhau/awxnext/internal/auth"
	"github.com/damhau/awxnext/internal/config"
	"github.com/damhau/awxnext/internal/crypto"
	"github.com/damhau/awxnext/internal/importer"
	"github.com/damhau/awxnext/internal/kms"
	"github.com/damhau/awxnext/internal/runners/ansible"
	"github.com/damhau/awxnext/internal/server"
	"github.com/damhau/awxnext/internal/tracing"
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
  import-awx        import the configuration of an AWX installation from its database
  inventory-import  import an inventory file, directory or script (as awx-manage inventory_import)
  agent             run a remote execution node agent (dials back to the control plane)
  shim              run one job inside a container group pod (started by the control plane)
  install-shim DEST copy this binary to DEST (job pod init container)
  version           print the version
`)
	os.Exit(2)
}

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	cfg := config.Load()
	server.ConfigureLogging(cfg)
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
	case "import-awx":
		err = importAWX(ctx, cfg, os.Args[2:])
	case "inventory-import":
		err = inventoryImport(ctx, cfg, os.Args[1:])
	case "agent":
		err = runAgent(ctx, os.Args[2:])
	case "shim":
		err = runShim(ctx, os.Args[2:])
	case "install-shim":
		if len(os.Args) != 3 {
			usage()
		}
		err = agent.InstallShim(os.Args[2])
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
	if err := server.Harden(); err != nil {
		slog.Warn("could not hide the server's environment from other processes of its user", "err", err)
	}
	switch cfg.ContainerRuntime {
	case "none", "process", "":
		slog.Warn("jobs run as processes of the server's user (AWXNEXT_CONTAINER_RUNTIME=none): a playbook can read the files of other running jobs and change project checkouts; use this runtime for one trusted team only")
	}
	if cfg.SecretKey == "change-me" {
		slog.Warn("AWXNEXT_SECRET_KEY is the example value of the Compose file; set your own before storing credentials")
	}
	if u, err := url.Parse(cfg.DatabaseURL); err == nil {
		if pw, _ := u.User.Password(); pw == "awxnext" {
			slog.Warn("the database password is the example value of the Compose file; set AWXNEXT_DB_PASSWORD when creating the database")
		}
	}
	stopTracing, err := tracing.Setup(ctx, api.ProductVersion, cfg.NodeName)
	if err != nil {
		return fmt.Errorf("tracing: %w", err)
	}
	defer func() {
		flush, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		stopTracing(flush)
	}()
	app, err := server.Setup(ctx, cfg)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(cfg.ProjectsRoot, 0o755); err != nil {
		return fmt.Errorf("projects root: %w", err)
	}
	jm := server.StartJobs(ctx, app)
	defer jm.Wait()
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

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func runAgent(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("agent", flag.ExitOnError)
	cfg := agent.Config{Version: api.ProductVersion}
	fs.StringVar(&cfg.URL, "url", os.Getenv("AWXNEXT_AGENT_URL"), "control plane URL (AWXNEXT_AGENT_URL)")
	fs.StringVar(&cfg.Token, "token", os.Getenv("AWXNEXT_AGENT_TOKEN"), "agent token from /api/v2/instances/N/agent_token/ (AWXNEXT_AGENT_TOKEN)")
	fs.StringVar(&cfg.Runtime, "runtime", envOr("AWXNEXT_AGENT_RUNTIME", "podman"), "podman, docker or none (ansible-runner installed on the node)")
	fs.StringVar(&cfg.RunnerCommand, "runner", envOr("AWXNEXT_RUNNER_CMD", "ansible-runner"), "ansible-runner binary for runtime none")
	fs.StringVar(&cfg.WorkDir, "work-dir", envOr("AWXNEXT_AGENT_WORK_DIR", "/var/lib/awxnext-agent"), "directory for job files")
	fs.IntVar(&cfg.Capacity, "capacity", 0, "forks capacity (default: computed from CPU and memory)")
	fs.StringVar(&cfg.CACert, "ca-cert", os.Getenv("AWXNEXT_AGENT_CA_CERT"), "PEM file with the CA of the control plane certificate")
	fs.BoolVar(&cfg.InsecureSkipVerify, "insecure", false, "skip TLS verification (testing only)")
	fs.Parse(args)
	if cfg.URL == "" || cfg.Token == "" {
		return errors.New("--url and --token are required")
	}
	return agent.RunAgent(ctx, cfg)
}

func runShim(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("shim", flag.ExitOnError)
	url := fs.String("url", "", "work unit URL")
	cfg := agent.Config{Runtime: "none", ExactWorkDir: true, Token: os.Getenv("AWXNEXT_SHIM_TOKEN")}
	fs.StringVar(&cfg.WorkDir, "work-dir", "/runner", "private data dir")
	fs.StringVar(&cfg.RunnerCommand, "runner", "ansible-runner", "ansible-runner binary")
	fs.BoolVar(&cfg.InsecureSkipVerify, "insecure", os.Getenv("AWXNEXT_SHIM_INSECURE") != "", "skip TLS verification")
	fs.Parse(args)
	if *url == "" || cfg.Token == "" {
		return errors.New("--url and AWXNEXT_SHIM_TOKEN are required")
	}
	return agent.RunShim(ctx, cfg, *url)
}

// inventoryImport is awx-manage inventory_import: the same options, the
// import recorded as a `file` inventory source with an update.
func inventoryImport(ctx context.Context, cfg *config.Config, argv []string) error {
	fs := flag.NewFlagSet("inventory-import", flag.ExitOnError)
	o := ansible.FileImport{Args: append([]string{"awxnext"}, argv...)}
	fs.StringVar(&o.InventoryName, "inventory-name", "", "name of inventory to sync")
	fs.Int64Var(&o.InventoryID, "inventory-id", 0, "id of inventory to sync")
	fs.BoolVar(&o.Overwrite, "overwrite", false, "overwrite the destination hosts and groups")
	fs.BoolVar(&o.OverwriteVars, "overwrite-vars", false, "overwrite (rather than merge) variables")
	fs.Bool("keep-vars", false, "DEPRECATED legacy option, has no effect")
	fs.StringVar(&o.Source, "source", "", "inventory directory, file, or script to load")
	fs.StringVar(&o.EnabledVar, "enabled-var", "", `host variable used to set/clear enabled flag when host is online/offline, may be specified as "foo.bar" to traverse nested dicts`)
	fs.StringVar(&o.EnabledValue, "enabled-value", "", "value of host variable specified by --enabled-var that indicates host is enabled/online")
	fs.StringVar(&o.GroupFilter, "group-filter", "", "regular expression to filter group name(s); only matches are imported")
	fs.StringVar(&o.HostFilter, "host-filter", "", "regular expression to filter host name(s); only matches are imported")
	fs.BoolVar(&o.ExcludeEmptyGroups, "exclude-empty-groups", false, "when set, exclude all groups that have no child groups, hosts, or variables")
	fs.StringVar(&o.InstanceIDVar, "instance-id-var", "", `host variable that specifies the unique, immutable instance ID, may be specified as "foo.bar" to traverse nested dicts`)
	fs.IntVar(&o.Verbosity, "v", 0, "verbosity (0-3)")
	fs.Parse(argv[1:])
	app, err := server.Setup(ctx, cfg)
	if err != nil {
		return err
	}
	_, err = ansible.ImportFile(ctx, app, o, os.Stdout)
	return err
}

func importAWX(ctx context.Context, cfg *config.Config, args []string) error {
	fs := flag.NewFlagSet("import-awx", flag.ExitOnError)
	src := fs.String("source", os.Getenv("AWX_DATABASE_URL"), "AWX database URL, e.g. postgres://awx:pass@host/awx (AWX_DATABASE_URL)")
	key := fs.String("secret-key", os.Getenv("AWX_SECRET_KEY"), "AWX SECRET_KEY used to decrypt credentials (AWX_SECRET_KEY)")
	dry := fs.Bool("dry-run", false, "import in a transaction that is rolled back, and report")
	fs.Parse(args)
	if *src == "" || *key == "" {
		return errors.New("--source and --secret-key are required")
	}
	app, err := server.Setup(ctx, cfg)
	if err != nil {
		return err
	}
	rep, err := importer.Run(ctx, app, importer.Options{SourceURL: *src, SecretKey: *key, DryRun: *dry,
		Log: func(f string, a ...any) { slog.Info(fmt.Sprintf(f, a...)) }})
	if rep != nil {
		out, _ := json.MarshalIndent(rep, "", "  ")
		fmt.Println(string(out))
	}
	if err == nil && *dry {
		fmt.Println("dry run: nothing was written")
	}
	return err
}
