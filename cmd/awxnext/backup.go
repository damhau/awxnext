package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/damhau/awxnext/internal/backup"
	"github.com/damhau/awxnext/internal/config"
	"github.com/damhau/awxnext/internal/server"
)

// passphrase reads --passphrase-file, else AWXNEXT_BACKUP_PASSPHRASE.
func passphrase(file string) (string, error) {
	if file == "" {
		return os.Getenv("AWXNEXT_BACKUP_PASSPHRASE"), nil
	}
	b, err := os.ReadFile(file)
	if err != nil {
		return "", err
	}
	p := strings.TrimRight(string(b), "\r\n")
	if p == "" {
		return "", errors.New("the passphrase file is empty")
	}
	return p, nil
}

func runBackup(ctx context.Context, cfg *config.Config, args []string) error {
	fs := flag.NewFlagSet("backup", flag.ExitOnError)
	output := fs.String("output", "", "backup file (default awxnext-backup-<time>.tar.gz; - for stdout)")
	fs.StringVar(output, "o", "", "same as --output")
	pass := fs.String("passphrase-file", "", "file holding a passphrase to put the data keys' escrow in the backup, encrypted (or AWXNEXT_BACKUP_PASSPHRASE)")
	fs.Parse(args)
	pw, err := passphrase(*pass)
	if err != nil {
		return err
	}
	app, err := server.Setup(ctx, cfg)
	if err != nil {
		return err
	}
	name := *output
	if name == "" {
		name = "awxnext-backup-" + time.Now().UTC().Format("20060102-150405") + ".tar.gz"
	}
	var w io.Writer = os.Stdout
	var f *os.File
	if name != "-" {
		if f, err = os.OpenFile(name, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o600); err != nil {
			return err
		}
		w = f
	}
	m, err := backup.Backup(ctx, app, w, pw)
	if f != nil {
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			os.Remove(name)
		}
	}
	if err != nil {
		return err
	}
	if name != "-" {
		fmt.Fprintf(os.Stderr, "backup of %s written to %s\n", m.InstallUUID, name)
	}
	if !m.Keys {
		fmt.Fprintln(os.Stderr, "the data keys' escrow is not in the backup (no passphrase): restoring it needs this install's master key")
	}
	return nil
}

func runRestore(ctx context.Context, cfg *config.Config, args []string) error {
	fs := flag.NewFlagSet("restore", flag.ExitOnError)
	pass := fs.String("passphrase-file", "", "file holding the passphrase of the backup's key escrow (or AWXNEXT_BACKUP_PASSPHRASE)")
	fs.Parse(args)
	if fs.NArg() != 1 {
		return errors.New("usage: awxnext restore [--passphrase-file FILE] BACKUP")
	}
	pw, err := passphrase(*pass)
	if err != nil {
		return err
	}
	var r io.Reader = os.Stdin
	if fs.Arg(0) != "-" {
		f, err := os.Open(fs.Arg(0))
		if err != nil {
			return err
		}
		defer f.Close()
		r = f
	}
	logf := func(format string, a ...any) { fmt.Fprintf(os.Stderr, format+"\n", a...) }
	if _, err := backup.Restore(ctx, cfg, r, backup.RestoreOptions{Passphrase: pw, Log: logf}); err != nil {
		return err
	}
	logf("restored; start the server to run the queued project updates")
	return nil
}
