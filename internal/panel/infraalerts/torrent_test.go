package infraalerts

import (
	"database/sql"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"

	"mikan/internal/panel/domain"
	"mikan/internal/panel/settings"
	"mikan/internal/panel/store/db"
)

// New catches reach the admin in one message a round; with the event off the cursor still
// moves, so switching it on does not bring back old catches.
func TestTorrentCatchesAlertTheAdmin(t *testing.T) {
	st, ctx := testMonitorStore(t)
	now := time.Unix(1_800_000_000, 0)
	clock := func() time.Time { return now }
	if err := domain.Seed(ctx, st, now); err != nil {
		t.Fatal(err)
	}
	m := New(st, settings.New(st.Q), nil, nil, nil, nil, nil, slog.Default(), clock)
	state, err := m.load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	tariffs, _ := st.Q.ListTariffs(ctx)
	users := domain.NewUsers(st, domain.NewPool(st, clock), noChanges{}, clock)
	u, err := users.Create(ctx, domain.CreateInput{Name: "<pirate>", TariffID: tariffs[1].ID})
	if err != nil {
		t.Fatal(err)
	}
	add := func(n int) {
		for i := 0; i < n; i++ {
			if _, err := st.Q.AddTorrentHit(ctx, db.AddTorrentHitParams{UserID: u.ID, NodeID: sql.NullInt64{Int64: 1, Valid: true},
				Ip: "203.0.113.5", Inbound: "vless", Network: "tcp", Kind: "handshake", Dest: fmt.Sprintf("198.51.100.%d:6881", i+1),
				Hits: 2, At: now.Unix(), BannedUntil: now.Unix() + 3600}); err != nil {
				t.Fatal(err)
			}
		}
	}
	add(12)
	cfg := Default()
	m.torrentEvents(ctx, &state, cfg, "en")
	if len(state.Pending) != 1 {
		t.Fatalf("one message a round: %+v", state.Pending)
	}
	text := state.Pending[0].Text
	for _, want := range []string{"Torrent caught", "&lt;pirate&gt;", "handshake", "198.51.100.1:6881", "×2", "banned until", "and 2 more"} {
		if !strings.Contains(text, want) {
			t.Fatalf("alert %q lacks %q", text, want)
		}
	}
	if strings.Contains(text, "198.51.100.11:6881") || strings.Contains(text, "—") {
		t.Fatalf("alert %q lists more than %d or has an em dash", text, torrentShown)
	}
	m.torrentEvents(ctx, &state, cfg, "en")
	if len(state.Pending) != 1 {
		t.Fatal("no new catches, no new message")
	}

	cfg.Events.Torrent = false
	add(1)
	m.torrentEvents(ctx, &state, cfg, "en")
	cfg.Events.Torrent = true
	m.torrentEvents(ctx, &state, cfg, "en")
	if len(state.Pending) != 1 {
		t.Fatalf("catches made while the event was off stay quiet: %+v", state.Pending)
	}
}
