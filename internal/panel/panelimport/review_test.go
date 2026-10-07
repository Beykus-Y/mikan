package panelimport

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/netip"
	"strconv"
	"testing"
	"time"

	"mikan/internal/panel/domain"
	"mikan/internal/panel/store"
)

// A panel that caps limit below ours gives short pages: they are not the end, the offset
// moves by what came. A panel that gives fewer users than it says it has fails the import.
func TestFetchMarzbanCappedLimit(t *testing.T) {
	all := []map[string]any{}
	for i := range 5 {
		all = append(all, map[string]any{"username": fmt.Sprintf("u%d", i), "status": "active"})
	}
	serveUsers := func(total int) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/api/admin/token" {
				_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "tok"})
				return
			}
			off, _ := strconv.Atoi(r.URL.Query().Get("offset"))
			end := min(off+2, len(all)) // the panel's own cap: 2, whatever limit asks
			_ = json.NewEncoder(w).Encode(map[string]any{"users": all[min(off, end):end], "total": total})
		}
	}
	got, err := Fetch(context.Background(), Client(), Source{Kind: Marzban, URL: serve(t, serveUsers(5)).URL, Username: "a", Password: "b"})
	if err != nil || len(got) != 5 {
		t.Fatalf("a capped limit: %d users, %v", len(got), err)
	}
	if _, err := Fetch(context.Background(), Client(), Source{Kind: Marzban, URL: serve(t, serveUsers(7)).URL, Username: "a", Password: "b"}); !errors.Is(err, ErrIncomplete) {
		t.Fatalf("users missing: %v", err)
	}
}

// A short UUID too short or odd to be a secret is not taken over as a link.
func TestRemnawaveWeakToken(t *testing.T) {
	for token, weak := range map[string]bool{"admin": true, "a": true, "Abc123Def456Ghi7": false, "abc$%^def456ghi789": true} {
		u, err := remnawaveUser{Username: "x", ShortUUID: token, Status: "ACTIVE"}.user()
		if err != nil || u.WeakToken != weak || (weak && u.Token != "") {
			t.Errorf("%q: weak %v token %q %v", token, u.WeakToken, u.Token, err)
		}
	}
	st, tariff := seeded(t)
	users := domain.NewUsers(st, domain.NewPool(st, time.Now), noChanges{}, time.Now)
	r, err := Apply(context.Background(), st, users, time.Now(), Remnawave, tariff, []User{{Name: "weak", Status: StatusActive, WeakToken: true}}, nil)
	if err != nil || r.Created != 1 || r.Links != 0 || r.NoLink.Count != 1 {
		t.Fatalf("report %+v %v", r, err)
	}
}

// An on-hold term past what time.Duration holds does not wrap into "no term".
func TestOnHoldOverflow(t *testing.T) {
	huge := int64(1) << 55
	u, err := marzbanUser{Username: "x", Status: "on_hold", OnHoldExpireDuration: &huge}.user()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Normalize(u); err == nil {
		t.Fatalf("an on-hold term of 2^55 seconds is taken: %v", u.OnHold)
	}
}

func TestAddressOKSpecial(t *testing.T) {
	for addr, ok := range map[string]bool{
		"fd00:ec2::254":          false, // AWS metadata over IPv6
		"64:ff9b::a9fe:a9fe":     false, // NAT64 of 169.254.169.254
		"64:ff9b::a00:1":         true,  // NAT64 of 10.0.0.1
		"100.64.0.1":             true,  // CGNAT: Tailscale and similar overlays
		"::ffff:169.254.169.254": false,
	} {
		if AddressOK(netip.MustParseAddr(addr)) != ok {
			t.Errorf("%s: want %v", addr, ok)
		}
	}
}

