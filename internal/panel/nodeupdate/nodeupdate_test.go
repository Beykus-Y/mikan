package nodeupdate

import (
	"context"
	"crypto/x509"
	"errors"
	"io"
	"log/slog"
	"slices"
	"sync"
	"testing"
	"time"

	"mikan/internal/nodeapi"
	"mikan/internal/nodetls"
	"mikan/internal/panel/domain"
	"mikan/internal/panel/nodesync"
	"mikan/internal/panel/settings"
	"mikan/internal/panel/store"
	"mikan/internal/panel/store/db"
	"mikan/internal/panel/store/storetest"
)

// fakeNodes stands in for the running nodes: what their health says, and what the panel
// asked of them.
type fakeNodes struct {
	mu     sync.Mutex
	health map[int64]nodesync.HealthView
	asked  []string // "id version", in order
	fail   map[int64]error
}

func (f *fakeNodes) Health(id int64) (nodesync.HealthView, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	hv, ok := f.health[id]
	return hv, ok
}

func (f *fakeNodes) RequestUpdate(_ context.Context, id int64, version string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.fail[id]; err != nil {
		return err
	}
	f.asked = append(f.asked, idVersion(id, version))
	return nil
}

func idVersion(id int64, v string) string { return string(rune('0'+id)) + " " + v }

func (f *fakeNodes) up(id int64, version string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.health[id] = nodesync.HealthView{OK: true, CheckedAt: time.Unix(1_800_000_000, 0), Health: nodeapi.Health{Version: version}}
}

func (f *fakeNodes) report(id int64, s nodeapi.UpdateStatus) {
	f.mu.Lock()
	defer f.mu.Unlock()
	hv := f.health[id]
	hv.Health.Update = &s
	f.health[id] = hv
}

func (f *fakeNodes) down(id int64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.health[id] = nodesync.HealthView{OK: false, Error: "unreachable", CheckedAt: time.Unix(1_800_000_000, 0)}
}

func (f *fakeNodes) log() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.asked)
}

type rig struct {
	t     *testing.T
	st    *store.Store
	set   *settings.Settings
	nodes *fakeNodes
	now   time.Time
	svc   *Service
	ctx   context.Context
	// ids of the remote nodes: 2, 3, 4; the panel's own is 1
}

// newRig has the panel's own node (1) and three remote ones (2, 3, 4), all on 0.5.0.1,
// and a panel on 0.5.0.2.
func newRig(t *testing.T, panelVersion string) *rig {
	t.Helper()
	ctx := context.Background()
	st, err := storetest.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	r := &rig{t: t, st: st, set: settings.New(st.Q), ctx: ctx, now: time.Unix(1_800_000_000, 0),
		nodes: &fakeNodes{health: map[int64]nodesync.HealthView{}, fail: map[int64]error{}}}
	if err := domain.Seed(ctx, st, r.now); err != nil {
		t.Fatal(err)
	}
	panel, _ := nodetls.Generate("mikan-panel", x509.ExtKeyUsageClientAuth, r.now)
	for i, host := range []string{"198.51.100.2", "198.51.100.3", "198.51.100.4"} {
		n, _, err := domain.AddNode(ctx, st, panel, domain.NodeInput{Name: "N" + string(rune('A'+i)), Host: host, APIPort: 40000 + i}, r.now)
		if err != nil {
			t.Fatal(err)
		}
		if n.ID != int64(i+2) {
			t.Fatalf("node ids: %d", n.ID)
		}
	}
	for id := int64(1); id <= 4; id++ {
		r.nodes.up(id, "0.5.0.1")
	}
	r.svc = r.service(panelVersion)
	return r
}

func (r *rig) service(version string) *Service {
	return New(r.st, r.set, r.nodes, version, slog.New(slog.NewTextHandler(io.Discard, nil)), func() time.Time { return r.now })
}

func (r *rig) tick() {
	r.t.Helper()
	if err := r.svc.Tick(r.ctx); err != nil {
		r.t.Fatal(err)
	}
}

