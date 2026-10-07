package nodesync

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"mikan/internal/nodeapi"
	"mikan/internal/panel/domain"
	"mikan/internal/panel/settings"
	"mikan/internal/panel/store"
	"mikan/internal/panel/store/db"
	"mikan/internal/panel/torrent"
)

type torrentNode struct {
	*fakeNode
	hits  nodeapi.TorrentHits
	asked int
}

func (n *torrentNode) Torrents(_ context.Context, epoch string, after int64) (nodeapi.TorrentHits, error) {
	n.asked++
	out := nodeapi.TorrentHits{Epoch: n.hits.Epoch}
	for _, h := range n.hits.Hits {
		if epoch != n.hits.Epoch || h.Seq > after {
			out.Hits = append(out.Hits, h)
		}
	}
	return out, nil
}

func policyOf(t *testing.T, s *Syncer, userID int64) nodeapi.Policy {
	t.Helper()
	_, ps, owners, err := s.policies(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range ps {
		if owners[p.Slot] == userID {
			return p
		}
	}
	t.Fatalf("no policy for user %d", userID)
	return nodeapi.Policy{}
}

// A catch a node reports bans its user on every node from the panel's clock, once, and
// the exempt are left alone.
func TestTorrentHitsBanOnEveryNode(t *testing.T) {
	s, fake, st, users, now := setup(t)
	ctx := context.Background()
	node := &torrentNode{fakeNode: fake}
	s.node = node
	tariffs, _ := st.Q.ListTariffs(ctx)
	caught, _ := users.Create(ctx, domain.CreateInput{Name: "caught", TariffID: tariffs[2].ID})
	free, _ := users.Create(ctx, domain.CreateInput{Name: "free", TariffID: tariffs[2].ID})
	slotOf := func(u db.User) string {
		sl, err := st.Q.GetSlot(ctx, u.SlotID.Int64)
		if err != nil {
			t.Fatal(err)
		}
		return sl.Name
	}
	reported := nodeapi.TorrentHits{Epoch: "e1", Hits: []nodeapi.TorrentHit{
		{Seq: 1, Slot: slotOf(caught), IP: "203.0.113.5", Inbound: "vless", Network: "tcp", Kind: nodeapi.TorrentHandshake, Dest: "198.51.100.1:6881", Count: 3, BannedUntil: 1},
		{Seq: 2, Slot: slotOf(free), IP: "203.0.113.6", Inbound: "vless", Network: "udp", Kind: nodeapi.TorrentDHT, Dest: "198.51.100.2:6881", Count: 1},
		{Seq: 3, Slot: "s-nobody", IP: "203.0.113.7", Network: "udp", Kind: nodeapi.TorrentUTP, Count: 1},
	}}
	node.hits = nodeapi.TorrentHits{Epoch: "e1"}
	s.pullTorrents(ctx)
	if node.asked != 0 {
		t.Fatal("a blocker that is off does not ask the nodes")
	}

	cfg := torrent.Config{Enabled: true, BanMinutes: 30, Exempt: []int64{free.ID}}
	if err := settings.Set(ctx, settings.New(st.Q), torrent.KeyConfig, cfg); err != nil {
		t.Fatal(err)
	}
	s.m.SlotsChanged()
	st0, err := s.desired(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if st0.Torrent == nil || st0.Torrent.BanSeconds != 1800 {
		t.Fatalf("the nodes get the blocker with their state: %+v", st0.Torrent)
	}
	if p := policyOf(t, s, free.ID); !p.TorrentExempt {
		t.Fatalf("the exempt user's policy says so: %+v", p)
	}

	s.pullTorrents(ctx) // the first pull after the switch-on only takes the node's position
	node.hits = reported
	s.pullTorrents(ctx)
	s.pullTorrents(ctx) // the same hits again: the cursor skips them
	rows, err := st.Q.ListTorrentHits(ctx, db.ListTorrentHitsParams{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].UserID != caught.ID || rows[0].Hits != 3 || rows[0].BannedUntil != now.Unix()+1800 {
		t.Fatalf("one hit, the exempt and the unknown left out, the ban from the panel's clock: %+v", rows)
	}
	if p := policyOf(t, s, caught.ID); p.BannedUntil != now.Unix()+1800 {
		t.Fatalf("the ban goes to the nodes: %+v", p)
	}

	// The node restarted: its sequence starts over under a new epoch.
	node.hits = nodeapi.TorrentHits{Epoch: "e2", Hits: []nodeapi.TorrentHit{
		{Seq: 1, Slot: slotOf(caught), IP: "203.0.113.5", Network: "udp", Kind: nodeapi.TorrentUTP, Count: 1},
	}}
	*now = now.Add(time.Minute)
	s.pullTorrents(ctx)
	if rows, _ = st.Q.ListTorrentHits(ctx, db.ListTorrentHitsParams{Limit: 10}); len(rows) != 2 {
		t.Fatalf("a new epoch's hits are stored: %d", len(rows))
	}
	s.m.PoliciesChanged()
	if p := policyOf(t, s, caught.ID); p.BannedUntil != now.Unix()+1800 {
		t.Fatalf("the latest ban holds: %d, want %d", p.BannedUntil, now.Unix()+1800)
	}

	if n, err := st.Q.LiftTorrentBans(ctx, db.LiftTorrentBansParams{UserID: caught.ID, Now: now.Unix()}); err != nil || n != 2 {
		t.Fatalf("lift: %d %v", n, err)
	}
	s.m.PoliciesChanged()
	if p := policyOf(t, s, caught.ID); p.BannedUntil != 0 {
		t.Fatalf("a lifted ban is gone: %+v", p)
	}

	// Switched off, the bans stop holding: the admin turned the blocker off, not just the catching.
	cfg.Enabled = false
	_ = settings.Set(ctx, settings.New(st.Q), torrent.KeyConfig, cfg)
	s.m.SlotsChanged()
	if st1, _ := s.desired(ctx); st1.Torrent != nil {
		t.Fatal("the blocker is off on the nodes")
	}
}

// torrentHarness is a panel with the blocker on (a 30 minute ban), one node reporting hits
// and a few users, each with a slot.
type torrentHarness struct {
	t     *testing.T
	s     *Syncer
	node  *torrentNode
	st    *store.Store
	users *domain.Users
	now   *time.Time
	cfg   torrent.Config
	seq   int64
}

func newTorrentHarness(t *testing.T) *torrentHarness {
	t.Helper()
	s, fake, st, users, now := setup(t)
	h := &torrentHarness{t: t, s: s, node: &torrentNode{fakeNode: fake, hits: nodeapi.TorrentHits{Epoch: "e1"}}, st: st, users: users,
		now: now, cfg: torrent.Config{Enabled: true, BanMinutes: 30, Exempt: []int64{}}}
	s.node = h.node
	h.setConfig(h.cfg)
	// The first pull after the blocker went on only takes the node's position.
	s.pullTorrents(context.Background())
	return h
}

func (h *torrentHarness) setConfig(cfg torrent.Config) {
	h.t.Helper()
	h.cfg = cfg
	if err := settings.Set(context.Background(), settings.New(h.st.Q), torrent.KeyConfig, cfg); err != nil {
		h.t.Fatal(err)
	}
	h.s.m.SlotsChanged()
}

func (h *torrentHarness) user(name string) db.User {
	h.t.Helper()
	ctx := context.Background()
	tariffs, _ := h.st.Q.ListTariffs(ctx)
	u, err := h.users.Create(ctx, domain.CreateInput{Name: name, TariffID: tariffs[2].ID})
	if err != nil {
		h.t.Fatal(err)
	}
	return u
}

// hit makes the node report one catch of the user's slot, at the given time.
func (h *torrentHarness) hit(u db.User, network, kind string, at time.Time, count int) {
	h.t.Helper()
	sl, err := h.st.Q.GetSlot(context.Background(), u.SlotID.Int64)
	if err != nil {
		h.t.Fatal(err)
	}
	h.seq++
	h.node.hits.Hits = append(h.node.hits.Hits, nodeapi.TorrentHit{Seq: h.seq, Slot: sl.Name, IP: "203.0.113.5", Inbound: "vless",
		Network: network, Kind: kind, Dest: "198.51.100.1:80", At: at.Unix(), Count: count})
}

func (h *torrentHarness) pull() {
	h.t.Helper()
	h.s.pullTorrents(context.Background())
	h.s.m.PoliciesChanged()
}

func (h *torrentHarness) banOf(u db.User) int64 { return policyOf(h.t, h.s, u.ID).BannedUntil }

func (h *torrentHarness) rowsOf(u db.User) []db.ListTorrentHitsRow {
	h.t.Helper()
	rows, err := h.st.Q.ListTorrentHits(context.Background(), db.ListTorrentHitsParams{UserID: sql.NullInt64{Int64: u.ID, Valid: true}, Limit: 50})
	if err != nil {
		h.t.Fatal(err)
	}
	return rows
}

// An HTTP tracker line is plain text a web page can make the browser send: one is stored
// but bans nobody; three within ten minutes ban. A UDP tracker connect or a handshake
// cannot be forged by a page and bans at once.
func TestTorrentHTTPTrackerNeedsRepeats(t *testing.T) {
	h := newTorrentHarness(t)
	now := *h.now

	one := h.user("one")
	h.hit(one, "tcp", nodeapi.TorrentTracker, now, 1)
	h.pull()
	if len(h.rowsOf(one)) != 1 || h.banOf(one) != 0 {
		t.Fatalf("one tracker line: the hit is stored, no ban (rows %d, ban %d)", len(h.rowsOf(one)), h.banOf(one))
	}
	*h.now = now.Add(time.Minute)
	h.hit(one, "tcp", nodeapi.TorrentTracker, *h.now, 1)
	h.pull()
	if h.banOf(one) != 0 {
		t.Fatal("two tracker lines do not ban")
	}
	*h.now = now.Add(2 * time.Minute)
	h.hit(one, "tcp", nodeapi.TorrentTracker, *h.now, 1)
	h.pull()
	if want := h.now.Unix() + 1800; h.banOf(one) != want {
		t.Fatalf("three within the window ban: %d, want %d", h.banOf(one), want)
	}
	// Lifted by the admin: the lines before the lift do not count, one more is one.
	if _, err := h.st.Q.LiftTorrentBans(context.Background(), db.LiftTorrentBansParams{UserID: one.ID, Now: h.now.Unix()}); err != nil {
		t.Fatal(err)
	}
	*h.now = now.Add(3 * time.Minute)
	h.hit(one, "tcp", nodeapi.TorrentTracker, *h.now, 1)
	h.pull()
	if h.banOf(one) != 0 {
		t.Fatalf("one tracker line after a lift bans again: %d", h.banOf(one))
	}
	*h.now = now.Add(2 * time.Minute)

	// Three catches the node folded into one hit count as three.
	folded := h.user("folded")
	h.hit(folded, "tcp", nodeapi.TorrentTracker, *h.now, 3)
	h.pull()
	if h.banOf(folded) == 0 {
		t.Fatal("a hit that stands for three catches bans")
	}

	// Three lines spread over more than ten minutes never add up.
	slow := h.user("slow")
	for i := 0; i < 3; i++ {
		*h.now = h.now.Add(6 * time.Minute)
		h.hit(slow, "tcp", nodeapi.TorrentTracker, *h.now, 1)
		h.pull()
	}
	if len(h.rowsOf(slow)) != 3 || h.banOf(slow) != 0 {
		t.Fatalf("three lines 6 minutes apart do not ban (rows %d, ban %d)", len(h.rowsOf(slow)), h.banOf(slow))
	}

	// What cannot be forged bans at once.
	udp := h.user("udp")
	h.hit(udp, "udp", nodeapi.TorrentTracker, *h.now, 1)
	hs := h.user("handshake")
	h.hit(hs, "tcp", nodeapi.TorrentHandshake, *h.now, 1)
	h.pull()
	if h.banOf(udp) == 0 || h.banOf(hs) == 0 {
		t.Fatalf("a UDP tracker connect and a handshake ban at once: %d %d", h.banOf(udp), h.banOf(hs))
	}
}

// The ban counts from the catch, not from the pull; a node that was out of reach does not
// make fresh bans out of old hits.
func TestTorrentBanFromHitTime(t *testing.T) {
	h := newTorrentHarness(t)
	now := *h.now

	late := h.user("late")
	h.hit(late, "tcp", nodeapi.TorrentHandshake, now.Add(-10*time.Minute), 1)
	old := h.user("old")
	h.hit(old, "tcp", nodeapi.TorrentHandshake, now.Add(-31*time.Minute), 1)
	skewed := h.user("skewed")
	h.hit(skewed, "tcp", nodeapi.TorrentHandshake, now.Add(time.Hour), 1) // a node clock far ahead
	near := h.user("near")
	h.hit(near, "tcp", nodeapi.TorrentHandshake, now.Add(time.Minute), 1) // a small skew is let be
	h.pull()

	if want := now.Add(20 * time.Minute).Unix(); h.banOf(late) != want {
		t.Fatalf("a hit from 10 minutes ago bans for the 20 that are left: %d, want %d", h.banOf(late), want)
	}
	if len(h.rowsOf(old)) != 0 || h.banOf(old) != 0 {
		t.Fatalf("a hit whose ban is already over is skipped (rows %d, ban %d)", len(h.rowsOf(old)), h.banOf(old))
	}
	if want := now.Add(30 * time.Minute).Unix(); h.banOf(skewed) != want {
		t.Fatalf("a hit from the future counts from now: %d, want %d", h.banOf(skewed), want)
	}
	if want := now.Add(31 * time.Minute).Unix(); h.banOf(near) != want {
		t.Fatalf("a small skew keeps the node's time: %d, want %d", h.banOf(near), want)
	}
	if rows := h.rowsOf(late); len(rows) != 1 || rows[0].At != now.Add(-10*time.Minute).Unix() {
		t.Fatalf("the hit is stored at its own time: %+v", rows)
	}
}

// Hits that wait on the node since before the blocker was switched off must not turn into
// bans when it is switched on again.
func TestTorrentOffAndOnDoesNotBanOldHits(t *testing.T) {
	h := newTorrentHarness(t)
	now := *h.now
	u := h.user("u")

	// Caught just before the admin turned the blocker off; the panel has not read it yet.
	h.hit(u, "tcp", nodeapi.TorrentHandshake, now, 1)
	off := h.cfg
	off.Enabled = false
	h.setConfig(off)
	asked := h.node.asked
	h.pull()
	if h.node.asked != asked || len(h.rowsOf(u)) != 0 {
		t.Fatalf("an off blocker reads nothing (asked %d, rows %d)", h.node.asked-asked, len(h.rowsOf(u)))
	}

	*h.now = now.Add(time.Minute) // well inside the ban time: the age rule alone would not skip it
	on := h.cfg
	on.Enabled = true
	h.setConfig(on)
	h.pull()
	if len(h.rowsOf(u)) != 0 || h.banOf(u) != 0 {
		t.Fatalf("a hit from before the switch-off must not ban (rows %d, ban %d)", len(h.rowsOf(u)), h.banOf(u))
	}

	// A catch after the switch-on is a fresh one.
	*h.now = now.Add(2 * time.Minute)
	h.hit(u, "tcp", nodeapi.TorrentHandshake, *h.now, 1)
	h.pull()
	if want := h.now.Unix() + 1800; h.banOf(u) != want {
		t.Fatalf("a new catch bans: %d, want %d", h.banOf(u), want)
	}
}

// A user put on the exemption list gets no ban, even one that was already running.
func TestTorrentExemptGetsNoBan(t *testing.T) {
	h := newTorrentHarness(t)
	u := h.user("u")
	h.hit(u, "tcp", nodeapi.TorrentHandshake, *h.now, 1)
	h.pull()
	if h.banOf(u) == 0 {
		t.Fatal("caught")
	}
	cfg := h.cfg
	cfg.Exempt = []int64{u.ID}
	h.setConfig(cfg)
	h.s.m.PoliciesChanged()
	if p := policyOf(t, h.s, u.ID); !p.TorrentExempt || p.BannedUntil != 0 {
		t.Fatalf("an exempt user gets no ban: %+v", p)
	}
}
