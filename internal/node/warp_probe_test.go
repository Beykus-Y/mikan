package node

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"mikan/internal/nodeapi"
)

// pipeConn is a connection that closes at once: the first step of a probe only needs
// the dial to succeed.
func pipeConn() net.Conn {
	a, b := net.Pipe()
	_ = b.Close()
	return a
}

// A probe says what is broken: the tunnel (no TCP connection by IP) or what rides on it
// (names, TLS, Cloudflare's answer), with a stable code and a short detail.
func TestRunProbe(t *testing.T) {
	trace := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, "fl=1\nip=203.0.113.9\nwarp=on\ncolo=AMS\n")
	}))
	t.Cleanup(trace.Close)
	pool := trace.Client().Transport.(*http.Transport).TLSClientConfig
	toServer := func(u string) func(context.Context, string) (net.Conn, error) {
		return func(ctx context.Context, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "tcp", strings.TrimPrefix(u, "https://"))
		}
	}
	base := func(dial func(context.Context, string) (net.Conn, error)) probeTarget {
		return probeTarget{dial: dial, traceURL: trace.URL, tls: pool, reachAddr: "1.1.1.1:443", endpoint: "162.159.192.1:2408",
			dialTimeout: 100 * time.Millisecond, httpTimeout: 300 * time.Millisecond}
	}

	t.Run("works", func(t *testing.T) {
		st := runProbe(context.Background(), base(toServer(trace.URL)))
		if !st.OK || st.IP != "203.0.113.9" || st.Warp != "on" || st.Colo != "AMS" || st.Error != "" || st.Detail != "" {
			t.Fatalf("%+v", st)
		}
	})

	t.Run("no answer over UDP", func(t *testing.T) {
		// A WireGuard handshake nobody answers: the TCP dial hangs until its deadline.
		tg := base(func(ctx context.Context, _ string) (net.Conn, error) { <-ctx.Done(); return nil, ctx.Err() })
		st := runProbe(context.Background(), tg)
		if st.OK || st.Error != "timeout" || !strings.Contains(st.Detail, "162.159.192.1:2408") || !strings.Contains(st.Detail, "UDP") {
			t.Fatalf("%+v", st)
		}
	})

	t.Run("refused", func(t *testing.T) {
		// A closed port on a real socket.
		l, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		addr := l.Addr().String()
		_ = l.Close()
		st := runProbe(context.Background(), base(func(ctx context.Context, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "tcp", addr)
		}))
		if st.OK || st.Error != "refused" || st.Detail == "" {
			t.Fatalf("%+v", st)
		}
	})

	// Past the first step the tunnel is known to carry TCP.
	afterTunnel := func(second func(context.Context, string) (net.Conn, error)) probeTarget {
		var calls atomic.Int32
		return base(func(ctx context.Context, addr string) (net.Conn, error) {
			if calls.Add(1) == 1 {
				return pipeConn(), nil
			}
			return second(ctx, addr)
		})
	}

	t.Run("name does not resolve", func(t *testing.T) {
		st := runProbe(context.Background(), afterTunnel(func(context.Context, string) (net.Conn, error) {
			return nil, &net.DNSError{Err: "no such host", Name: "www.cloudflare.com", IsNotFound: true}
		}))
		if st.OK || st.Error != "dns" {
			t.Fatalf("%+v", st)
		}
	})

	t.Run("HTTPS hangs", func(t *testing.T) {
		st := runProbe(context.Background(), afterTunnel(func(ctx context.Context, _ string) (net.Conn, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		}))
		if st.OK || st.Error != "https_timeout" || !strings.Contains(st.Detail, "tunnel is up") {
			t.Fatalf("%+v", st)
		}
	})

	t.Run("not TLS", func(t *testing.T) {
		plain := httptest.NewServer(http.NotFoundHandler())
		t.Cleanup(plain.Close)
		tg := afterTunnel(func(ctx context.Context, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "tcp", strings.TrimPrefix(plain.URL, "http://"))
		})
		tg.traceURL = "https://" + strings.TrimPrefix(plain.URL, "http://")
		st := runProbe(context.Background(), tg)
		if st.OK || st.Error != "tls" {
			t.Fatalf("%+v", st)
		}
	})

	t.Run("answer without an address", func(t *testing.T) {
		empty := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = fmt.Fprint(w, "fl=1\n") }))
		t.Cleanup(empty.Close)
		tg := base(toServer(empty.URL))
		tg.traceURL = empty.URL
		tg.tls = empty.Client().Transport.(*http.Transport).TLSClientConfig
		st := runProbe(context.Background(), tg)
		if st.OK || st.Error != "bad_answer" || !strings.Contains(st.Detail, "200") {
			t.Fatalf("%+v", st)
		}
	})

	t.Run("anything else", func(t *testing.T) {
		long := strings.Repeat("x", 500)
		st := runProbe(context.Background(), base(func(context.Context, string) (net.Conn, error) { return nil, errors.New(long) }))
		if st.Error != "failed" || len([]rune(st.Detail)) > 201 {
			t.Fatalf("%q", st.Detail)
		}
	})
}

