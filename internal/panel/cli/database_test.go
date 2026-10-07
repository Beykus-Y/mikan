package cli

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"mikan/internal/panel/store"
	"mikan/internal/panel/store/db"
	"mikan/internal/panel/store/storetest"
)

func TestPostgresArchiveActuallyRestores(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	st, err := storetest.Open(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	dsn := schemaDSN(t, st)
	t.Setenv("MIKAN_DATABASE_URL", dsn)
	t.Setenv("MIKAN_DATA_DIR", dir)
	if err := st.Q.SetSetting(ctx, db.SetSettingParams{Key: "preserved", Value: "42"}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "backup.dump")
	if err := databaseCmd(ctx, []string{"backup", path}); err != nil {
		t.Fatal(err)
	}
	magic, err := os.ReadFile(path)
	if err != nil || string(magic[:5]) != "PGDMP" {
		t.Fatal("not a PostgreSQL custom archive", err)
	}
	if err := st.Q.SetSetting(ctx, db.SetSettingParams{Key: "preserved", Value: "99"}); err != nil {
		t.Fatal(err)
	}
	// A migration newer than the archive: pg_restore --clean alone would leave it behind.
	if _, err := st.DB.ExecContext(ctx, "CREATE TABLE newer_migration (id BIGINT)"); err != nil {
		t.Fatal(err)
	}
	// A server that migrated once still has the original SQLite and marker. A fresh
	// PG dump contains no import record: explicit restore must establish provenance.
	if err := os.WriteFile(filepath.Join(dir, "mikan.db"), []byte("original SQLite preserved"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, store.MigrationMarker), []byte("stale"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := databaseCmd(ctx, []string{"restore", path}); err != nil {
		t.Fatal(err)
	}
	if v, err := st.Q.GetSetting(ctx, "preserved"); err != nil || v != "42" {
		t.Fatalf("restore did not replace data: %s %v", v, err)
	}
	var newer bool
	if err := st.DB.QueryRowContext(ctx, "SELECT to_regclass('newer_migration') IS NOT NULL").Scan(&newer); err != nil || newer {
		t.Fatal("an object the archive never had survived the restore", err)
	}
	live, err := store.OpenPostgres(ctx, dir, dsn)
	if err != nil {
		t.Fatal("restored PG cannot start:", err)
	}
	live.Close()
	// A truncated archive must fail without applying its partial contents.
	bad := filepath.Join(t.TempDir(), "truncated.dump")
	if err := os.WriteFile(bad, magic[:len(magic)/2], 0600); err != nil {
		t.Fatal(err)
	}
	if err := databaseCmd(ctx, []string{"restore", bad}); err == nil {
		t.Fatal("truncated archive accepted")
	} else if !strings.Contains(err.Error(), "pg_restore: ") {
		t.Fatal("the failure does not say why:", err)
	}
	if v, err := st.Q.GetSetting(ctx, "preserved"); err != nil || v != "42" {
		t.Fatal("failed restore changed data", v, err)
	}
}

// An installation in the public schema (a MIKAN_DATABASE_URL without a search path, as the
// installer writes it) backs up and restores like a fixture's own schema does.
func TestPublicSchemaBackupAndRestore(t *testing.T) {
	ctx := context.Background()
	base := os.Getenv("MIKAN_TEST_DATABASE_URL")
	admin, err := pgx.Connect(ctx, base)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close(context.Background())
	name := fmt.Sprintf("mikan_public_%d", time.Now().UnixNano())
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+name); err != nil {
		t.Skip("CREATE DATABASE is not permitted here:", err)
	}
	defer admin.Exec(context.Background(), "DROP DATABASE "+name+" WITH (FORCE)")
	u, err := url.Parse(base)
	if err != nil {
		t.Fatal(err)
	}
	u.Path = "/" + name
	dsn := u.String()
	dir := t.TempDir()
	t.Setenv("MIKAN_DATABASE_URL", dsn)
	t.Setenv("MIKAN_DATA_DIR", dir)
	st, err := store.OpenPostgres(ctx, dir, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	var schema string
	if err := st.DB.QueryRowContext(ctx, "SELECT current_schema()").Scan(&schema); err != nil || schema != "public" {
		t.Fatalf("schema %q %v", schema, err)
	}
	if err := st.Q.SetSetting(ctx, db.SetSettingParams{Key: "preserved", Value: "42"}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "backup.dump")
	if err := databaseCmd(ctx, []string{"backup", path}); err != nil {
		t.Fatal(err)
	}
	if err := st.Q.SetSetting(ctx, db.SetSettingParams{Key: "preserved", Value: "99"}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB.ExecContext(ctx, "CREATE TABLE newer_migration (id BIGINT)"); err != nil {
		t.Fatal(err)
	}
	if err := databaseCmd(ctx, []string{"restore", path}); err != nil {
		t.Fatal(err)
	}
	if v, err := st.Q.GetSetting(ctx, "preserved"); err != nil || v != "42" {
		t.Fatalf("restore did not replace data: %s %v", v, err)
	}
	var newer bool
	var leftovers int
	if err := st.DB.QueryRowContext(ctx, "SELECT to_regclass('newer_migration') IS NOT NULL, (SELECT count(*) FROM pg_namespace WHERE nspname LIKE 'public\\_%')").Scan(&newer, &leftovers); err != nil || newer || leftovers != 0 {
		t.Fatal("the restore left the previous objects behind:", newer, leftovers, err)
	}
	live, err := store.OpenPostgres(ctx, dir, dsn)
	if err != nil {
		t.Fatal("restored PG cannot start:", err)
	}
	live.Close()
}

// schemaDSN reaches st's schema the way an installation's MIKAN_DATABASE_URL reaches its
// own: pg_dump and pg_restore get the search path too.
func schemaDSN(t *testing.T, st *store.Store) string {
	t.Helper()
	var schema string
	if err := st.DB.QueryRowContext(context.Background(), "SELECT current_schema()").Scan(&schema); err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(os.Getenv("MIKAN_TEST_DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	query := u.Query()
	query.Set("options", "-csearch_path="+schema)
	u.RawQuery = query.Encode()
	return u.String()
}

func TestToolOutputMasksThePassword(t *testing.T) {
	got := toolOutput("postgresql://mikan:test-secret@localhost/mikan", "pg_restore: error: test-secret rejected\n")
	if strings.Contains(got, "test-secret") || !strings.Contains(got, "pg_restore: error: *** rejected") {
		t.Fatal(got)
	}
	if toolOutput("postgresql://mikan:x@localhost/mikan", " \n") != "" {
		t.Fatal("empty diagnostics produce text")
	}
	// The tail starts on a whole character, not in the middle of one.
	long := strings.Repeat("я", toolOutputLimit/2+1) + "!" // the limit falls inside a "я"
	if got := toolOutput("postgresql://mikan:x@localhost/mikan", long); !utf8.ValidString(got) || !strings.HasSuffix(got, "я!") {
		t.Fatalf("cut diagnostics: %q", got[:16])
	}
}

// A panel that answers on its port is unhealthy when its database does not.
func TestHealthChecksTheDatabase(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	defer srv.Close()
	t.Setenv("MIKAN_DEV", "1")
	t.Setenv("MIKAN_PANEL_LISTEN", strings.TrimPrefix(srv.URL, "http://"))
	t.Setenv("MIKAN_DATABASE_URL", os.Getenv("MIKAN_TEST_DATABASE_URL"))
	if err := health(); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MIKAN_DATABASE_URL", "postgresql://mikan:x@127.0.0.1:1/mikan?sslmode=disable")
	if err := health(); err == nil || !strings.Contains(err.Error(), "database") {
		t.Fatal("healthy without a database:", err)
	}
}

func TestRestoreRefusesATooShortFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "short")
	if err := os.WriteFile(path, []byte("PGDMP"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MIKAN_DATABASE_URL", "postgresql://mikan:x@localhost/mikan")
	t.Setenv("MIKAN_DATA_DIR", t.TempDir())
	if err := databaseCmd(context.Background(), []string{"restore", path}); err == nil || !strings.Contains(err.Error(), "too short") {
		t.Fatal(err)
	}
}

func TestPostgresToolCredentialsStayOutOfArguments(t *testing.T) {
	env, err := postgresEnv("postgresql://mikan:test-secret@localhost/mikan?host=/run/postgresql&sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(env, "\n")
	for _, want := range []string{"PGHOST=/run/postgresql", "PGUSER=mikan", "PGPASSWORD=test-secret", "PGDATABASE=mikan", "PGSSLMODE=disable"} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing libpq parameter %s", want)
		}
	}
}

// A PostgreSQL client gets what it needs from the environment and none of the panel's
// secrets.
func TestPostgresToolsGetNoPanelSecrets(t *testing.T) {
	t.Setenv("MIKAN_DATABASE_URL", "postgresql://mikan:db-secret@localhost/mikan")
	t.Setenv("MIKAN_JOIN_KEY", "join-secret")
	t.Setenv("PGPASSFILE", "/somewhere/else")
	t.Setenv("PATH", "/usr/bin")
	env, err := postgresEnv("postgresql://mikan:db-secret@localhost/mikan")
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(env, "\n")
	for _, leaked := range []string{"MIKAN_", "join-secret", "PGPASSFILE"} {
		if strings.Contains(joined, leaked) {
			t.Errorf("%s reaches the client", leaked)
		}
	}
	if !strings.Contains(joined, "PATH=/usr/bin") {
		t.Error("the client has no PATH")
	}
}

func TestCheckDumpList(t *testing.T) {
	good := ";\n; Archive created at 2026-10-03\n;\n" +
		"3557; 2615 2200 SCHEMA - public pg_database_owner\n" +
		"3558; 0 0 COMMENT - SCHEMA public pg_database_owner\n" +
		"220; 1259 16390 TABLE public users mikan\n" +
		"221; 1259 16389 SEQUENCE public users_id_seq mikan\n" +
		"3400; 0 16390 TABLE DATA public users mikan\n" +
		"3560; 0 0 SEQUENCE SET public users_id_seq mikan\n" +
		"3201; 2606 16420 CONSTRAINT public users users_pkey mikan\n" +
		"3301; 1259 16430 INDEX public users_status mikan\n" +
		"3350; 2606 16440 FK CONSTRAINT public slots slots_user_fkey mikan\n"
	if err := checkDumpList([]byte(good)); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{
		"230; 1255 16500 FUNCTION public evil() mikan\n",
		"231; 2620 16501 TRIGGER public users run_evil mikan\n",
		"232; 3079 16502 EXTENSION - plpython3u\n",
		"233; 0 0 ACL - SCHEMA public postgres\n",
		"not a toc line\n",
	} {
		if err := checkDumpList([]byte(good + bad)); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
}

// A dump with a function in it, made by the real pg_dump, is refused before pg_restore
// connects; the data stay as they were.
func TestRestoreRefusesADumpWithCode(t *testing.T) {
	if _, err := exec.LookPath("pg_dump"); err != nil {
		t.Skip("pg_dump is not installed")
	}
	ctx := context.Background()
	dsn, dir := newDatabase(t, "code"), t.TempDir()
	st, err := store.OpenPostgres(ctx, dir, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if _, err := st.DB.ExecContext(ctx, "CREATE FUNCTION evil() RETURNS int LANGUAGE sql AS 'SELECT 1'"); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MIKAN_DATABASE_URL", dsn)
	t.Setenv("MIKAN_DATA_DIR", dir)
	path := filepath.Join(t.TempDir(), "with-code.dump")
	if err := databaseCmd(ctx, []string{"backup", path}); err != nil {
		t.Fatal(err)
	}
	if err := st.Q.SetSetting(ctx, db.SetSettingParams{Key: "kept", Value: "1"}); err != nil {
		t.Fatal(err)
	}
	if err := databaseCmd(ctx, []string{"restore", path}); err == nil || !strings.Contains(err.Error(), "FUNCTION") {
		t.Fatalf("a dump with a function was restored: %v", err)
	}
	if v, err := st.Q.GetSetting(ctx, "kept"); err != nil || v != "1" {
		t.Fatal("the refused restore changed data:", v, err)
	}
}
