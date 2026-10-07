package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
	_ "modernc.org/sqlite"
)

const MigrationMarker = "postgres-migration.json"

// reportFormat versions the JSON of ImportReport.
const reportFormat = 1

// importLockKey is "MIKN" in ASCII: with the schema's hash, the two-key advisory lock
// that serializes imports into one schema.
const importLockKey = 1296649038

// firstRemoteNode is where the nodes identity starts (START WITH 2): id 1 is the local
// node, which a fresh installation creates itself.
const firstRemoteNode = 2

type TableProof struct {
	Rows   int64  `json:"rows"`
	SHA256 string `json:"sha256"`
}
type ImportReport struct {
	Format       int                   `json:"format"`
	ImportedAt   int64                 `json:"imported_at"`
	SourceSHA256 string                `json:"source_sha256"`
	SourceKind   string                `json:"source_kind,omitempty"`
	Tables       map[string]TableProof `json:"tables"`
}

func openSQLite(ctx context.Context, path string) (*sql.DB, error) {
	return sqliteConnection(ctx, path, false)
}
func sqliteConnection(ctx context.Context, path string, readOnly bool) (*sql.DB, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	u := url.URL{Scheme: "file", Path: filepath.ToSlash(absolute)}
	v := url.Values{}
	v.Add("_pragma", "foreign_keys(ON)")
	v.Add("_pragma", "busy_timeout(5000)")
	if readOnly {
		v.Set("mode", "ro")
	}
	u.RawQuery = v.Encode()
	c, err := sql.Open("sqlite", u.String())
	if err != nil {
		return nil, err
	}
	c.SetMaxOpenConns(1)
	if err := c.PingContext(ctx); err != nil {
		c.Close()
		return nil, err
	}
	return c, nil
}
func migrateSQLite(ctx context.Context, c *sql.DB) error {
	f, err := fs.Sub(migrations, "migrations")
	if err != nil {
		return err
	}
	p, err := goose.NewProvider(goose.DialectSQLite3, c, f)
	if err != nil {
		return err
	}
	var known int64
	for _, source := range p.ListSources() {
		if source.Version > known {
			known = source.Version
		}
	}
	version, err := p.GetDBVersion(ctx)
	if err != nil {
		return err
	}
	if version > known {
		return errors.New("SQLite schema is newer than this importer; use a compatible release")
	}
	_, err = p.Up(ctx)
	return err
}

func writeMigrationMarker(dir string, report []byte) error {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".postgres-migration-*")
	if err != nil {
		return err
	}
	path := f.Name()
	defer os.Remove(path)
	if err := f.Chmod(0600); err != nil {
		f.Close()
		return err
	}
	if _, err := f.Write(report); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(path, filepath.Join(dir, MigrationMarker))
}

// ImportSQLite never changes the original file. All copying, constraints, checksums,
// sequence resets and the import record commit together while the panel is stopped.
func ImportSQLite(ctx context.Context, pg *sql.DB, dir string) (*ImportReport, error) {
	return importSQLite(ctx, pg, dir, filepath.Join(dir, "mikan.db"), false)
}

// ImportSQLiteRestore deliberately replaces application data from an explicitly
// selected backup; a failed restore rolls back to the previous PostgreSQL data.
func ImportSQLiteRestore(ctx context.Context, pg *sql.DB, dir, source string) (*ImportReport, error) {
	return importSQLite(ctx, pg, dir, source, true)
}

type importTable struct {
	name          string
	columns, keys []string
	identities    []string
	textKeys      map[string]bool
}

func importSQLite(ctx context.Context, pg *sql.DB, dir, source string, replace bool) (*ImportReport, error) {
	if report, done, err := committedImport(ctx, pg, dir, source, replace); done || err != nil {
		return report, err
	}
	// SQLite is snapshotted and checked before the PostgreSQL transaction opens: that can
	// take minutes on a big database, and the transaction must not idle meanwhile.
	snapshot, sourceSHA256, err := snapshotSQLite(ctx, dir, source)
	if err != nil {
		return nil, err
	}
	defer snapshot.close()
	report, encoded, err := copySQLite(ctx, pg, snapshot.db, dir, source, replace, sourceSHA256)
	if err != nil || encoded == nil {
		return report, err
	}
	if err := writeMigrationMarker(dir, encoded); err != nil {
		return nil, fmt.Errorf("import committed; retry to recover migration marker: %w", err)
	}
	return report, nil
}

