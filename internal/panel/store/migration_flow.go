package store

import (
	"context"
	"database/sql"
	"fmt"
	"io/fs"
)

// SQLite's final normalized schema matches PostgreSQL baseline 1. A user may skip
// the first PostgreSQL release: importing must precede newer PostgreSQL-only schema
// changes, otherwise required new columns/tables would make that import impossible.
const sqliteImportBaseline int64 = 1

// MigrateResult is what Migrate did: the schema versions before and after it, and the
// SQLite import (nil without a legacy database).
type MigrateResult struct {
	Before, After int64
	Import        *ImportReport
}

// Migrate imports the legacy SQLite database, if any, and applies the PostgreSQL
// migrations. It refuses to run beside a running panel.
func Migrate(ctx context.Context, dsn, dataDir string) (MigrateResult, error) {
	var res MigrateResult
	conn, err := connectPostgres(ctx, dsn, maintenanceSession)
	if err != nil {
		return res, err
	}
	defer conn.Close()
	guard, err := conn.Conn(ctx)
	if err != nil {
		return res, err
	}
	defer guard.Close()
	unlock, err := lockOffline(ctx, guard)
	if err != nil {
		return res, err
	}
	defer unlock()
	if err := prepareImport(ctx, conn); err != nil {
		return res, err
	}
	if res.Before, err = schemaVersion(ctx, conn); err != nil {
		return res, err
	}
	if res.Import, err = ImportSQLite(ctx, conn, dataDir); err != nil {
		return res, err
	}
	// Migrations come only after the import commits and verifies. On a retry, ImportSQLite
	// reads its committed proof instead of copying stale SQLite over new PostgreSQL data;
	// pending migrations can then continue.
	if err := migrateUp(ctx, conn, postgresFS); err != nil {
		return res, err
	}
	res.After, err = schemaVersion(ctx, conn)
	return res, err
}

// openAtImportBaseline opens maintenance connections to a schema that has at least the
// import baseline, after bringing back what an interrupted restore set aside.
func openAtImportBaseline(ctx context.Context, dsn string) (*sql.DB, error) {
	conn, err := connectPostgres(ctx, dsn, maintenanceSession)
	if err != nil {
		return nil, err
	}
	if err := prepareImport(ctx, conn); err != nil {
		conn.Close()
		return nil, err
	}
	return conn, nil
}

func prepareImport(ctx context.Context, conn *sql.DB) error {
	if err := recoverRestore(ctx, conn); err != nil {
		return err
	}
	return migrateToImportBaseline(ctx, conn, postgresFS)
}

// migrateToImportBaseline creates only the import baseline on an empty destination.
// Existing PostgreSQL installations retain their current schema: this never rolls
// schema versions backwards, including on a repeated or interrupted migration.
func migrateToImportBaseline(ctx context.Context, conn *sql.DB, fsys fs.FS) error {
	p, err := postgresProvider(ctx, conn, fsys)
	if err != nil {
		return err
	}
	if _, err := p.UpTo(ctx, sqliteImportBaseline); err != nil {
		return fmt.Errorf("PostgreSQL import baseline: %w", err)
	}
	return nil
}

// migrateUp applies every pending PostgreSQL migration.
func migrateUp(ctx context.Context, conn *sql.DB, fsys fs.FS) error {
	p, err := postgresProvider(ctx, conn, fsys)
	if err != nil {
		return err
	}
	if _, err := p.Up(ctx); err != nil {
		return fmt.Errorf("PostgreSQL migrations: %w", err)
	}
	return nil
}

// schemaVersion is the applied PostgreSQL migration version.
func schemaVersion(ctx context.Context, conn *sql.DB) (int64, error) {
	p, err := postgresProvider(ctx, conn, postgresFS)
	if err != nil {
		return 0, err
	}
	return p.GetDBVersion(ctx)
}
