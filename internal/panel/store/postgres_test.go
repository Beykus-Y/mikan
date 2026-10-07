package store

import (
	"context"
	"database/sql"
	"errors"
	"github.com/pressly/goose/v3"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"mikan/internal/panel/store/db"
)

// A slot belongs to one buyer even when two purchases reach PostgreSQL together.
func TestPostgresConcurrentSlots(t *testing.T) {
	ctx := context.Background()
	s, err := OpenTest(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for _, n := range []string{"a", "b"} {
		if err := s.Q.InsertSlot(ctx, db.InsertSlotParams{Name: n, Uuid: n, Secret: n, CreatedAt: 1}); err != nil {
			t.Fatal(err)
		}
	}
	var wg sync.WaitGroup
	ids := make(chan int64, 2)
	errs := make(chan error, 2)
	for range 2 {
		wg.Go(func() {
			var id int64
			err := s.Tx(ctx, func(q *db.Queries) error {
				slot, err := q.TakeFreeSlot(ctx)
				if err == nil {
					id = slot.ID
				}
				return err
			})
			if err == nil {
				ids <- id
			}
			errs <- err
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	a, b := <-ids, <-ids
	if a == b {
		t.Fatalf("two transactions received slot %d", a)
	}
}

func TestConcurrentFreshPostgresMigrations(t *testing.T) {
	ctx := context.Background()
	s, err := OpenTest(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var schema string
	if err := s.DB.QueryRowContext(ctx, "SELECT current_schema()").Scan(&schema); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.ExecContext(ctx, "DROP SCHEMA \""+schema+"\" CASCADE; CREATE SCHEMA \""+schema+"\""); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 4)
	for range 4 {
		wg.Go(func() { errs <- migrateUp(ctx, s.DB, postgresFS) })
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal("concurrent initialization:", err)
		}
	}
	var n int
	if err := s.DB.QueryRowContext(ctx, "SELECT count(*) FROM goose_db_version WHERE version_id=1 AND is_applied").Scan(&n); err != nil || n != 1 {
		t.Fatalf("baseline applied %d times: %v", n, err)
	}
}

// SQLite is imported as data: stable IDs, credentials, NULL quotas and 64-bit counters
// survive; a repeat never replaces changes subsequently made in PostgreSQL.
func TestSQLiteImportKeepsDataAndIsRepeatable(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	legacy, err := openSQLite(ctx, filepath.Join(dir, "mikan.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := migrateSQLite(ctx, legacy); err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{
		"INSERT INTO admins(id,username,password_hash,totp_secret,recovery_codes,created_at) VALUES(1,'admin','existing-hash','existing-totp','[]',1)",
		"INSERT INTO audit_log(id,ts,admin_id,action,details) VALUES(1,1,1,'action','{}')",
		"INSERT INTO node_state(key,value) VALUES('counters_epoch/1','old-epoch'),('counters_seq/1','77')",
		"INSERT INTO nodes(id,name,created_at,updated_at) VALUES(2,'remote',1,1)",
		"INSERT INTO slot_counter(id,last) VALUES(1,99) ON CONFLICT(id) DO UPDATE SET last=99",
		"INSERT INTO tariffs(id,name,duration_days,created_at) VALUES(5,'unlimited',30,1)",
		"INSERT INTO tg_chats(tg_id,created_at,updated_at) VALUES(9000000001,1,1)",
		"INSERT INTO traffic_pools(id,name,created_at) VALUES(2,'extra',1)",
		"INSERT INTO api_keys(id,admin_id,name,prefix,hash,scope,created_at) VALUES(1,1,'key','prefix','existing-api-hash','full',1)",
		"INSERT INTO inbounds(id,node_id,name,preset,port,settings,config,created_at,updated_at) VALUES(3,1,'in','vless-reality','443','{}','existing-config',1,1)",
		"INSERT INTO node_relays(node_id,port,config,created_at) VALUES(1,'4443','existing-relay',1)",
		"INSERT INTO node_warp(node_id,source,private_key,peer_public_key,endpoint,ipv4,created_at,updated_at) VALUES(1,'import','existing-private','existing-peer','example.org:2408','172.16.0.2',1,1)",
		"INSERT INTO relay_users(exit_node_id,src_node_id,uuid) VALUES(2,1,'existing-relay-uuid')",
		"INSERT INTO sessions(id_hash,admin_id,csrf_token,created_at,last_seen_at,expires_at,ip,user_agent) VALUES('existing-session',1,'existing-csrf',1,2,9999999999,'127.0.0.1','agent')",
		"INSERT INTO tariff_pools(tariff_id,pool_id,traffic_limit) VALUES(5,2,1099511627776)",
		"INSERT INTO traffic_packages(id,name,bytes,pool_id,lifetime,created_at) VALUES(6,'package',100,2,'used',1)",
		"INSERT INTO slots (id,name,uuid,secret,state,created_at) VALUES (71,'s000071','existing-uuid','existing-secret','assigned',1)",
		"INSERT INTO slots (id,name,uuid,secret,state,created_at) VALUES (72,'s000072','device-uuid','device-secret','assigned',1)",
		"INSERT INTO users (id,name,sub_token,slot_id,period_start,created_at,updated_at,used_up,total_up) VALUES (41,'Имя','existing-token',71,1,1,1,1099511627776,2199023255552)",
		"INSERT INTO settings (key,value) VALUES ('tg_offset','{\"bot\":123,\"offset\":77}')",
		"INSERT INTO settings(key,value) VALUES('a_b','1'),('ab','2'),('a-b','3'),('Я','4')",
		"INSERT INTO tg_links (user_id,tg_id,created_at) VALUES (41,9000000001,1)",
		"INSERT INTO bound_devices(id,user_id,hwid,slot_id,created_at,last_seen) VALUES(4,41,'hwid',72,1,2)",
		"INSERT INTO devices(user_id,ip,first_seen,last_seen) VALUES(41,'203.0.113.1',1,2)",
		"INSERT INTO inbound_events(id,inbound_id,node_id,kind,old_value,new_value,reason,created_at) VALUES(7,3,1,'port','443','4443','blocked',1)",
		"INSERT INTO infrastructure_alert_state(key,value,updated_at) VALUES('monitor','{}',1)",
		"INSERT INTO inbound_reach(slot,inbound_id,at) VALUES('s000071',3,1)",
		"INSERT INTO payments(id,provider,payload,tg_id,kind,user_id,tariff_id,tariff_name,amount,currency,status,created_at,paid_at) VALUES(8,'stars','existing-payload',9000000001,'renew',41,5,'unlimited',100,'XTR','paid',1,2)",
		"INSERT INTO sub_fetches(user_id,ip,fetched_at) VALUES(41,'203.0.113.1',1)",
		"INSERT INTO tg_notices(user_id,kind,period,sent_at) VALUES(41,'expire_3d',123,1)",
		"INSERT INTO traffic_daily(user_id,day,up,down) VALUES(41,1,1099511627776,20)",
		"INSERT INTO traffic_hourly(user_id,hour,up,down) VALUES(41,1,1099511627776,20)",
		"INSERT INTO user_pools(user_id,pool_id,traffic_limit,used_up,used_down) VALUES(41,2,NULL,100,200)",
		"INSERT INTO traffic_grants(id,user_id,pool_id,bytes,remaining,lifetime,source,payment_id,package_id,created_at) VALUES(9,41,2,100,50,'used','purchase',8,6,1)",
	} {
		if _, err := legacy.ExecContext(ctx, query); err != nil {
			t.Fatal(err)
		}
	}
	legacy.Close()
	original, err := os.ReadFile(filepath.Join(dir, "mikan.db"))
	if err != nil {
		t.Fatal(err)
	}
	s, err := openImportTest(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := s.DB.ExecContext(ctx, "DELETE FROM nodes"); err != nil {
		t.Fatal(err)
	}
	report, err := ImportSQLite(ctx, s.DB, dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Tables) != 32 || report.Tables["users"].Rows != 1 {
		t.Fatalf("incomplete import: %+v", report)
	}
	for name, proof := range report.Tables {
		if proof.Rows == 0 {
			t.Errorf("fixture missed table %s", name)
		}
	}
	u, err := s.Q.GetUser(ctx, 41)
	if err != nil || u.SubToken != "existing-token" || u.TrafficLimit.Valid || u.UsedUp != 1<<40 || u.TotalUp != 1<<41 {
		t.Fatalf("user changed: %+v %v", u, err)
	}
	link, err := s.Q.GetTgLink(ctx, 41)
	if err != nil || link.TgID != 9000000001 {
		t.Fatalf("Telegram link changed: %+v %v", link, err)
	}
	if _, err := s.DB.ExecContext(ctx, "UPDATE users SET used_up = used_up + 1 WHERE id = 41"); err != nil {
		t.Fatal(err)
	}
	if _, err := ImportSQLite(ctx, s.DB, dir); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(dir, MigrationMarker)); err != nil {
		t.Fatal(err)
	}
	if _, err := ImportSQLite(ctx, s.DB, dir); err != nil {
		t.Fatal("recover committed import marker:", err)
	}
	u, _ = s.Q.GetUser(ctx, 41)
	if u.UsedUp != (1<<40)+1 {
		t.Fatal("repeated import overwrote PostgreSQL")
	}
	after, _ := os.ReadFile(filepath.Join(dir, "mikan.db"))
	if string(original) != string(after) {
		t.Fatal("source SQLite was modified")
	}
	if _, err := os.Stat(filepath.Join(dir, MigrationMarker)); err != nil {
		t.Fatal(err)
	}
	// The next generated ID must follow imported IDs, not collide with them.
	created, err := s.Q.CreateUser(ctx, db.CreateUserParams{Name: "next", SubToken: "next", ResetStrategy: "none", PeriodStart: 1, CreatedAt: 1, UpdatedAt: 1, SlotID: sql.NullInt64{}})
	if err != nil || created.ID <= 41 {
		t.Fatalf("sequence not advanced: %d %v", created.ID, err)
	}
}

func fixtureDSN(t *testing.T, s *Store) string {
	t.Helper()
	var schema string
	if err := s.DB.QueryRowContext(context.Background(), "SELECT current_schema()").Scan(&schema); err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(os.Getenv("MIKAN_TEST_DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	query := u.Query()
	query.Set("search_path", schema)
	u.RawQuery = query.Encode()
	return u.String()
}

func TestRuntimeRefusesIncompleteOrLostMigration(t *testing.T) {
	ctx := context.Background()
	s, err := OpenTest(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	dir := t.TempDir()
	dsn := fixtureDSN(t, s)
	if err := os.WriteFile(filepath.Join(dir, "mikan.db"), []byte("legacy"), 0600); err != nil {
		t.Fatal(err)
	}
	if live, err := OpenPostgres(ctx, dir, dsn); err == nil {
		live.Close()
		t.Fatal("started without importing SQLite")
	}
	if err := os.Remove(filepath.Join(dir, "mikan.db")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, MigrationMarker), []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	if live, err := OpenPostgres(ctx, dir, dsn); err == nil {
		live.Close()
		t.Fatal("started after PostgreSQL data loss")
	}
	if _, err := ImportSQLite(ctx, s.DB, dir); err == nil {
		t.Fatal("missing import accepted with existing marker")
	}
	if err := ConfirmPostgresRestore(ctx, s.DB, dir); err != nil {
		t.Fatal(err)
	}
	live, err := OpenPostgres(ctx, dir, dsn)
	if err != nil {
		t.Fatal("explicit PG restore cannot start:", err)
	}
	live.Close()
}

func TestSQLiteOlderSchemaAndWALImport(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	legacy, err := openSQLite(ctx, filepath.Join(dir, "mikan.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer legacy.Close()
	f, _ := fs.Sub(migrations, "migrations")
	p, err := goose.NewProvider(goose.DialectSQLite3, legacy, f)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.UpTo(ctx, 16); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{"PRAGMA journal_mode=WAL", "PRAGMA wal_autocheckpoint=0", "INSERT INTO settings(key,value) VALUES('wal-only','committed')", "INSERT INTO slots(id,name,uuid,secret,state,created_at) VALUES(91,'s000091','uuid91','key91','free',1)"} {
		if _, err := legacy.ExecContext(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	s, err := openImportTest(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := s.DB.ExecContext(ctx, "DELETE FROM nodes"); err != nil {
		t.Fatal(err)
	}
	if _, err := ImportSQLite(ctx, s.DB, dir); err != nil {
		t.Fatal(err)
	}
	if v, err := s.Q.GetSetting(ctx, "wal-only"); err != nil || v != "committed" {
		t.Fatal("lost committed WAL", v, err)
	}
	if last, err := s.Q.SlotCounter(ctx); err != nil || last != 91 {
		t.Fatal("old schema not normalized", last, err)
	}
	var version int64
	if err := legacy.QueryRowContext(ctx, "SELECT MAX(version_id) FROM goose_db_version").Scan(&version); err != nil || version != 16 {
		t.Fatal("source schema was migrated", version, err)
	}
}

func TestFailedSQLiteRestoreRollsBackAndCanRetry(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	legacy, err := openSQLite(ctx, filepath.Join(dir, "mikan.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := migrateSQLite(ctx, legacy); err != nil {
		t.Fatal(err)
	}
	if _, err := legacy.ExecContext(ctx, "INSERT INTO users(id,name,sub_token,period_start,created_at,updated_at,used_up) VALUES(41,'old','token',1,1,1,0.5)"); err != nil {
		t.Fatal(err)
	}
	legacy.Close()
	s, err := openImportTest(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.Q.SetSetting(ctx, db.SetSettingParams{Key: "new-payment", Value: "keep"}); err != nil {
		t.Fatal(err)
	}
	if _, err := ImportSQLiteRestore(ctx, s.DB, dir, filepath.Join(dir, "mikan.db")); err == nil {
		t.Fatal("invalid counter imported")
	}
	if value, _ := s.Q.GetSetting(ctx, "new-payment"); value != "keep" {
		t.Fatal("failed restore lost PostgreSQL state")
	}
	if _, err := os.Stat(filepath.Join(dir, MigrationMarker)); !os.IsNotExist(err) {
		t.Fatal("failed restore certified")
	}
	legacy, err = openSQLite(ctx, filepath.Join(dir, "mikan.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := legacy.ExecContext(ctx, "UPDATE users SET used_up=500"); err != nil {
		t.Fatal(err)
	}
	legacy.Close()
	if _, err := ImportSQLiteRestore(ctx, s.DB, dir, filepath.Join(dir, "mikan.db")); err != nil {
		t.Fatal(err)
	}
	if u, err := s.Q.GetUser(ctx, 41); err != nil || u.UsedUp != 500 {
		t.Fatal("retry lost legacy user", u, err)
	}
	if _, err := s.Q.GetSetting(ctx, "new-payment"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("explicit restore failed to replace data")
	}
}

func TestSQLiteImportRefusesOccupiedDestination(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	legacy, err := openSQLite(ctx, filepath.Join(dir, "mikan.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := migrateSQLite(ctx, legacy); err != nil {
		t.Fatal(err)
	}
	legacy.Close()
	s, err := OpenTest(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.Q.SetSetting(ctx, db.SetSettingParams{Key: "existing", Value: "keep"}); err != nil {
		t.Fatal(err)
	}
	if _, err := ImportSQLite(ctx, s.DB, dir); err == nil {
		t.Fatal("import into occupied database")
	}
	if v, _ := s.Q.GetSetting(ctx, "existing"); v != "keep" {
		t.Fatal("destination changed")
	}
	if _, err := os.Stat(filepath.Join(dir, MigrationMarker)); !os.IsNotExist(err) {
		t.Fatal("failed import marked complete")
	}
}