// committedImport decides whether there is anything to import. done with a report: an
// import committed before (its marker is written again); done without one: a fresh
// installation, which needs only PostgreSQL schema migrations.
func committedImport(ctx context.Context, q queryRower, dir, source string, replace bool) (report *ImportReport, done bool, err error) {
	var previous string
	err = q.QueryRowContext(ctx, "SELECT report FROM mikan_sqlite_import WHERE id=1").Scan(&previous)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, true, err
	}
	if previous != "" && !replace {
		report = &ImportReport{}
		if err := json.Unmarshal([]byte(previous), report); err != nil {
			return nil, true, errors.New("invalid committed migration record")
		}
		if err := writeMigrationMarker(dir, []byte(previous)); err != nil {
			return nil, true, err
		}
		return report, true, nil
	}
	if _, err := os.Stat(filepath.Join(dir, MigrationMarker)); err == nil && !replace {
		return nil, true, errors.New("migration marker exists but PostgreSQL import record is missing; restore the PostgreSQL backup before starting the panel")
	} else if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, true, err
	}
	if _, err := os.Stat(source); errors.Is(err, fs.ErrNotExist) && !replace {
		return nil, true, nil
	} else if err != nil {
		return nil, true, err
	}
	return nil, false, nil
}

type sqliteSnapshot struct {
	db  *sql.DB
	tmp string
}

func (s *sqliteSnapshot) close() {
	s.db.Close()
	os.RemoveAll(s.tmp)
}

// snapshotSQLite copies source aside, normalizes the copy to the final SQLite schema and
// checks it; the hash is of the copy as taken, before normalizing.
func snapshotSQLite(ctx context.Context, dir, source string) (*sqliteSnapshot, string, error) {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, "", err
	}
	tmp, err := os.MkdirTemp(dir, ".sqlite-import-*")
	if err != nil {
		return nil, "", err
	}
	snapshot, sum, err := takeSnapshot(ctx, tmp, source)
	if err != nil {
		os.RemoveAll(tmp)
		return nil, "", err
	}
	return &sqliteSnapshot{db: snapshot, tmp: tmp}, sum, nil
}

func takeSnapshot(ctx context.Context, tmp, source string) (*sql.DB, string, error) {
	path := filepath.Join(tmp, "snapshot.db")
	original, err := sqliteConnection(ctx, source, true)
	if err != nil {
		return nil, "", fmt.Errorf("read legacy SQLite: %w", err)
	}
	// VACUUM INTO is a consistent snapshot, including committed WAL pages. Opening
	// the source read-only prevents accidental migrations or checkpoints in it.
	_, err = original.ExecContext(ctx, "VACUUM INTO ?", path)
	original.Close()
	if err != nil {
		return nil, "", fmt.Errorf("snapshot SQLite: %w", err)
	}
	if err := os.Chmod(path, 0600); err != nil {
		return nil, "", err
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, "", err
	}
	sum := sha256.New()
	_, err = io.Copy(sum, f)
	f.Close()
	if err != nil {
		return nil, "", err
	}
	snapshot, err := openSQLite(ctx, path)
	if err != nil {
		return nil, "", err
	}
	if err := checkSnapshot(ctx, snapshot); err != nil {
		snapshot.Close()
		return nil, "", err
	}
	return snapshot, hex.EncodeToString(sum.Sum(nil)), nil
}

func checkSnapshot(ctx context.Context, sqlite *sql.DB) error {
	if err := migrateSQLite(ctx, sqlite); err != nil {
		return fmt.Errorf("normalize SQLite snapshot: %w", err)
	}
	var integrity string
	if err := sqlite.QueryRowContext(ctx, "PRAGMA integrity_check").Scan(&integrity); err != nil || integrity != "ok" {
		return errors.New("SQLite integrity check failed")
	}
	foreign, err := sqlite.QueryContext(ctx, "PRAGMA foreign_key_check")
	if err != nil {
		return err
	}
	invalid := foreign.Next()
	foreignErr := foreign.Err()
	foreign.Close()
	if invalid || foreignErr != nil {
		return errors.New("SQLite foreign key check failed")
	}
	return nil
}