func (r *rig) node(id int64) db.Node {
	r.t.Helper()
	n, err := r.st.Q.GetNode(r.ctx, id)
	if err != nil {
		r.t.Fatal(err)
	}
	return n
}

func (r *rig) attempt(id int64) Attempt {
	r.t.Helper()
	a, ok := r.svc.Attempt(r.ctx, id)
	if !ok {
		r.t.Fatalf("node %d: no attempt", id)
	}
	return a
}

func (r *rig) want(asked ...string) {
	r.t.Helper()
	if got := r.nodes.log(); !slices.Equal(got, asked) {
		r.t.Fatalf("asked %q, want %q", got, asked)
	}
}

// Versions of three and four numbers compare as the panel and the installer compare them.
func TestBehindAndCanUpdate(t *testing.T) {
	for _, c := range []struct {
		panel, node string
		behind      bool
	}{
		{"0.5.0.2", "0.5.0.1", true},
		{"0.5.0.2", "0.5.0.2", false},
		{"0.5.0.2", "0.5.0.3", false},
		{"0.5.0.2", "0.4.5", true},
		{"0.5.0.0", "0.4.5", true},
		{"0.4.5", "0.4.5.0", false},
		{"0.4.5.1", "0.4.5", true},
		{"0.4.10", "0.4.9.99", true},
		{"0.5.0.2", "0.5.0.2-rc.1", true},
		{"0.5.0.2-rc.1", "0.5.0.2", false},
		{"0.5.0.2", "dev", true},
		{"0.5.0.2", "", true},
		// a panel that runs no release has no node to be ahead of
		{"dev", "0.5.0.1", false},
		{"", "0.5.0.1", false},
		{"v0.5.0.2", "0.5.0.1", false},
		{"0.5.0.2 ", "0.5.0.1", false},
	} {
		if got := Behind(c.panel, c.node); got != c.behind {
			t.Errorf("Behind(%q, %q) = %v", c.panel, c.node, got)
		}
	}
	for v, want := range map[string]bool{
		"0.5.0.2": true, "0.5.0.3": true, "0.5.1.0": true, "0.6.0": true, "1.0.0": true, "0.5.0.10": true,
		"0.5.0.1": false, "0.5.0": false, "0.4.5": false, "0.5.0.2-rc.1": false, "dev": false, "": false, "v0.5.0.2": false,
	} {
		if got := CanUpdate(v); got != want {
			t.Errorf("CanUpdate(%q) = %v", v, got)
		}
	}
}

// The admin's button: what is refused. Nothing is asked of a node in any of these cases.
func TestRequestRefusals(t *testing.T) {
	r := newRig(t, "0.5.0.3")
	for _, id := range []int64{2, 3, 4} {
		r.nodes.up(id, "0.5.0.2")
	}
	r.nodes.down(2)
	delete(r.nodes.health, 3)
	for name, c := range map[string]struct {
		id   int64
		want error
	}{
		"the panel's own node":        {1, ErrLocal},
		"a node that does not answer": {2, ErrOffline},
		"a node never heard of":       {3, ErrOffline},
	} {
		if err := r.svc.Request(r.ctx, r.node(c.id)); !errors.Is(err, c.want) {
			t.Errorf("%s: %v, want %v", name, err, c.want)
		}
	}
	// not behind: the same version, and a later one
	for _, v := range []string{"0.5.0.3", "0.5.0.4", "0.6.0.0"} {
		r.nodes.up(4, v)
		if err := r.svc.Request(r.ctx, r.node(4)); !errors.Is(err, ErrNotBehind) {
			t.Errorf("a node on %s: %v", v, err)
		}
	}
	// no endpoint before 0.5.0.2: the admin runs `mikan update` on that server once
	for _, v := range []string{"0.5.0.1", "0.5.0", "0.4.5", "dev"} {
		r.nodes.up(4, v)
		if err := r.svc.Request(r.ctx, r.node(4)); !errors.Is(err, ErrUnsupported) {
			t.Errorf("a node on %s: %v", v, err)
		}
	}
	// a panel that runs no release asks nothing, and "update all" does not start
	r.nodes.up(4, "0.5.0.2")
	for _, v := range []string{"dev", "", "v0.5.0.3", "latest"} {
		svc := r.service(v)
		if err := svc.Request(r.ctx, r.node(4)); !errors.Is(err, ErrNotRelease) {
			t.Errorf("panel %q: %v", v, err)
		}
		if err := svc.RequestAll(r.ctx); !errors.Is(err, ErrNotRelease) {
			t.Errorf("update all, panel %q: %v", v, err)
		}
		if err := svc.Tick(r.ctx); err != nil {
			t.Fatal(err)
		}
	}
	r.want()
}

