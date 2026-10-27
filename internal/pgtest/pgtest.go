// Package pgtest gives tests a PostgreSQL server they may create databases on:
// the one in AWXNEXT_TEST_DATABASE_URL, or a throwaway PostgreSQL 16 started
// with embedded-postgres. Its binaries are downloaded once into the user cache
// directory, so later runs need no network.
package pgtest

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"time"

	embeddedpostgres "github.com/fergusstrange/embedded-postgres"
)

// URL returns a connection URL for the postgres database of a server and a
// function that stops the server when this package started it.
func URL() (string, func(), error) {
	if url := os.Getenv("AWXNEXT_TEST_DATABASE_URL"); url != "" {
		return url, func() {}, nil
	}
	port, err := freePort()
	if err != nil {
		return "", nil, err
	}
	cache, err := os.UserCacheDir()
	if err != nil {
		cache = os.TempDir()
	}
	dir, err := os.MkdirTemp("", "awxnext-pg-")
	if err != nil {
		return "", nil, err
	}
	logPath := filepath.Join(dir, "postgres.log")
	log, err := os.Create(logPath)
	if err != nil {
		os.RemoveAll(dir)
		return "", nil, err
	}
	pg := embeddedpostgres.NewDatabase(embeddedpostgres.DefaultConfig().
		Version(embeddedpostgres.V16).
		Port(uint32(port)).
		Username("postgres").
		Password("postgres").
		Database("postgres").
		RuntimePath(filepath.Join(dir, "runtime")).
		DataPath(filepath.Join(dir, "data")).
		CachePath(filepath.Join(cache, "awxnext", "embedded-postgres")).
		StartTimeout(time.Minute).
		StartParameters(map[string]string{
			// TCP only: some sandboxes forbid Unix sockets, and tests don't use them.
			"unix_socket_directories": "",
			"max_connections":         "300",
			// Durability is not needed for a database that is thrown away.
			"fsync": "off", "synchronous_commit": "off", "full_page_writes": "off",
		}).
		Logger(log))
	if err := pg.Start(); err != nil {
		log.Close()
		out, _ := os.ReadFile(logPath)
		os.RemoveAll(dir)
		return "", nil, fmt.Errorf("start embedded postgres: %w\n%s", err, out)
	}
	stop := func() {
		pg.Stop()
		log.Close()
		os.RemoveAll(dir)
	}
	return fmt.Sprintf("postgres://postgres:postgres@localhost:%d/postgres?sslmode=disable", port), stop, nil
}

func freePort() (int, error) {
	l, err := net.Listen("tcp", "localhost:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}