// copySQLite copies every table in one READ COMMITTED transaction: the tables are locked
// before anything is read, so a serializable snapshot would only add aborts. encoded is
// nil when a concurrent import committed first; its report comes back instead.
func copySQLite(ctx context.Context, pg *sql.DB, sqlite *sql.DB, dir, source string, replace bool, sourceSHA256 string) (report *ImportReport, encoded []byte, err error) {
	// COPY runs on the pgx connection under the transaction, so both need one session.
	c, err := pg.Conn(ctx)
	if err != nil {
		return nil, nil, err
	}
	defer c.Close()
	tx, err := c.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return nil, nil, err
	}
	defer tx.Rollback()
	// Also serializes concurrent migration/restore invocations in this schema.
	if _, err := tx.ExecContext(ctx, "SELECT pg_advisory_xact_lock(hashtext(current_schema()), $1)", importLockKey); err != nil {
		return nil, nil, err
	}
	if report, done, err := committedImport(ctx, tx, dir, source, replace); done || err != nil {
		return report, nil, err
	}
	tables, err := importSchema(ctx, sqlite, tx)
	if err != nil {
		return nil, nil, err
	}
	names := make([]string, len(tables))
	for i, t := range tables {
		names[i] = t.name
	}
	if _, err := tx.ExecContext(ctx, "LOCK TABLE "+quoteList(names)+" IN ACCESS EXCLUSIVE MODE"); err != nil {
		return nil, nil, err
	}
	if replace {
		if _, err := tx.ExecContext(ctx, "TRUNCATE "+quoteList(names)+" RESTART IDENTITY CASCADE"); err != nil {
			return nil, nil, err
		}
	} else {
		for _, t := range tables {
			var exists bool
			if err := tx.QueryRowContext(ctx, "SELECT EXISTS (SELECT 1 FROM "+quote(t.name)+")").Scan(&exists); err != nil {
				return nil, nil, err
			}
			if exists {
				return nil, nil, fmt.Errorf("PostgreSQL table %s is occupied; automatic import refused", t.name)
			}
		}
	}
	report = &ImportReport{Format: reportFormat, ImportedAt: time.Now().Unix(), SourceKind: "sqlite", SourceSHA256: sourceSHA256, Tables: make(map[string]TableProof)}
	for _, t := range tables {
		proof, err := copyTable(ctx, sqlite, c, t)
		if err != nil {
			return nil, nil, fmt.Errorf("import table %s: %w", t.name, err)
		}
		report.Tables[t.name] = proof
	}
	for _, t := range tables {
		actual, err := tableProof(ctx, tx, t)
		if err != nil {
			return nil, nil, err
		}
		if actual != report.Tables[t.name] {
			return nil, nil, fmt.Errorf("verification failed for table %s; import rolled back", t.name)
		}
		if err := restartIdentities(ctx, tx, t); err != nil {
			return nil, nil, err
		}
	}
	encoded, err = json.MarshalIndent(report, "", "  ")
	if err != nil {
		return nil, nil, err
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO mikan_sqlite_import(id,report) VALUES(1,$1) ON CONFLICT(id) DO UPDATE SET report=excluded.report", string(encoded)); err != nil {
		return nil, nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, nil, err
	}
	return report, encoded, nil
}

// restartIdentities moves t's identity sequences past the imported IDs.
func restartIdentities(ctx context.Context, tx *sql.Tx, t importTable) error {
	for _, col := range t.identities {
		var seq string
		if err := tx.QueryRowContext(ctx, "SELECT pg_get_serial_sequence($1,$2)", quote(t.name), col).Scan(&seq); err != nil {
			return err
		}
		var next int64
		if err := tx.QueryRowContext(ctx, "SELECT COALESCE(MAX("+quote(col)+"),0)+1 FROM "+quote(t.name)).Scan(&next); err != nil {
			return err
		}
		if t.name == "nodes" {
			next = max(next, firstRemoteNode)
		}
		// ALTER SEQUENCE is transactional, unlike setval: failed restores cannot move
		// a live sequence backwards and cause subsequent ID collisions.
		if _, err := tx.ExecContext(ctx, "ALTER SEQUENCE "+seq+" RESTART WITH "+strconv.FormatInt(next, 10)); err != nil {
			return err
		}
	}
	return nil
}