// A node the panel cannot reach is not left marked as being updated.
func TestAnUndeliveredRequestLeavesNothingBehind(t *testing.T) {
	r := newRig(t, "0.5.0.3")
	r.nodes.up(2, "0.5.0.2")
	r.nodes.fail[2] = nodeapi.ErrUnavailable
	if err := r.svc.Request(r.ctx, r.node(2)); !errors.Is(err, nodeapi.ErrUnavailable) {
		t.Fatalf("request: %v", err)
	}
	if _, ok := r.svc.Attempt(r.ctx, 2); ok {
		t.Fatal("an attempt was kept for a request that did not go out")
	}
	delete(r.nodes.fail, 2)
	if err := r.svc.Request(r.ctx, r.node(2)); err != nil {
		t.Fatalf("the node is back: %v", err)
	}
}

// A node 0.5.0.2 or newer that is behind is asked for the panel's own version, and the
// panel remembers it; asked twice, the second time is refused while the first may still finish.
func TestRequestAsksForThePanelsVersion(t *testing.T) {
	r := newRig(t, "0.5.0.3")
	r.nodes.up(2, "0.5.0.2")
	if err := r.svc.Request(r.ctx, r.node(2)); err != nil {
		t.Fatal(err)
	}
	r.want("2 0.5.0.3")
	a := r.attempt(2)
	if a.Version != "0.5.0.3" || a.From != "0.5.0.2" || a.State != Running || a.At != r.now.Unix() {
		t.Fatalf("attempt: %+v", a)
	}
	if err := r.svc.Request(r.ctx, r.node(2)); !errors.Is(err, ErrBusy) {
		t.Fatalf("asked again at once: %v", err)
	}
	// ... but not for ever: an attempt that is past its time may be asked again
	r.now = r.now.Add(Timeout + time.Minute)
	if err := r.svc.Request(r.ctx, r.node(2)); err != nil {
		t.Fatalf("asked again after the time: %v", err)
	}
	r.want("2 0.5.0.3", "2 0.5.0.3")
}