func TestClassifyProbeError(t *testing.T) {
	for name, tc := range map[string]struct {
		err   error
		https bool
		want  string
	}{
		"deadline":         {context.DeadlineExceeded, false, "timeout"},
		"net timeout":      {&net.OpError{Op: "dial", Err: timeoutErr{}}, false, "timeout"},
		"https deadline":   {fmt.Errorf("Get x: %w", context.DeadlineExceeded), true, "https_timeout"},
		"refused syscall":  {&net.OpError{Op: "dial", Err: syscall.ECONNREFUSED}, false, "refused"},
		"dns":              {&net.DNSError{Err: "no such host"}, true, "dns"},
		"mihomo dns":       {errors.New("dns resolve failed: all DNS requests failed"), true, "dns"},
		"tls header":       {tls.RecordHeaderError{Msg: "first record does not look like a TLS handshake"}, true, "tls"},
		"remote tls alert": {errors.New("remote error: tls: handshake failure"), true, "tls"},
		"other":            {errors.New("boom"), false, "failed"},
	} {
		t.Run(name, func(t *testing.T) {
			if code, _ := classifyProbeError(tc.err, tc.https, "1.2.3.4:2408"); code != tc.want {
				t.Fatalf("%s, want %s", code, tc.want)
			}
		})
	}
	if _, d := classifyProbeError(context.DeadlineExceeded, false, ""); strings.Contains(d, "WARP endpoint") {
		t.Fatalf("an outbound that is not WARP: %q", d)
	}
}

type timeoutErr struct{}

func (timeoutErr) Error() string   { return "i/o timeout" }
func (timeoutErr) Timeout() bool   { return true }
func (timeoutErr) Temporary() bool { return true }

func warpEngine(buf *bytes.Buffer, calls *atomic.Int32, answer func() nodeapi.WarpStatus) *Engine {
	e := &Engine{
		log:     slog.New(slog.NewTextHandler(buf, nil)),
		applied: nodeapi.DesiredState{Warp: &nodeapi.Warp{Endpoint: "162.159.192.1:2408"}},
	}
	e.warpCheck = func(_ context.Context, _, _ string) nodeapi.WarpStatus {
		calls.Add(1)
		s := answer()
		s.Configured, s.CheckedAt = true, time.Now().UTC()
		return s
	}
	return e
}

// The check is kept for a minute; force skips that, but not more often than every few
// seconds, so the button cannot hammer Cloudflare.
func TestWarpStatusForce(t *testing.T) {
	var buf bytes.Buffer
	var calls atomic.Int32
	e := warpEngine(&buf, &calls, func() nodeapi.WarpStatus { return nodeapi.WarpStatus{Error: "timeout"} })
	ctx := context.Background()

	if e.WarpStatus(ctx, false); calls.Load() != 1 {
		t.Fatal("first check")
	}
	if e.WarpStatus(ctx, false); calls.Load() != 1 {
		t.Fatal("a plain check must come from the cache")
	}
	if got := e.WarpStatus(ctx, true); calls.Load() != 1 || got.Error != "timeout" {
		t.Fatalf("a forced check right after another must come from the cache: %d", calls.Load())
	}
	e.warp.CheckedAt = time.Now().Add(-10 * time.Second)
	if e.WarpStatus(ctx, false); calls.Load() != 1 {
		t.Fatal("10 s old: still cached for a plain check")
	}
	if e.WarpStatus(ctx, true); calls.Load() != 2 {
		t.Fatal("10 s old: a forced check must look again")
	}
	e.warp.CheckedAt = time.Now().Add(-2 * time.Minute)
	if e.WarpStatus(ctx, false); calls.Load() != 3 {
		t.Fatal("an old check must be renewed")
	}

	var none Engine
	if s := none.WarpStatus(ctx, true); s.Configured {
		t.Fatal("no WARP, no check")
	}
}

// A failure is logged when it starts or its reason changes, and recovery when it ends;
// repeated probes of the same state stay silent.
func TestWarpFailureLoggedOnChange(t *testing.T) {
	var buf bytes.Buffer
	var calls atomic.Int32
	cur := nodeapi.WarpStatus{Error: "timeout", Detail: "no answer from the WARP endpoint 162.159.192.1:2408 over UDP"}
	e := warpEngine(&buf, &calls, func() nodeapi.WarpStatus { return cur })
	ctx := context.Background()
	probeAgain := func() {
		e.warp.CheckedAt = time.Now().Add(-time.Hour)
		e.WarpStatus(ctx, false)
	}
	count := func(s string) int { return strings.Count(buf.String(), s) }

	probeAgain()
	probeAgain()
	probeAgain()
	if count("WARP check failed") != 1 || !strings.Contains(buf.String(), "reason=timeout") || !strings.Contains(buf.String(), "162.159.192.1:2408") {
		t.Fatalf("one line for one failure:\n%s", &buf)
	}
	cur = nodeapi.WarpStatus{Error: "https_timeout", Detail: "slow"}
	probeAgain()
	probeAgain()
	if count("WARP check failed") != 2 || !strings.Contains(buf.String(), "reason=https_timeout") {
		t.Fatalf("a new reason is a new line:\n%s", &buf)
	}
	cur = nodeapi.WarpStatus{OK: true, IP: "203.0.113.9", Colo: "AMS"}
	probeAgain()
	probeAgain()
	if count("WARP works again") != 1 {
		t.Fatalf("recovery once:\n%s", &buf)
	}
	probeAgain()
	if count("\n") != 3 {
		t.Fatalf("nothing for a state that did not change:\n%s", &buf)
	}

	// A WARP that works from the first check says nothing.
	buf.Reset()
	fresh := warpEngine(&buf, &calls, func() nodeapi.WarpStatus { return nodeapi.WarpStatus{OK: true} })
	fresh.WarpStatus(ctx, false)
	if buf.Len() != 0 {
		t.Fatalf("a working WARP is not news:\n%s", &buf)
	}
}