// One refill for what is missing before the users are made, one word to the nodes.
func TestRefillForTheImport(t *testing.T) {
	st, _ := seeded(t)
	ctx := context.Background()
	ch := &slotChanges{}
	pool := domain.NewPool(st, time.Now)
	users := domain.NewUsers(st, pool, ch, time.Now)
	before, err := pool.Stats(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := users.RefillFor(ctx, int(before.Free)+7); err != nil {
		t.Fatal(err)
	}
	after, _ := pool.Stats(ctx)
	if after.Free < before.Free+7 || ch.slots != 1 {
		t.Fatalf("free %d → %d, nodes told %d times", before.Free, after.Free, ch.slots)
	}
	if err := users.RefillFor(ctx, 1); err != nil || ch.slots != 1 {
		t.Fatalf("a refill nothing needed: %v, told %d", err, ch.slots)
	}
}

// An import of users mikan refuses or has already takes no slots: the pool is filled for
// the users that will be made, not for the whole list.
func TestApplyRefillsOnlyForNewUsers(t *testing.T) {
	st, tariff := seeded(t)
	ctx := context.Background()
	ch := &slotChanges{}
	pool := domain.NewPool(st, time.Now)
	users := domain.NewUsers(st, pool, ch, time.Now)
	if r, err := Apply(ctx, st, users, time.Now(), Marzban, tariff, []User{{Name: "kept", Status: StatusActive}}, nil); err != nil || r.Created != 1 {
		t.Fatalf("first import: %+v %v", r, err)
	}
	before, err := pool.Stats(ctx)
	if err != nil {
		t.Fatal(err)
	}
	told := ch.slots
	list := []User{{Name: "kept", Status: StatusActive}}
	for range before.Free + 3 {
		list = append(list, User{Name: "", Status: StatusActive})
	}
	r, err := Apply(ctx, st, users, time.Now(), Marzban, tariff, list, nil)
	if err != nil || r.Created != 0 || r.Skipped.Count != 1 {
		t.Fatalf("report %+v %v", r, err)
	}
	after, _ := pool.Stats(ctx)
	if after.Free != before.Free || ch.slots != told {
		t.Fatalf("free %d → %d, nodes told %d more times", before.Free, after.Free, ch.slots-told)
	}
}

type slotChanges struct{ slots int }

func (c *slotChanges) PoliciesChanged() {}
func (c *slotChanges) SlotsChanged()    { c.slots++ }

// A new link for a user takes its old panel's links away: a leaked one must not serve the
// new keys.
func TestReissueDropsOldLinks(t *testing.T) {
	st, tariff := seeded(t)
	ctx := context.Background()
	users := domain.NewUsers(st, domain.NewPool(st, time.Now), noChanges{}, time.Now)
	if _, err := Apply(ctx, st, users, time.Now(), Remnawave, tariff, []User{{Name: "leaked", Status: StatusActive, Token: "Abc123Def456Ghi7"}}, nil); err != nil {
		t.Fatal(err)
	}
	row, err := st.Q.LegacySubTokenUser(ctx, "Abc123Def456Ghi7")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := users.Reissue(ctx, row.User.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Q.LegacySubTokenUser(ctx, "Abc123Def456Ghi7"); err == nil {
		t.Fatal("the old link still leads to the user after a reissue")
	}
}

func importer(t *testing.T) (*Importer, *store.Store, int64) {
	st, tariff := seeded(t)
	users := domain.NewUsers(st, domain.NewPool(st, time.Now), noChanges{}, time.Now)
	return NewImporter(st, users, nil, time.Now, nil), st, tariff
}

func waitState(t *testing.T, im *Importer) JobState {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if s := im.State(); s.State == "done" || s.State == "failed" {
			return s
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("the job did not end")
	return JobState{}
}

func marzbanSource(t *testing.T, n int, slow chan struct{}) Source {
	var list []map[string]any
	for i := range n {
		list = append(list, map[string]any{"username": fmt.Sprintf("u%d", i), "status": "active"})
	}
	srv := serve(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/admin/token" {
			_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "tok"})
			return
		}
		if slow != nil {
			select {
			case <-slow:
			case <-r.Context().Done():
				return
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"users": list, "total": len(list)})
	})
	return Source{Kind: Marzban, URL: srv.URL, Username: "a", Password: "b"}
}

