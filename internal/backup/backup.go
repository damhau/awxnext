// Package backup is awxnext backup and awxnext restore: a gzip'd tar of a
// manifest, the database (pg_dump's custom format) and, with a passphrase,
// the escrow of the data keys encrypted with it.
package backup

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/jackc/pgx/v5"
	"golang.org/x/crypto/scrypt"

	"github.com/damhau/awxnext/internal/api"
	"github.com/damhau/awxnext/internal/config"
	"github.com/damhau/awxnext/internal/crypto"
	"github.com/damhau/awxnext/internal/rbac"
	"github.com/damhau/awxnext/internal/resources"
	"github.com/damhau/awxnext/internal/server"
)

// Manifest describes a backup.
type Manifest struct {
	Format      int       `json:"format"`
	Version     string    `json:"version"`
	Created     time.Time `json:"created"`
	InstallUUID string    `json:"install_uuid"`
	// Migration is the last database migration applied.
	Migration string `json:"migration"`
	// Keys says whether the data keys' escrow is in the backup.
	Keys bool `json:"keys"`
	// Config is the backed up install's settings that are not secrets,
	// to configure the restored one.
	Config map[string]string `json:"config"`
}

const format = 1

// tool returns the path of pg_dump or pg_restore: AWXNEXT_PG_DUMP or
// AWXNEXT_PG_RESTORE, else the PATH.
func tool(name, env string) (string, error) {
	if p := os.Getenv(env); p != "" {
		return p, nil
	}
	p, err := exec.LookPath(name)
	if err != nil {
		return "", fmt.Errorf("%s is needed (PostgreSQL's client tools, of the server's major version or newer; or set %s): %w", name, env, err)
	}
	return p, nil
}

// pgCommand runs a PostgreSQL client tool on the database, the password
// in PGPASSWORD rather than on the command line.
func pgCommand(ctx context.Context, bin, dbURL string, args ...string) (*exec.Cmd, error) {
	u, err := url.Parse(dbURL)
	if err != nil {
		return nil, fmt.Errorf("AWXNEXT_DATABASE_URL: %w", err)
	}
	env := os.Environ()
	if u.User != nil {
		if pw, ok := u.User.Password(); ok {
			env = append(env, "PGPASSWORD="+pw)
			u.User = url.User(u.User.Username())
		}
	}
	cmd := exec.CommandContext(ctx, bin, append(args, "--dbname="+u.String())...)
	cmd.Env = env
	return cmd, nil
}

