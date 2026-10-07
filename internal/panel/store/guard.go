package store

import (
	"context"
	"database/sql"
	"errors"
)

// The panel guard keeps database migrate and restore off a running panel: the panel holds
// it shared for as long as it runs, they take it exclusively or refuse. A backup takes no
// part in it: pg_dump reads a consistent snapshot beside the running panel.
const panelGuard = "panel"

// LockPanel holds the panel guard shared on a connection of its own until release. A
// migrate or restore in progress makes it fail rather than wait: the panel would start on
// a schema in the middle of a change.
func LockPanel(ctx context.Context, dsn string) (release func() error, err error) {
	conn, err := connectPostgres(ctx, dsn, runtimeSession)
	if err != nil {
		return nil, err
	}
	conn.SetMaxOpenConns(1)
	c, err := conn.Conn(ctx)
	if err != nil {
		conn.Close()
		return nil, err
	}
	// Closing the pool ends the session, and the session's locks with it.
	release = func() error { return errors.Join(c.Close(), conn.Close()) }
	_, id, err := schemaLock(ctx, c, panelGuard)
	var locked bool
	if err == nil {
		err = c.QueryRowContext(ctx, "SELECT pg_try_advisory_lock_shared($1)", id).Scan(&locked)
	}
	if err == nil && !locked {
		err = errors.New("a database migrate or restore is running; start the panel after it ends")
	}
	if err != nil {
		release()
		return nil, err
	}
	return release, nil
}

// lockOffline takes the panel guard exclusively on c's session; unlock gives it back.
func lockOffline(ctx context.Context, c *sql.Conn) (unlock func(), err error) {
	_, id, err := schemaLock(ctx, c, panelGuard)
	if err != nil {
		return nil, err
	}
	var locked bool
	if err := c.QueryRowContext(ctx, "SELECT pg_try_advisory_lock($1)", id).Scan(&locked); err != nil {
		return nil, err
	}
	if !locked {
		return nil, errors.New("the panel is running (or another database migrate or restore is): stop it first")
	}
	return func() { c.ExecContext(context.WithoutCancel(ctx), "SELECT pg_advisory_unlock($1)", id) }, nil
}
