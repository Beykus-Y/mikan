package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"

	"mikan/internal/panel/store/db"
)

// The tables keyed by (user_id, time) are read and pruned by time alone: the primary key
// cannot serve that, nor a cascade by the second column of a key. With sequential scans
// disabled, PostgreSQL must have a usable index even for a fresh empty fixture (where a
// sequential scan is usually cheaper).
func TestTimeQueriesUseTheirIndex(t *testing.T) {
	ctx := context.Background()
	st, err := OpenTest(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	conn, err := st.DB.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, "SET enable_seqscan=off"); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ sql, table, index string }{
		{"DELETE FROM traffic_hourly WHERE hour < 1", "traffic_hourly", "traffic_hourly_hour"},
		{"DELETE FROM traffic_daily WHERE day < 1", "traffic_daily", "traffic_daily_day"},
		{"SELECT hour, sum(up), sum(down) FROM traffic_hourly WHERE hour >= 1 GROUP BY hour ORDER BY hour", "traffic_hourly", "traffic_hourly_hour"},
		{"SELECT day, sum(up), sum(down) FROM traffic_daily WHERE day >= 1 GROUP BY day ORDER BY day", "traffic_daily", "traffic_daily_day"},
		{"SELECT u.id, sum(d.up + d.down) AS b FROM traffic_daily d JOIN users u ON u.id = d.user_id WHERE d.day >= 1 GROUP BY u.id ORDER BY b DESC LIMIT 5", "traffic_daily", "traffic_daily_day"},
		{"DELETE FROM devices WHERE last_seen < 1", "devices", "devices_last_seen"},
		{"DELETE FROM audit_log WHERE ts < 1", "audit_log", "audit_log_ts"},
		// Migration 0002.
		{"SELECT count(*) FROM payments WHERE tg_id = 1 AND status IN ('pending', 'paid') AND created_at > 1", "payments", "payments_tg"},
		{"SELECT * FROM payments WHERE tg_id = 1 AND tariff_id = 1 AND provider = 'stars' AND kind = 'new' AND user_id IS NOT DISTINCT FROM NULLIF(CAST(0 AS BIGINT), 0) AND status = 'pending' AND pay_url <> '' AND created_at > 1 ORDER BY id DESC LIMIT 1", "payments", "payments_tg"},
		{"SELECT * FROM inbound_reach WHERE at >= 1", "inbound_reach", "inbound_reach_at"},
		{"DELETE FROM inbound_reach WHERE inbound_id = 1", "inbound_reach", "inbound_reach_inbound"},
		{"DELETE FROM sub_fetches WHERE fetched_at < 1", "sub_fetches", "sub_fetches_fetched"},
		{"DELETE FROM inbound_events WHERE created_at < 1", "inbound_events", "inbound_events_created"},
		{"DELETE FROM tg_notices WHERE sent_at < 1", "tg_notices", "tg_notices_sent"},
		{"DELETE FROM user_pools WHERE pool_id = 1", "user_pools", "user_pools_pool"},
		{"DELETE FROM tariff_pools WHERE pool_id = 1", "tariff_pools", "tariff_pools_pool"},
		{"SELECT id FROM slots WHERE state = 'free' ORDER BY id LIMIT 1", "slots", "slots_free"},
		{"SELECT user_id, sum(remaining) FROM traffic_grants WHERE remaining > 0 GROUP BY user_id", "traffic_grants", "traffic_grants_spendable"},
	} {
		rows, err := conn.QueryContext(ctx, "EXPLAIN "+c.sql)
		if err != nil {
			t.Fatalf("%s: %v", c.sql, err)
		}
		var plan []string
		for rows.Next() {
			var detail string
			if err := rows.Scan(&detail); err != nil {
				t.Fatal(err)
			}
			plan = append(plan, detail)
		}
		rows.Close()
		text := strings.Join(plan, "; ")
		if !strings.Contains(text, c.index) {
			t.Errorf("%s\n  plan: %s\n  want a search by %s", c.sql, text, c.index)
		}
		if strings.Contains(text, "SCAN "+c.table) && !strings.Contains(text, c.index) {
			t.Errorf("%s scans %s: %s", c.sql, c.table, text)
		}
	}
}

// FindOpenPayment's user 0 is an invoice without a user (NULL), never somebody else's.
func TestFindOpenPaymentUserZero(t *testing.T) {
	ctx := context.Background()
	st, err := OpenTest(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	for _, q := range []string{
		"INSERT INTO tariffs(id,name,duration_days,created_at) VALUES(5,'t',30,1)",
		"INSERT INTO users(id,name,sub_token,period_start,created_at,updated_at) VALUES(41,'u','tok',1,1,1)",
		"INSERT INTO payments(id,provider,payload,tg_id,kind,user_id,tariff_id,tariff_name,amount,currency,status,pay_url,created_at) VALUES(1,'stars','p1',7,'new',NULL,5,'t',1,'XTR','pending','u',10),(2,'stars','p2',7,'renew',41,5,'t',1,'XTR','pending','u',10)",
	} {
		if _, err := st.DB.ExecContext(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	find := func(kind string, user int64) int64 {
		p, err := st.Q.FindOpenPayment(ctx, db.FindOpenPaymentParams{TgID: 7, TariffID: sql.NullInt64{Int64: 5, Valid: true}, Provider: "stars", Kind: kind, UserID: user, Since: 1})
		if errors.Is(err, sql.ErrNoRows) {
			return 0
		} else if err != nil {
			t.Fatal(err)
		}
		return p.ID
	}
	if find("new", 0) != 1 || find("renew", 41) != 2 || find("renew", 0) != 0 || find("new", 41) != 0 {
		t.Fatal("FindOpenPayment matched the wrong user")
	}
}