// Nodes follow the panel one at a time, in order; the panel's own node and the nodes that
// cannot update themselves are left out.
func TestNodesFollowOneAtATime(t *testing.T) {
	r := newRig(t, "0.5.0.2")
	r.tick()
	r.want() // every node runs 0.5.0.1, which has no endpoint: nothing can be asked

	r = newRig(t, "0.5.0.3")
	for _, id := range []int64{2, 3, 4} {
		r.nodes.up(id, "0.5.0.2")
	}
	r.nodes.up(1, "0.5.0.2") // the panel's own node is behind too, as it is right after the panel's restart
	r.tick()
	r.want("2 0.5.0.3")
	// nothing more while it runs, however often the panel looks
	for range 3 {
		r.now = r.now.Add(15 * time.Second)
		r.tick()
	}
	r.want("2 0.5.0.3")
	// it reports it is on the way: still nothing
	r.nodes.report(2, nodeapi.UpdateStatus{State: nodeapi.UpdateRunning, Version: "0.5.0.3", From: "0.5.0.2"})
	r.tick()
	r.want("2 0.5.0.3")
	// it is up on the new version: the next one
	r.nodes.up(2, "0.5.0.3")
	r.tick()
	r.want("2 0.5.0.3", "3 0.5.0.3")
	if a := r.attempt(2); a.State != OK {
		t.Fatalf("node 2: %+v", a)
	}
	// the node's container restarts: its health does not answer for a while, and nothing else moves
	r.nodes.down(3)
	r.now = r.now.Add(time.Minute)
	r.tick()
	r.want("2 0.5.0.3", "3 0.5.0.3")
	r.nodes.up(3, "0.5.0.3")
	r.tick()
	r.want("2 0.5.0.3", "3 0.5.0.3", "4 0.5.0.3")
	r.nodes.up(4, "0.5.0.3")
	r.tick()
	r.tick()
	r.want("2 0.5.0.3", "3 0.5.0.3", "4 0.5.0.3") // all there; the panel's own node was never asked
	for id := int64(2); id <= 4; id++ {
		if a := r.attempt(id); a.State != OK {
			t.Fatalf("node %d: %+v", id, a)
		}
	}
	if _, ok := r.svc.Attempt(r.ctx, 1); ok {
		t.Fatal("the panel's own node has an attempt")
	}
}

// A node ahead of the panel, one that is not behind, one that does not answer, and an old
// one are skipped without stopping the rest.
func TestNodesThatCannotOrNeedNotBeUpdatedAreSkipped(t *testing.T) {
	r := newRig(t, "0.5.0.3")
	r.nodes.up(2, "0.5.0.1") // before the endpoint: updated by hand once
	r.nodes.down(3)
	r.nodes.up(4, "0.5.0.4") // ahead of the panel
	r.tick()
	r.want()
	r.nodes.up(3, "0.5.0.2")
	r.tick()
	r.want("3 0.5.0.3")
	// the old node does not hold up the others, nor do the ones that were fine already
	r.nodes.up(3, "0.5.0.3")
	r.tick()
	r.want("3 0.5.0.3")
}

// A failure stops the rollout: an alert for the admin, no other node asked, and the node is
// not asked again for the same version by the panel. The button still works, and so does
// switching "follow" off and on.
func TestAFailureStopsTheRollout(t *testing.T) {
	r := newRig(t, "0.5.0.3")
	for _, id := range []int64{2, 3, 4} {
		r.nodes.up(id, "0.5.0.2")
	}
	r.tick()
	r.want("2 0.5.0.3")
	// the node's updater went back to the old image and says why
	r.nodes.report(2, nodeapi.UpdateStatus{State: nodeapi.UpdateFailed, Version: "0.5.0.3", From: "0.5.0.2", Error: "mikan 0.5.0.3 did not start: going back"})
	r.tick()
	a := r.attempt(2)
	if a.State != Failed || a.Error != "mikan 0.5.0.3 did not start: going back" {
		t.Fatalf("attempt: %+v", a)
	}
	r.want("2 0.5.0.3")
	failures := r.svc.Failures(r.ctx)
	if len(failures) != 1 || failures[0].NodeID != 2 || failures[0].Version != "0.5.0.3" || failures[0].From != "0.5.0.2" || failures[0].Error == "" {
		t.Fatalf("failures: %+v", failures)
	}
	// the node is healthy again on its old version, a lot of time passes: still nothing, for
	// that node nor for the ones after it
	for range 5 {
		r.now = r.now.Add(time.Hour)
		r.tick()
	}
	r.want("2 0.5.0.3")
	if _, ok := r.svc.Attempt(r.ctx, 3); ok {
		t.Fatal("the rollout went on past a failure")
	}
	// the alert reads the same failure each time, with the same time to tell it by
	if again := r.svc.Failures(r.ctx); len(again) != 1 || again[0].At != failures[0].At {
		t.Fatalf("failures: %+v", again)
	}
	// the admin presses Update on the node: it goes out, and the rollout is its own again
	if err := r.svc.Request(r.ctx, r.node(2)); err != nil {
		t.Fatal(err)
	}
	r.want("2 0.5.0.3", "2 0.5.0.3")
	r.nodes.up(2, "0.5.0.3")
	r.tick()
	r.want("2 0.5.0.3", "2 0.5.0.3", "3 0.5.0.3")
	if len(r.svc.Failures(r.ctx)) != 0 {
		t.Fatalf("the failure stayed after the node was updated: %+v", r.svc.Failures(r.ctx))
	}
}

