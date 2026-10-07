package api

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"mikan/internal/nodeapi"
	"mikan/internal/panel/auth"
	"mikan/internal/panel/domain"
	"mikan/internal/panel/store/db"
	"mikan/internal/panel/store/storetest"
)

type speedNodes struct {
	NodeRuntime
	res   nodeapi.SpeedTest
	err   error
	calls int
	// hook runs inside the call, with the context the call got.
	hook func(ctx context.Context)
	// ctxErr is that context's error when the call returned.
	ctxErr error
	// warp and warpErr answer Warp; warpForce notes the force flag of each call.
	warp      nodeapi.WarpStatus
	warpErr   error
	warpForce []bool
}

func (n *speedNodes) Warp(_ context.Context, _ int64, force bool) (nodeapi.WarpStatus, error) {
	n.warpForce = append(n.warpForce, force)
	return n.warp, n.warpErr
}

func (n *speedNodes) SpeedTest(ctx context.Context, _ int64) (nodeapi.SpeedTest, error) {
	n.calls++
	if n.hook != nil {
		n.hook(ctx)
	}
	n.ctxErr = ctx.Err()
	return n.res, n.err
}

type speedFixture struct {
	t     *testing.T
	st    *db.Queries
	nodes *speedNodes
	now   time.Time
	call  func(method, path string) (int, string)
	// callCtx is call with the request's own context, and the response headers.
	callCtx func(ctx context.Context, method, path string) (int, string, http.Header)
}

func newSpeedFixture(t *testing.T) *speedFixture {
	t.Helper()
	ctx := context.Background()
	st, err := storetest.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	f := &speedFixture{t: t, st: st.Q, now: time.Unix(1_800_000_000, 0)}
	clock := func() time.Time { return f.now }
	if err := domain.Seed(ctx, st, f.now); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Q.CreateAdmin(ctx, db.CreateAdminParams{Username: "admin", PasswordHash: "x", CreatedAt: f.now.Unix()}); err != nil {
		t.Fatal(err)
	}
	sessions := auth.NewSessions(st.Q, clock, nil)
	token, sess, err := sessions.Create(ctx, 1, "127.0.0.1", "test")
	if err != nil {
		t.Fatal(err)
	}
	f.nodes = &speedNodes{res: nodeapi.SpeedTest{At: f.now, PingMs: 12.5, JitterMs: 1.2, LossPct: 5, DownBps: 500e6, UpBps: 200e6}}
	pool := domain.NewPool(st, clock)
	handler, _, err := New(Deps{
		Version: "test", Store: st, Sessions: sessions, Now: clock, Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		IPLimit: auth.NewLimiter(10, time.Minute, time.Minute, time.Hour), UserLimit: auth.NewLimiter(10, time.Minute, time.Minute, time.Hour), TOTP: auth.NewTOTPGuard(),
		Users: domain.NewUsers(st, pool, noChanges{}, clock), Pool: pool, Changes: noChanges{}, Nodes: f.nodes,
	})
	if err != nil {
		t.Fatal(err)
	}
	f.callCtx = func(ctx context.Context, method, path string) (int, string, http.Header) {
		t.Helper()
		req := httptest.NewRequest(method, path, nil).WithContext(ctx)
		req.AddCookie(&http.Cookie{Name: auth.CookieName, Value: token})
		req.Header.Set("X-CSRF-Token", sess.CsrfToken)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec.Code, rec.Body.String(), rec.Header()
	}
	f.call = func(method, path string) (int, string) {
		code, body, _ := f.callCtx(context.Background(), method, path)
		return code, body
	}
	return f
}

// speedAudits counts the audit entries of started tests of node 1.
func (f *speedFixture) speedAudits() int {
	f.t.Helper()
	rows, err := f.st.ListAudit(context.Background(), db.ListAuditParams{BeforeID: 1 << 62, Lim: 100})
	if err != nil {
		f.t.Fatal(err)
	}
	n := 0
	for _, r := range rows {
		if r.Action == "node.speedtest" && r.TargetType.String == "node" && r.TargetID.String == "1" {
			n++
		}
	}
	return n
}

const speedRun = "/api/v1/nodes/1/speedtest"

