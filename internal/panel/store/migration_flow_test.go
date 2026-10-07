package store

import (
	"context"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
)

func TestSkippedPostgresReleaseImportsBeforeNewSchemaChanges(t *testing.T) {
	ctx := context.Background()
	s, err := OpenTest(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	// OpenTest is the existing isolated-schema fixture. Reset just its application
	// schema so the test can exercise a first import into a newer release.
	var schema string
	if err := s.DB.QueryRowContext(ctx, "SELECT current_schema()").Scan(&schema); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(schema, "mikan_test_") {
		t.Fatal("not a disposable test schema")
	}
	if _, err := s.DB.ExecContext(ctx, "DROP SCHEMA \""+schema+"\" CASCADE; CREATE SCHEMA \""+schema+"\""); err != nil {
		t.Fatal(err)
	}

	baseline, err := fs.ReadFile(postgresFS, "0001_baseline.sql")
	if err != nil {
		t.Fatal(err)
	}
	// Numbered past any real migration: this binary must take it for a newer release.
	future := fstest.MapFS{
		"0001_baseline.sql": &fstest.MapFile{Data: baseline},
		"9999_future.sql":   &fstest.MapFile{Data: []byte("-- +goose Up\nALTER TABLE users ADD COLUMN future_required TEXT NOT NULL DEFAULT 'preserved-default';\nCREATE TABLE future_table (id BIGINT PRIMARY KEY);\n")},
	}
	if err := migrateToImportBaseline(ctx, s.DB, future); err != nil {
		t.Fatal(err)
	}
	var version int64
	if err := s.DB.QueryRowContext(ctx, "SELECT max(version_id) FROM goose_db_version WHERE is_applied").Scan(&version); err != nil || version != 1 {
		t.Fatalf("baseline %d: %v", version, err)
	}
	dir := t.TempDir()
	legacy, err := openSQLite(ctx, filepath.Join(dir, "mikan.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := migrateSQLite(ctx, legacy); err != nil {
		t.Fatal(err)
	}
	if _, err := legacy.ExecContext(ctx, "INSERT INTO users(id,name,sub_token,period_start,created_at,updated_at) VALUES(41,'existing','existing-link',1,1,1)"); err != nil {
		t.Fatal(err)
	}
	legacy.Close()
	if _, err := ImportSQLite(ctx, s.DB, dir); err != nil {
		t.Fatal(err)
	}
	if err := migrateUp(ctx, s.DB, future); err != nil {
		t.Fatal(err)
	}
	var link, added string
	if err := s.DB.QueryRowContext(ctx, "SELECT sub_token,future_required FROM users WHERE id=41").Scan(&link, &added); err != nil || link != "existing-link" || added != "preserved-default" {
		t.Fatalf("import then schema migration: %q %q %v", link, added, err)
	}
	// Retrying retains the future schema and PostgreSQL writes; legacy SQLite is not
	// re-imported. The older binary must explicitly refuse a schema downgrade.
	if _, err := s.DB.ExecContext(ctx, "UPDATE users SET name='changed in PostgreSQL' WHERE id=41"); err != nil {
		t.Fatal(err)
	}
	if err := migrateToImportBaseline(ctx, s.DB, future); err != nil {
		t.Fatal(err)
	}
	if _, err := ImportSQLite(ctx, s.DB, dir); err != nil {
		t.Fatal(err)
	}
	if err := migrateUp(ctx, s.DB, future); err != nil {
		t.Fatal(err)
	}
	var name string
	if err := s.DB.QueryRowContext(ctx, "SELECT name FROM users WHERE id=41").Scan(&name); err != nil || name != "changed in PostgreSQL" {
		t.Fatal("retry replaced newer data:", name, err)
	}
	if err := migrateToImportBaseline(ctx, s.DB, postgresFS); err == nil || !strings.Contains(err.Error(), "downgrade refused") {
		t.Fatal("older binary accepted newer schema:", err)
	}
}
