package store

import (
	"context"
	"errors"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"mikan/internal/panel/store/db"
)

func tableExists(t *testing.T, s *Store, name string) bool {
	t.Helper()
	var exists bool
	if err := s.DB.QueryRowContext(context.Background(), "SELECT to_regclass($1) IS NOT NULL", name).Scan(&exists); err != nil {
		t.Fatal(err)
	}
	return exists
}

func schemaExists(t *testing.T, s *Store, name string) bool {
	t.Helper()
	var exists bool
	if err := s.DB.QueryRowContext(context.Background(), "SELECT EXISTS (SELECT 1 FROM pg_namespace WHERE nspname=$1)", name).Scan(&exists); err != nil {
		t.Fatal(err)
	}
	return exists
}

// A backup made before a newer migration restores into that migration's schema: what
// the backup never had must not survive, and a failed restore keeps everything.
func TestSQLiteRestoreReplacesNewerSchema(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	source := filepath.Join(dir, "mikan.db")
	legacy, err := openSQLite(ctx, source)
	if err != nil {
		t.Fatal(err)
	}
	if err := migrateSQLite(ctx, legacy); err != nil {
		t.Fatal(err)
	}
	// A REAL counter makes the import fail verification.
	if _, err := legacy.ExecContext(ctx, "INSERT INTO users(id,name,sub_token,period_start,created_at,updated_at,used_up) VALUES(41,'old','token',1,1,1,0.5)"); err != nil {
		t.Fatal(err)
	}
	legacy.Close()
	s, err := OpenTest(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	dsn := fixtureDSN(t, s)
	var schema string
	if err := s.DB.QueryRowContext(ctx, "SELECT current_schema()").Scan(&schema); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.ExecContext(ctx, "CREATE TABLE newer_migration (id BIGINT)"); err != nil {
		t.Fatal(err)
	}
	if err := s.Q.SetSetting(ctx, db.SetSettingParams{Key: "live", Value: "keep"}); err != nil {
		t.Fatal(err)
	}

	if err := RestoreSQLite(ctx, dsn, dir, source); err == nil {
		t.Fatal("invalid backup restored")
	}
	if v, _ := s.Q.GetSetting(ctx, "live"); v != "keep" || !tableExists(t, s, "newer_migration") {
		t.Fatal("failed restore changed the database")
	}
	if schemaExists(t, s, schema+restoreSuffix) {
		t.Fatal("failed restore left its aside schema")
	}

	legacy, err = openSQLite(ctx, source)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := legacy.ExecContext(ctx, "UPDATE users SET used_up=500"); err != nil {
		t.Fatal(err)
	}
	// Enough rows for COPY to send them in several messages.
	const hours = 20007
	if _, err := legacy.ExecContext(ctx, "WITH RECURSIVE h(n) AS (SELECT 1 UNION ALL SELECT n+1 FROM h WHERE n < ?) INSERT INTO traffic_hourly(user_id,hour,up,down) SELECT 41,n,n,2*n FROM h", hours); err != nil {
		t.Fatal(err)
	}
	legacy.Close()
	if err := RestoreSQLite(ctx, dsn, dir, source); err != nil {
		t.Fatal(err)
	}
	if u, err := s.Q.GetUser(ctx, 41); err != nil || u.UsedUp != 500 {
		t.Fatal("restored user missing", u, err)
	}
	var rows, down int64
	if err := s.DB.QueryRowContext(ctx, "SELECT count(*), sum(down) FROM traffic_hourly WHERE user_id=41").Scan(&rows, &down); err != nil || rows != hours || down != hours*(hours+1) {
		t.Fatal("batched rows lost", rows, down, err)
	}
	if tableExists(t, s, "newer_migration") {
		t.Fatal("an object the backup never had survived the restore")
	}
	if schemaExists(t, s, schema+restoreSuffix) {
		t.Fatal("the previous schema was not dropped")
	}
}

// A restore cut short by a crash never reported success: the next start brings the
// previous data back, but never while a restore still runs.
func TestInterruptedRestoreComesBack(t *testing.T) {
	ctx := context.Background()
	s, err := OpenTest(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	dsn := fixtureDSN(t, s)
	if err := s.Q.SetSetting(ctx, db.SetSettingParams{Key: "before", Value: "keep"}); err != nil {
		t.Fatal(err)
	}
	c, err := s.DB.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	schema, aside, lockID, err := restoreNames(ctx, c)
	if err != nil {
		t.Fatal(err)
	}
	if err := inTx(ctx, c, "ALTER SCHEMA "+quote(schema)+" RENAME TO "+quote(aside), "CREATE SCHEMA "+quote(schema), "CREATE TABLE "+quote(schema)+".partial (id BIGINT)"); err != nil {
		t.Fatal(err)
	}

	if _, err := c.ExecContext(ctx, "SELECT pg_advisory_lock($1)", lockID); err != nil {
		t.Fatal(err)
	}
	if _, err := openAtImportBaseline(ctx, dsn); err == nil || !strings.Contains(err.Error(), "restore is running") {
		t.Fatal("recovered beside a running restore:", err)
	}
	if _, err := c.ExecContext(ctx, "SELECT pg_advisory_unlock($1)", lockID); err != nil {
		t.Fatal(err)
	}

	pg, err := openAtImportBaseline(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	pg.Close()
	if v, err := s.Q.GetSetting(ctx, "before"); err != nil || v != "keep" {
		t.Fatal("previous data did not come back", v, err)
	}
	if tableExists(t, s, "partial") || schemaExists(t, s, aside) {
		t.Fatal("the unfinished restore was not discarded")
	}
}

// A restore that loaded stays even when its previous schema cannot be dropped: the next
// start must not take the leftover for an interrupted restore and bring the old data back.
func TestFinishedRestoreSurvivesAFailedDrop(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	source := filepath.Join(dir, "backup.db")
	legacy, err := openSQLite(ctx, source)
	if err != nil {
		t.Fatal(err)
	}
	if err := migrateSQLite(ctx, legacy); err != nil {
		t.Fatal(err)
	}
	if _, err := legacy.ExecContext(ctx, "INSERT INTO settings(key,value) VALUES('restored','yes')"); err != nil {
		t.Fatal(err)
	}
	legacy.Close()
	s, err := OpenTest(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	dsn := fixtureDSN(t, s)
	if err := s.Q.SetSetting(ctx, db.SetSettingParams{Key: "previous", Value: "old"}); err != nil {
		t.Fatal(err)
	}
	// A reader of the previous data holds its table: DROP SCHEMA waits for it and gives up.
	reader, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reader.ExecContext(ctx, "LOCK TABLE settings IN ACCESS SHARE MODE"); err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(dsn)
	query := u.Query()
	query.Set("lock_timeout", "300ms")
	u.RawQuery = query.Encode()
	err = RestoreSQLite(ctx, u.String(), dir, source)
	reader.Rollback()
	var leftover *LeftoverSchemaError
	if !errors.As(err, &leftover) {
		t.Fatal("a failed drop is not reported as a leftover:", err)
	}

	pg, err := openAtImportBaseline(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	pg.Close()
	if v, err := s.Q.GetSetting(ctx, "restored"); err != nil || v != "yes" {
		t.Fatal("the next start brought the previous data back", v, err)
	}
	if !schemaExists(t, s, leftover.Schema) {
		t.Fatal("the leftover schema the error names is not there")
	}
	if _, err := s.DB.ExecContext(ctx, "DROP SCHEMA "+quote(leftover.Schema)+" CASCADE"); err != nil {
		t.Fatal(err)
	}
}