// Turning "follow" off and on is the admin asking for another try.
func TestSwitchingFollowOffAndOnTriesAgain(t *testing.T) {
	r := newRig(t, "0.5.0.3")
	r.nodes.up(2, "0.5.0.2")
	r.nodes.up(3, "0.5.0.2")
	r.tick()
	r.nodes.report(2, nodeapi.UpdateStatus{State: nodeapi.UpdateFailed, Error: "x"})
	r.tick()
	r.tick()
	r.want("2 0.5.0.3")
	if err := r.svc.SetFollowing(r.ctx, false); err != nil {
		t.Fatal(err)
	}
	r.tick()
	r.want("2 0.5.0.3")
	if len(r.svc.Failures(r.ctx)) != 1 {
		t.Fatal("switching off forgot the failure")
	}
	// the node's old report is gone with its next request, as the real node does
	r.nodes.mu.Lock()
	hv := r.nodes.health[2]
	hv.Health.Update = nil
	r.nodes.health[2] = hv
	r.nodes.mu.Unlock()
	if err := r.svc.SetFollowing(r.ctx, true); err != nil {
		t.Fatal(err)
	}
	if len(r.svc.Failures(r.ctx)) != 0 {
		t.Fatal("switching on kept the failure")
	}
	r.tick()
	r.want("2 0.5.0.3", "2 0.5.0.3")
}

// A node that never comes up on the version is a failure when the time is up, and the
// rollout stops there too.
func TestATimeoutIsAFailure(t *testing.T) {
	r := newRig(t, "0.5.0.3")
	r.nodes.up(2, "0.5.0.2")
	r.nodes.up(3, "0.5.0.2")
	r.tick()
	r.now = r.now.Add(Timeout - time.Minute)
	r.tick()
	if a := r.attempt(2); a.State != Running {
		t.Fatalf("before the time: %+v", a)
	}
	r.now = r.now.Add(2 * time.Minute)
	r.tick()
	if a := r.attempt(2); a.State != Failed || a.Error == "" {
		t.Fatalf("after the time: %+v", a)
	}
	r.tick()
	r.want("2 0.5.0.3")
	if fs := r.svc.Failures(r.ctx); len(fs) != 1 {
		t.Fatalf("failures: %+v", fs)
	}
}

// The panel's restart loses nothing: what it waits for, and what it must not repeat, is in
// the database.
func TestStateSurvivesARestart(t *testing.T) {
	r := newRig(t, "0.5.0.3")
	for _, id := range []int64{2, 3, 4} {
		r.nodes.up(id, "0.5.0.2")
	}
	r.tick()
	r.want("2 0.5.0.3")
	// restart while node 2 updates: a new service waits for it, as the old one did
	r.svc = r.service("0.5.0.3")
	r.tick()
	r.want("2 0.5.0.3")
	r.nodes.up(2, "0.5.0.3")
	r.svc = r.service("0.5.0.3")
	r.tick()
	r.want("2 0.5.0.3", "3 0.5.0.3")
	// restart after node 3 failed: it is not asked again, and nobody after it is
	r.nodes.report(3, nodeapi.UpdateStatus{State: nodeapi.UpdateFailed, Error: "no"})
	r.svc = r.service("0.5.0.3")
	r.tick()
	r.svc = r.service("0.5.0.3")
	r.tick()
	r.tick()
	r.want("2 0.5.0.3", "3 0.5.0.3")
	if a := r.attempt(3); a.State != Failed {
		t.Fatalf("%+v", a)
	}
	// the panel itself is updated to a newer version: that is another version, tried afresh
	r.nodes.up(3, "0.5.0.2")
	r.svc = r.service("0.5.0.4")
	r.tick()
	r.want("2 0.5.0.3", "3 0.5.0.3", "2 0.5.0.4")
}