// Backup writes a backup of the install app runs to w. With a passphrase,
// the data keys' escrow goes in, encrypted with it: the backup then
// restores under another master key.
func Backup(ctx context.Context, app *api.App, w io.Writer, passphrase string) (*Manifest, error) {
	bin, err := tool("pg_dump", "AWXNEXT_PG_DUMP")
	if err != nil {
		return nil, err
	}
	m := &Manifest{Format: format, Version: api.ProductVersion, Created: time.Now().UTC(), InstallUUID: app.Settings.String(ctx, "INSTALL_UUID"),
		Keys: passphrase != "", Config: settingsToRestore(app.Cfg)}
	app.DB.QueryRow(ctx, "SELECT max(version) FROM schema_migrations").Scan(&m.Migration)
	dir, err := os.MkdirTemp("", "awxnext-backup-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	dump := filepath.Join(dir, "database.dump")
	cmd, err := pgCommand(ctx, bin, app.Cfg.DatabaseURL, "--format=custom", "--no-owner", "--no-privileges", "--file="+dump)
	if err != nil {
		return nil, err
	}
	if out, err := cmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("pg_dump: %v: %s", err, out)
	}
	files := map[string]string{"database.dump": dump}
	if passphrase != "" {
		escrow, err := app.Crypto.Export(ctx)
		if err != nil {
			return nil, err
		}
		sealed, err := seal(passphrase, escrow)
		if err != nil {
			return nil, err
		}
		p := filepath.Join(dir, "keys.json.enc")
		if err := os.WriteFile(p, sealed, 0o600); err != nil {
			return nil, err
		}
		files["keys.json.enc"] = p
	}
	manifest, _ := json.MarshalIndent(m, "", "  ")
	zw := gzip.NewWriter(w)
	tw := tar.NewWriter(zw)
	add := func(name string, body []byte, f *os.File) error {
		hdr := &tar.Header{Name: name, Mode: 0o600, ModTime: m.Created}
		if f != nil {
			st, err := f.Stat()
			if err != nil {
				return err
			}
			hdr.Size = st.Size()
		} else {
			hdr.Size = int64(len(body))
		}
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		if f != nil {
			_, err := io.Copy(tw, f)
			return err
		}
		_, err := tw.Write(body)
		return err
	}
	if err := add("manifest.json", manifest, nil); err != nil {
		return nil, err
	}
	for _, name := range []string{"database.dump", "keys.json.enc"} {
		p, ok := files[name]
		if !ok {
			continue
		}
		f, err := os.Open(p)
		if err != nil {
			return nil, err
		}
		err = add(name, nil, f)
		f.Close()
		if err != nil {
			return nil, err
		}
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	return m, zw.Close()
}

// settingsToRestore is the configuration a restored install needs the
// same, or knowingly not: everything but the secrets.
func settingsToRestore(c *config.Config) map[string]string {
	kms := os.Getenv("AWXNEXT_KMS")
	if kms == "" {
		kms = "local"
	}
	return map[string]string{"AWXNEXT_KMS": kms, "AWXNEXT_BASE_URL": c.BaseURL, "AWXNEXT_PROJECTS_ROOT": c.ProjectsRoot, "AWXNEXT_JOBS_ROOT": c.JobsRoot,
		"AWXNEXT_CONTAINER_RUNTIME": c.ContainerRuntime, "AWXNEXT_DEFAULT_EE_IMAGE": c.DefaultEEImage}
}

// The escrow is sealed with AES-256-GCM under a key scrypt derives from the
// passphrase: salt, nonce, then the sealed bytes.
func seal(passphrase string, plain []byte) ([]byte, error) {
	salt := make([]byte, 16)
	rand.Read(salt)
	gcm, err := passphraseCipher(passphrase, salt)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	rand.Read(nonce)
	return append(append(salt, nonce...), gcm.Seal(nil, nonce, plain, []byte("awxnext-backup-keys"))...), nil
}

func open(passphrase string, sealed []byte) ([]byte, error) {
	if len(sealed) < 16+12 {
		return nil, errors.New("the key escrow is damaged")
	}
	gcm, err := passphraseCipher(passphrase, sealed[:16])
	if err != nil {
		return nil, err
	}
	plain, err := gcm.Open(nil, sealed[16:16+gcm.NonceSize()], sealed[16+gcm.NonceSize():], []byte("awxnext-backup-keys"))
	if err != nil {
		return nil, errors.New("wrong passphrase for the backup's key escrow")
	}
	return plain, nil
}

func passphraseCipher(passphrase string, salt []byte) (cipher.AEAD, error) {
	key, err := scrypt.Key([]byte(passphrase), salt, 1<<15, 8, 1, 32)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

// RestoreOptions are awxnext restore's.
type RestoreOptions struct {
	Passphrase string
	// Log reports progress.
	Log func(format string, args ...any)
}

// Restore restores a backup into the empty database cfg names: the
// database, then the data keys' escrow re-wrapped with this install's
// master key (with the passphrase), then the migrations of this version.
// Project checkouts are rebuilt: an update of every SCM project is queued.
func Restore(ctx context.Context, cfg *config.Config, r io.Reader, opt RestoreOptions) (*Manifest, error) {
	if opt.Log == nil {
		opt.Log = func(string, ...any) {}
	}
	bin, err := tool("pg_restore", "AWXNEXT_PG_RESTORE")
	if err != nil {
		return nil, err
	}
	dir, err := os.MkdirTemp("", "awxnext-restore-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	m, files, err := unpack(r, dir)
	if err != nil {
		return nil, err
	}
	if m.Format != format {
		return nil, fmt.Errorf("unsupported backup format %d", m.Format)
	}
	var escrow []byte
	if p, ok := files["keys.json.enc"]; ok && opt.Passphrase != "" {
		sealed, err := os.ReadFile(p)
		if err != nil {
			return nil, err
		}
		if escrow, err = open(opt.Passphrase, sealed); err != nil {
			return nil, err
		}
	} else if opt.Passphrase != "" {
		return nil, errors.New("the backup has no key escrow: restore it with the master key of the backed up install")
	}
	dump, ok := files["database.dump"]
	if !ok {
		return nil, errors.New("the backup has no database")
	}
	// Into an empty database only.
	conn, err := pgx.Connect(ctx, cfg.DatabaseURL)
	if err != nil {
		return nil, err
	}
	var tables int
	err = conn.QueryRow(ctx, "SELECT count(*) FROM pg_tables WHERE schemaname = current_schema()").Scan(&tables)
	conn.Close(ctx)
	if err != nil {
		return nil, err
	}
	if tables > 0 {
		return nil, errors.New("the database is not empty: restore into a new database")
	}
	opt.Log("restoring the database of %s (version %s, %s)", m.InstallUUID, m.Version, m.Created.Format(time.RFC3339))
	cmd, err := pgCommand(ctx, bin, cfg.DatabaseURL, "--no-owner", "--no-privileges", "--exit-on-error", "--single-transaction", dump)
	if err != nil {
		return nil, err
	}
	if out, err := cmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("pg_restore: %v: %s", err, out)
	}
	if escrow != nil {
		pool, kek, err := server.OpenKeyless(ctx, cfg)
		if err != nil {
			return nil, err
		}
		n, err := crypto.Import(ctx, pool, kek, escrow)
		pool.Close()
		if err != nil {
			return nil, fmt.Errorf("key escrow: %w", err)
		}
		opt.Log("re-wrapped %d data keys with this install's master key", n)
	}
	app, err := server.Setup(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("opening the restored install (is this the backed up install's master key, or give the passphrase of its key escrow?): %w", err)
	}
	defer app.DB.Close()
	// The backed up install's nodes are not this one's: jobs are not sent
	// to them until they show up (an agent reconnects, a control node
	// starts on this database).
	if _, err := app.DB.Exec(ctx, "UPDATE instances SET node_state = 'unavailable', last_seen = NULL WHERE hostname <> $1", cfg.NodeName); err != nil {
		return nil, err
	}
	for k, v := range m.Config {
		if now := settingsToRestore(cfg)[k]; now != v {
			opt.Log("note: %s was %q in the backed up install, %q here", k, v, now)
		}
	}
	n, err := rebuildCheckouts(ctx, app)
	if err != nil {
		return nil, err
	}
	opt.Log("queued %d project updates to rebuild the checkouts", n)
	return m, nil
}

// rebuildCheckouts queues an update of every SCM project, run when the
// server starts: the checkouts were not in the backup.
func rebuildCheckouts(ctx context.Context, app *api.App) (int, error) {
	c := &api.Ctx{Context: ctx, App: app, User: rbac.System(), Extra: map[string]any{}}
	rows, err := app.DB.Query(ctx, "SELECT id FROM unified_job_templates WHERE type = 'project' AND coalesce(scm_type, '') <> '' ORDER BY id")
	if err != nil {
		return 0, err
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if rows.Scan(&id) == nil {
			ids = append(ids, id)
		}
	}
	rows.Close()
	projects := api.Lookup("projects")
	n := 0
	for _, id := range ids {
		p, err := api.Get(c, projects, id)
		if err != nil {
			continue
		}
		if _, err := resources.LaunchProjectUpdate(c, p, "manual", "check", ""); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

// unpack reads a backup into dir.
func unpack(r io.Reader, dir string) (*Manifest, map[string]string, error) {
	zr, err := gzip.NewReader(r)
	if err != nil {
		return nil, nil, fmt.Errorf("not an awxnext backup: %w", err)
	}
	tr := tar.NewReader(zr)
	files := map[string]string{}
	var m *Manifest
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, nil, err
		}
		switch hdr.Name {
		case "manifest.json":
			m = &Manifest{}
			if err := json.NewDecoder(tr).Decode(m); err != nil {
				return nil, nil, fmt.Errorf("manifest: %w", err)
			}
		case "database.dump", "keys.json.enc":
			p := filepath.Join(dir, hdr.Name)
			f, err := os.OpenFile(p, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
			if err != nil {
				return nil, nil, err
			}
			_, err = io.Copy(f, tr)
			f.Close()
			if err != nil {
				return nil, nil, err
			}
			files[hdr.Name] = p
		}
	}
	if m == nil {
		return nil, nil, errors.New("not an awxnext backup: no manifest")
	}
	return m, files, nil
}