// A test goes into the node's history, newest first, with what a node made up kept within
// sense; a busy or old node gets its own code.
func TestNodeSpeedTests(t *testing.T) {
	f := newSpeedFixture(t)
	if code, body := f.call(http.MethodPost, speedRun); code != http.StatusOK || !strings.Contains(body, `"down_bps":500000000`) {
		t.Fatalf("run: %d %s", code, body)
	}
	f.now = f.now.Add(speedTestCooldown)
	f.nodes.res = nodeapi.SpeedTest{At: time.Unix(1, 0), PingMs: math.NaN(), LossPct: 250, DownBps: -5, UpBps: 9e18, Error: "upload: " + strings.Repeat("x", 1000)}
	if code, body := f.call(http.MethodPost, speedRun); code != http.StatusOK {
		t.Fatalf("run: %d %s", code, body)
	}
	code, body := f.call(http.MethodGet, "/api/v1/nodes/1/speedtests")
	var hist []SpeedTestView
	if code != http.StatusOK || json.Unmarshal([]byte(body), &hist) != nil || len(hist) != 2 {
		t.Fatalf("history: %d %s", code, body)
	}
	if h := hist[0]; h.PingMs != -1 || h.LossPct != 100 || h.DownBps != 0 || h.UpBps != 1e12 || len(h.Error) != 300 || !h.At.Equal(f.now) {
		t.Fatalf("what a node made up, kept within sense, newest first: %+v", h)
	}
	if hist[1].PingMs != 12.5 {
		t.Fatalf("the first test: %+v", hist[1])
	}

	// A node that is busy or too old ran nothing, so it does not cost the cooldown: the
	// answer is the same again at once, not a 429. The old node is told by its type.
	f.now = f.now.Add(speedTestCooldown)
	f.nodes.err = &nodeapi.Error{Code: "speed_test_busy"}
	for range 2 {
		if code, body := f.call(http.MethodPost, speedRun); code != http.StatusConflict || !strings.Contains(body, "speed_test_busy") {
			t.Fatalf("busy: %d %s", code, body)
		}
	}
	f.nodes.err = &nodeapi.StatusError{Method: http.MethodPost, Path: "/v1/speedtest", Status: http.StatusNotFound}
	for range 2 {
		if code, body := f.call(http.MethodPost, speedRun); code != http.StatusConflict || !strings.Contains(body, "node_too_old") {
			t.Fatalf("an old node: %d %s", code, body)
		}
	}
	// Another failing status is no proof of an old node.
	f.nodes.err = &nodeapi.StatusError{Method: http.MethodPost, Path: "/v1/speedtest", Status: http.StatusBadGateway}
	if code, body := f.call(http.MethodPost, speedRun); code != http.StatusBadGateway || !strings.Contains(body, "node_unavailable") {
		t.Fatalf("a failing node: %d %s", code, body)
	}
	if code, _ := f.call(http.MethodGet, "/api/v1/nodes/999/speedtests"); code != http.StatusNotFound {
		t.Fatalf("an unknown node: %d", code)
	}
}

// One test per node per five minutes, each started test in the audit log; a refused
// request does not reach the node and is not logged.
func TestNodeSpeedTestCooldownAndAudit(t *testing.T) {
	f := newSpeedFixture(t)
	if code, body := f.call(http.MethodPost, speedRun); code != http.StatusOK {
		t.Fatalf("first: %d %s", code, body)
	}
	if f.speedAudits() != 1 {
		t.Fatalf("a started test is audited: %d", f.speedAudits())
	}
	f.now = f.now.Add(speedTestCooldown - time.Second)
	code, body, hdr := f.callCtx(context.Background(), http.MethodPost, speedRun)
	if code != http.StatusTooManyRequests || !strings.Contains(body, "speed_test_cooldown") || hdr.Get("Retry-After") != "1" {
		t.Fatalf("too soon: %d %s retry-after %q", code, body, hdr.Get("Retry-After"))
	}
	if f.nodes.calls != 1 || f.speedAudits() != 1 {
		t.Fatalf("a refused request reached the node (%d calls) or the log (%d)", f.nodes.calls, f.speedAudits())
	}
	f.now = f.now.Add(time.Second)
	if code, body := f.call(http.MethodPost, speedRun); code != http.StatusOK {
		t.Fatalf("after the cooldown: %d %s", code, body)
	}
	if f.nodes.calls != 2 || f.speedAudits() != 2 {
		t.Fatalf("calls %d, audit entries %d", f.nodes.calls, f.speedAudits())
	}
	// A test that broke on the way cost its traffic: the cooldown stays.
	f.now = f.now.Add(speedTestCooldown)
	f.nodes.err = io.ErrUnexpectedEOF
	if code, _ := f.call(http.MethodPost, speedRun); code != http.StatusBadGateway {
		t.Fatalf("broken: %d", code)
	}
	if code, _ := f.call(http.MethodPost, speedRun); code != http.StatusTooManyRequests {
		t.Fatalf("after a broken test: %d", code)
	}
}

// The cooldown is per node.
func TestSpeedCooldownPerNode(t *testing.T) {
	var c speedCooldown
	now := time.Unix(1_800_000_000, 0)
	if c.claim(1, now) != 0 || c.claim(2, now) != 0 {
		t.Fatal("the first test of each node")
	}
	if w := c.claim(1, now.Add(time.Minute)); w != speedTestCooldown-time.Minute {
		t.Fatalf("a minute in: %s left", w)
	}
	c.release(1)
	if c.claim(1, now.Add(time.Minute)) != 0 {
		t.Fatal("released")
	}
}

// The admin closes the page during the test: the node still runs it to the end and the
// result is saved, not lost with the request.
func TestNodeSpeedTestSavedAfterRequestCancelled(t *testing.T) {
	f := newSpeedFixture(t)
	reqCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f.nodes.hook = func(context.Context) { cancel() } // the request is gone while the node tests
	f.callCtx(reqCtx, http.MethodPost, speedRun)
	if reqCtx.Err() == nil {
		t.Fatal("the request should have been cancelled")
	}
	if f.nodes.ctxErr != nil {
		t.Fatalf("the node's test was cancelled with the request: %v", f.nodes.ctxErr)
	}
	rows, err := f.st.ListNodeSpeedTests(context.Background(), db.ListNodeSpeedTestsParams{NodeID: 1, Limit: 10})
	if err != nil || len(rows) != 1 || rows[0].DownBps != 500e6 {
		t.Fatalf("saved: %+v, %v", rows, err)
	}
}