// "Update all" rolls the nodes that are behind one at a time even when "follow" is off, and
// forgets the failures of this version before it starts; it ends when nothing is behind.
func TestUpdateAllRollsOutOneAtATime(t *testing.T) {
	r := newRig(t, "0.5.0.3")
	if err := r.svc.SetFollowing(r.ctx, false); err != nil {
		t.Fatal(err)
	}
	for _, id := range []int64{2, 3, 4} {
		r.nodes.up(id, "0.5.0.2")
	}
	r.tick()
	r.want() // follow is off: the panel does not touch the nodes
	if err := r.svc.RequestAll(r.ctx); err != nil {
		t.Fatal(err)
	}
	r.want("2 0.5.0.3")
	r.tick()
	r.want("2 0.5.0.3")
	r.nodes.up(2, "0.5.0.3")
	r.tick()
	r.want("2 0.5.0.3", "3 0.5.0.3")
	r.nodes.up(3, "0.5.0.3")
	r.tick()
	r.want("2 0.5.0.3", "3 0.5.0.3", "4 0.5.0.3")
	r.nodes.up(4, "0.5.0.3")
	r.tick()
	r.tick()
	// the rollout is over: a node that falls behind later is left alone while follow is off
	r.nodes.up(2, "0.5.0.2")
	r.tick()
	r.want("2 0.5.0.3", "3 0.5.0.3", "4 0.5.0.3")
	st, err := r.svc.load(r.ctx, r.set)
	if err != nil || st.Rollout != "" {
		t.Fatalf("the rollout was not ended: %+v, %v", st, err)
	}
}

// Nodes that are removed leave no attempts behind, and the panel's own node is no node to ask.
func TestRemovedNodesAreForgotten(t *testing.T) {
	r := newRig(t, "0.5.0.3")
	r.nodes.up(2, "0.5.0.2")
	r.nodes.up(3, "0.5.0.2")
	r.tick()
	r.want("2 0.5.0.3")
	if err := r.st.Q.DeleteNode(r.ctx, 2); err != nil {
		t.Fatal(err)
	}
	r.tick()
	if _, ok := r.svc.Attempt(r.ctx, 2); ok {
		t.Fatal("the removed node still has an attempt")
	}
	// the rollout goes on with the next node instead of waiting for one that is gone
	r.tick()
	r.want("2 0.5.0.3", "3 0.5.0.3")
}

// A node is a server somebody else may run: what it says about its update is cut short and
// must be a state the panel knows.
func TestReportedStatusIsCleaned(t *testing.T) {
	if Reported(nil) != nil {
		t.Fatal("nothing is nothing")
	}
	if Reported(&nodeapi.UpdateStatus{State: "owned"}) != nil {
		t.Fatal("an unknown state")
	}
	long := make([]byte, 100000)
	for i := range long {
		long[i] = 'x'
	}
	got := Reported(&nodeapi.UpdateStatus{State: nodeapi.UpdateFailed, Error: string(long), Version: string(long)})
	if got == nil || len(got.Error) > nodeapi.MaxUpdateError || len(got.Version) > nodeapi.MaxUpdateField {
		t.Fatalf("%+v", got)
	}
	// and what a node says in its health never fills the database through an attempt
	r := newRig(t, "0.5.0.3")
	r.nodes.up(2, "0.5.0.2")
	r.tick()
	r.nodes.report(2, nodeapi.UpdateStatus{State: nodeapi.UpdateFailed, Error: string(long)})
	r.tick()
	if a := r.attempt(2); len(a.Error) > nodeapi.MaxUpdateError {
		t.Fatalf("an error of %d bytes was kept", len(a.Error))
	}
}
