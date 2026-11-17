package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"

	"github.com/damhau/awxnext/internal/config"
	"github.com/damhau/awxnext/internal/resources"
	"github.com/damhau/awxnext/internal/server"
)

// hubCommand runs awxnext hub import: the collections ansible-galaxy
// collection download wrote on a machine with internet access, into a hub
// without.
func hubCommand(ctx context.Context, cfg *config.Config, args []string) error {
	if len(args) == 0 || args[0] != "import" {
		usage()
	}
	fs := flag.NewFlagSet("hub import", flag.ExitOnError)
	repo := fs.String("repository", "published", "the repository to import into: published, staging, or a remote's")
	org := fs.String("organization", "", "the organization that owns the namespaces the import creates")
	keyring := fs.String("keyring", "", "a file of public keys the signatures must verify with: then a collection without such a signature is skipped")
	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), "Usage: awxnext hub import [--repository R] [--organization O] [--keyring FILE] DIR")
		fs.PrintDefaults()
	}
	fs.Parse(args[1:])
	if fs.NArg() != 1 {
		fs.Usage()
		os.Exit(2)
	}
	d := resources.CollectionDirectory{Dir: fs.Arg(0), Repository: *repo, Organization: *org}
	if *keyring != "" {
		keys, err := os.ReadFile(*keyring)
		if err != nil {
			return err
		}
		if len(keys) == 0 {
			return errors.New("the keyring is empty")
		}
		d.Keys = string(keys)
	}
	app, err := server.Setup(ctx, cfg)
	if err != nil {
		return err
	}
	return resources.ImportCollectionDirectory(ctx, app, os.Stdout, d)
}