// Cancel stops a job that is reading the old panel.
func TestJobCancel(t *testing.T) {
	im, _, tariff := importer(t)
	block := make(chan struct{})
	defer close(block)
	if err := im.Start(marzbanSource(t, 3, block), tariff, "marzban test"); err != nil {
		t.Fatal(err)
	}
	if err := im.Start(marzbanSource(t, 1, nil), tariff, "again"); !errors.Is(err, ErrBusy) {
		t.Fatalf("a second job: %v", err)
	}
	im.Cancel()
	if s := waitState(t, im); s.State != "failed" || s.Error != "import_cancelled" {
		t.Fatalf("after cancel: %+v", s)
	}
}

// A cancel in the middle of making users keeps those made and their report, and the old
// links' kind is still filed.
func TestJobCancelKeepsThePartialReport(t *testing.T) {
	im, st, tariff := importer(t)
	var done Kind
	im.Done = func(_ context.Context, k Kind) { done = k }
	afterCreate = func(name string) error {
		if name == "u1" {
			im.Cancel()
		}
		return nil
	}
	defer func() { afterCreate = nil }()
	if err := im.Start(marzbanSource(t, 5, nil), tariff, "marzban test"); err != nil {
		t.Fatal(err)
	}
	s := waitState(t, im)
	if s.State != "failed" || s.Error != "import_cancelled" || s.Report == nil || s.Report.Created < 1 || done != Marzban {
		t.Fatalf("a cancelled import: %+v, done %q", s, done)
	}
	if taken, _ := st.Q.UserNameTaken(context.Background(), "u0"); !taken {
		t.Fatal("a user made before the cancel is gone")
	}
}

// A panic in a job is a failed job, not a panel that goes down.
func TestJobPanic(t *testing.T) {
	im, _, tariff := importer(t)
	jobHook = func(string) { panic("a bug") }
	defer func() { jobHook = nil }()
	if err := im.Start(marzbanSource(t, 1, nil), tariff, "marzban test"); err != nil {
		t.Fatal(err)
	}
	if s := waitState(t, im); s.State != "failed" || s.Error != "import_failed" {
		t.Fatalf("after a panic: %+v", s)
	}
}

func TestJobTimeout(t *testing.T) {
	defer func(d time.Duration) { JobTimeout = d }(JobTimeout)
	JobTimeout = 200 * time.Millisecond
	im, _, tariff := importer(t)
	block := make(chan struct{})
	defer close(block)
	if err := im.Start(marzbanSource(t, 1, block), tariff, "marzban test"); err != nil {
		t.Fatal(err)
	}
	if s := waitState(t, im); s.State != "failed" || (s.Error != "import_timeout" && s.Error != "import_unreachable") {
		t.Fatalf("after the time ran out: %+v", s)
	}
}

// When the panel stops, a running job is stopped and Run waits for it.
func TestJobStopsWithThePanel(t *testing.T) {
	im, _, tariff := importer(t)
	ctx, cancel := context.WithCancel(context.Background())
	ran := make(chan struct{})
	go func() { im.Run(ctx); close(ran) }()
	for im.State().State != "idle" {
		time.Sleep(5 * time.Millisecond)
	}
	time.Sleep(20 * time.Millisecond) // Run has taken ctx
	block := make(chan struct{})
	defer close(block)
	if err := im.Start(marzbanSource(t, 1, block), tariff, "marzban test"); err != nil {
		t.Fatal(err)
	}
	cancel()
	select {
	case <-ran:
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not return")
	}
	if s := im.State(); s.State != "failed" {
		t.Fatalf("the job after the panel stopped: %+v", s)
	}
	if err := im.Start(marzbanSource(t, 1, nil), tariff, "late"); !errors.Is(err, ErrCancelled) {
		t.Fatalf("a job after the panel stopped: %v", err)
	}
}
