// Package storetest opens stores for tests, each in a PostgreSQL schema of its own in the
// disposable database MIKAN_TEST_DATABASE_URL names. It stays out of the panel binary.
package storetest

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/jackc/pgx/v5"

	"mikan/internal/panel/store"
)

// Open gives each fixture its own PostgreSQL schema, dropped when the store closes.
// Reopening the same directory uses the same schema, like the old file-backed fixtures did.
func Open(ctx context.Context, dataDir string) (*store.Store, error) {
	dsn := os.Getenv("MIKAN_TEST_DATABASE_URL")
	if dsn == "" {
		return nil, errors.New("tests require MIKAN_TEST_DATABASE_URL pointing to a disposable PostgreSQL database")
	}
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return nil, err
	}
	path := filepath.Join(dataDir, ".test-postgres-schema")
	name, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		name = []byte(newSchemaName())
		if err := os.WriteFile(path, name, 0o600); err != nil {
			return nil, err
		}
	} else if err != nil {
		return nil, err
	}
	return openSchema(ctx, dsn, string(name))
}

// newSchemaName is a fresh name for a test schema.
func newSchemaName() string {
	var token [12]byte
	rand.Read(token[:])
	return "mikan_test_" + hex.EncodeToString(token[:])
}

// openSchema opens schema in dsn's database, creating it first; it is dropped when the
// store closes.
func openSchema(ctx context.Context, dsn, schema string) (*store.Store, error) {
	quoted := pgx.Identifier{schema}.Sanitize()
	if err := exec(ctx, dsn, "CREATE SCHEMA IF NOT EXISTS "+quoted); err != nil {
		return nil, err
	}
	return store.OpenMigrated(ctx, withSearchPath(dsn, schema), func() error {
		return exec(context.Background(), dsn, "DROP SCHEMA IF EXISTS "+quoted+" CASCADE")
	})
}

// withSearchPath is dsn with its tables looked up in schema.
func withSearchPath(dsn, schema string) string {
	u, err := url.Parse(dsn)
	if err != nil || !strings.Contains(dsn, "://") {
		return dsn + " search_path=" + schema // keyword/value form
	}
	query := u.Query()
	query.Set("search_path", schema)
	u.RawQuery = query.Encode()
	return u.String()
}

func exec(ctx context.Context, dsn, sql string) error {
	c, err := pgx.Connect(ctx, dsn)
	if err != nil {
		return err
	}
	defer c.Close(context.Background())
	_, err = c.Exec(ctx, sql)
	return err
}