// ConfirmPostgresRestore marks the explicitly restored database as authoritative.
// A fresh-install dump has no SQLite import row, even on a server that once migrated;
// its stale original SQLite must neither block startup nor overwrite the restored data.
func ConfirmPostgresRestore(ctx context.Context, pg *sql.DB, dir string) error {
	var report string
	err := pg.QueryRowContext(ctx, "SELECT report FROM mikan_sqlite_import WHERE id=1").Scan(&report)
	if errors.Is(err, sql.ErrNoRows) {
		encoded, err := json.Marshal(ImportReport{Format: reportFormat, ImportedAt: time.Now().Unix(), SourceKind: "postgresql-restore"})
		if err != nil {
			return err
		}
		report = string(encoded)
		if _, err := pg.ExecContext(ctx, "INSERT INTO mikan_sqlite_import(id,report) VALUES(1,$1)", report); err != nil {
			return err
		}
	} else if err != nil {
		return err
	}
	return writeMigrationMarker(dir, []byte(report))
}

func importSchema(ctx context.Context, sqlite *sql.DB, pg *sql.Tx) ([]importTable, error) {
	rows, err := pg.QueryContext(ctx, "SELECT table_name,column_name,is_identity FROM information_schema.columns WHERE table_schema=current_schema() AND table_name NOT IN ('goose_db_version','mikan_sqlite_import') ORDER BY table_name,ordinal_position")
	if err != nil {
		return nil, err
	}
	lookup := map[string]*importTable{}
	for rows.Next() {
		var name, col, identity string
		if err := rows.Scan(&name, &col, &identity); err != nil {
			rows.Close()
			return nil, err
		}
		if lookup[name] == nil {
			lookup[name] = &importTable{name: name}
		}
		lookup[name].columns = append(lookup[name].columns, col)
		if identity == "YES" {
			lookup[name].identities = append(lookup[name].identities, col)
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	names, err := sqlite.QueryContext(ctx, "SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%' AND name!='goose_db_version'")
	if err != nil {
		return nil, err
	}
	found := map[string]bool{}
	for names.Next() {
		var name string
		if err := names.Scan(&name); err != nil {
			names.Close()
			return nil, err
		}
		if lookup[name] == nil {
			names.Close()
			return nil, fmt.Errorf("unknown SQLite table %s; use a compatible migration version", name)
		}
		found[name] = true
	}
	err = names.Err()
	names.Close()
	if err != nil {
		return nil, err
	}
	for _, name := range []string{"promo_codes", "promo_redemptions"} {
		if !found[name] {
			delete(lookup, name)
		}
	}
	dependencies := map[string][]string{}
	for name, t := range lookup {
		if !found[name] {
			return nil, fmt.Errorf("SQLite table %s is missing", name)
		}
		cols, err := sqlite.QueryContext(ctx, "PRAGMA table_info("+quote(name)+")")
		if err != nil {
			return nil, err
		}
		var columns []string
		keys := map[int]string{}
		t.textKeys = map[string]bool{}
		for cols.Next() {
			var cid, notnull, pk int
			var col, typ string
			var def any
			if err := cols.Scan(&cid, &col, &typ, &notnull, &def, &pk); err != nil {
				cols.Close()
				return nil, err
			}
			columns = append(columns, col)
			if pk > 0 {
				keys[pk] = col
				if strings.EqualFold(typ, "TEXT") {
					t.textKeys[col] = true
				}
			}
		}
		err = cols.Err()
		cols.Close()
		if err != nil {
			return nil, err
		}
		if strings.Join(columns, "\x00") != strings.Join(t.columns, "\x00") {
			return nil, fmt.Errorf("schema mismatch for %s", name)
		}
		for i := 1; i <= len(keys); i++ {
			t.keys = append(t.keys, keys[i])
		}
		if len(t.keys) == 0 {
			return nil, fmt.Errorf("missing primary key for %s", name)
		}
		fks, err := sqlite.QueryContext(ctx, "PRAGMA foreign_key_list("+quote(name)+")")
		if err != nil {
			return nil, err
		}
		for fks.Next() {
			var id, seq int
			var parent, from, to, onupdate, ondelete, match string
			if err := fks.Scan(&id, &seq, &parent, &from, &to, &onupdate, &ondelete, &match); err != nil {
				fks.Close()
				return nil, err
			}
			if parent != name {
				dependencies[name] = append(dependencies[name], parent)
			}
		}
		err = fks.Err()
		fks.Close()
		if err != nil {
			return nil, err
		}
	}
	var sorted []importTable
	done := map[string]bool{}
	for len(sorted) < len(lookup) {
		var ready []string
		for name := range lookup {
			if done[name] {
				continue
			}
			ok := true
			for _, p := range dependencies[name] {
				if !done[p] {
					ok = false
				}
			}
			if ok {
				ready = append(ready, name)
			}
		}
		if len(ready) == 0 {
			return nil, errors.New("cyclic or missing SQLite foreign keys")
		}
		sort.Strings(ready)
		for _, name := range ready {
			sorted = append(sorted, *lookup[name])
			done[name] = true
		}
	}
	return sorted, nil
}

type queryRows interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

// quoteList is names quoted and joined with commas.
func quoteList(names []string) string {
	out := make([]string, len(names))
	for i, n := range names {
		out[i] = quote(n)
	}
	return strings.Join(out, ",")
}

func selectTable(t importTable, postgres bool) string {
	keys := make([]string, len(t.keys))
	for i, k := range t.keys {
		keys[i] = quote(k)
		if t.textKeys[k] {
			if postgres {
				keys[i] += " COLLATE \"C\""
			} else {
				keys[i] += " COLLATE BINARY"
			}
		}
	}
	return "SELECT " + quoteList(t.columns) + " FROM " + quote(t.name) + " ORDER BY " + strings.Join(keys, ",")
}
func hashValues(h hash.Hash, values []any) error {
	for _, v := range values {
		switch x := v.(type) {
		case nil:
			fmt.Fprint(h, "N;")
		case int64:
			fmt.Fprintf(h, "I%d;", x)
		case string:
			fmt.Fprintf(h, "S%d:", len(x))
			h.Write([]byte(x))
		case []byte:
			fmt.Fprintf(h, "S%d:", len(x))
			h.Write(x)
		default:
			return fmt.Errorf("unsupported SQLite value type %T", v)
		}
	}
	fmt.Fprint(h, "R;")
	return nil
}
func scanRow(rows *sql.Rows, n int) ([]any, error) {
	values := make([]any, n)
	dest := make([]any, n)
	for i := range values {
		dest[i] = &values[i]
	}
	if err := rows.Scan(dest...); err != nil {
		return nil, err
	}
	for i, v := range values {
		if b, ok := v.([]byte); ok {
			values[i] = string(b)
		}
	}
	return values, nil
}
func tableProof(ctx context.Context, c queryRows, t importTable) (TableProof, error) {
	rows, err := c.QueryContext(ctx, selectTable(t, true))
	if err != nil {
		return TableProof{}, err
	}
	defer rows.Close()
	h := sha256.New()
	p := TableProof{}
	for rows.Next() {
		values, err := scanRow(rows, len(t.columns))
		if err != nil {
			return p, err
		}
		if err := hashValues(h, values); err != nil {
			return p, err
		}
		p.Rows++
	}
	p.SHA256 = hex.EncodeToString(h.Sum(nil))
	return p, rows.Err()
}

// sqliteRows feeds COPY from SQLite and hashes each row on its way, for the table proof.
type sqliteRows struct {
	rows   *sql.Rows
	n      int
	h      hash.Hash
	count  int64
	values []any
	err    error
}

func (r *sqliteRows) Next() bool {
	if r.err != nil || !r.rows.Next() {
		return false
	}
	if r.values, r.err = scanRow(r.rows, r.n); r.err == nil {
		r.err = hashValues(r.h, r.values)
	}
	if r.err != nil {
		return false
	}
	r.count++
	return true
}

func (r *sqliteRows) Values() ([]any, error) { return r.values, nil }

func (r *sqliteRows) Err() error {
	if r.err != nil {
		return r.err
	}
	return r.rows.Err()
}

// copyTable streams t from SQLite into PostgreSQL with COPY on pg's session, inside the
// transaction open there.
func copyTable(ctx context.Context, sqlite *sql.DB, pg *sql.Conn, t importTable) (TableProof, error) {
	rows, err := sqlite.QueryContext(ctx, selectTable(t, false))
	if err != nil {
		return TableProof{}, err
	}
	defer rows.Close()
	src := &sqliteRows{rows: rows, n: len(t.columns), h: sha256.New()}
	var copied int64
	err = pg.Raw(func(driverConn any) error {
		copied, err = driverConn.(*stdlib.Conn).Conn().CopyFrom(ctx, pgx.Identifier{t.name}, t.columns, src)
		return err
	})
	if err != nil {
		return TableProof{}, err
	}
	if copied != src.count {
		return TableProof{}, fmt.Errorf("COPY took %d rows of %d", copied, src.count)
	}
	return TableProof{Rows: src.count, SHA256: hex.EncodeToString(src.h.Sum(nil))}, nil
}
