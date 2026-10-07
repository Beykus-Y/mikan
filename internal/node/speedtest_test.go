package node

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// fakeDNS answers every query but every fourth: 25% loss.
func fakeDNS(t *testing.T) string {
	t.Helper()
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pc.Close() })
	go func() {
		buf := make([]byte, 512)
		for i := 1; ; i++ {
			n, addr, err := pc.ReadFrom(buf)
			if err != nil {
				return
			}
			if i%4 == 0 {
				continue
			}
			_, _ = pc.WriteTo(buf[:n], addr)
		}
	}()
	return pc.LocalAddr().String()
}

// fakeCloudflare serves /down with exactly ?bytes=N and refuses a request without the
// Referer, as Cloudflare does for the big ones; it keeps what it was asked for.
type fakeCloudflare struct {
	*httptest.Server
	asked, maxAsked, uploaded atomic.Int64
	noReferer                 atomic.Int64
}

func newFakeCloudflare(t *testing.T) *fakeCloudflare {
	t.Helper()
	f := &fakeCloudflare{}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Referer") != "https://speed.test/" {
			f.noReferer.Add(1)
			http.Error(w, "no referer", http.StatusForbidden)
			return
		}
		switch r.URL.Path {
		case "/down":
			n, err := strconv.ParseInt(r.URL.Query().Get("bytes"), 10, 64)
			if err != nil || n <= 0 {
				http.Error(w, "no size", http.StatusBadRequest)
				return
			}
			f.asked.Add(n)
			for {
				m := f.maxAsked.Load()
				if n <= m || f.maxAsked.CompareAndSwap(m, n) {
					break
				}
			}
			chunk := make([]byte, 64<<10)
			for n > 0 {
				k := min(n, int64(len(chunk)))
				if _, err := w.Write(chunk[:k]); err != nil {
					return
				}
				n -= k
			}
		case "/up":
			n, _ := io.Copy(io.Discard, r.Body)
			f.uploaded.Add(n)
		}
	}))
	t.Cleanup(f.Close)
	return f
}

func (f *fakeCloudflare) target(t *testing.T) speedTarget {
	return speedTarget{DNS: fakeDNS(t), Down: f.URL + "/down", Up: f.URL + "/up", Referer: "https://speed.test/", Pings: 8,
		Spend: 300 * time.Millisecond, Chunk: 1 << 20, Streams: 2, DownCap: 64 << 20, UpCap: 16 << 20, Client: f.Client()}
}

func TestSpeedTest(t *testing.T) {
	f := newFakeCloudflare(t)
	target := f.target(t)
	now := time.Unix(1_800_000_000, 0)
	res, err := runSpeedTest(context.Background(), target, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	if res.Error != "" || res.DownBps <= 0 || res.UpBps <= 0 || f.uploaded.Load() == 0 {
		t.Fatalf("both ways measured: %+v (uploaded %d)", res, f.uploaded.Load())
	}
	if res.LossPct != 25 || res.PingMs < 0 || res.JitterMs < 0 || !res.At.Equal(now) {
		t.Fatalf("2 of 8 queries lost: %+v", res)
	}
	// Chunks, not one huge request, and every request carries the Referer.
	if f.maxAsked.Load() > target.Chunk || f.noReferer.Load() != 0 {
		t.Fatalf("largest request %d (chunk %d), %d without a Referer", f.maxAsked.Load(), target.Chunk, f.noReferer.Load())
	}

	// One test at a time.
	speedTesting.Lock()
	if _, err := runSpeedTest(context.Background(), target, time.Now); err != ErrSpeedTestBusy {
		t.Fatalf("a second test at once: %v", err)
	}
	speedTesting.Unlock()

	// Nothing answers: the times say so, and the broken step is named.
	dead := speedTarget{DNS: "127.0.0.1:9", Down: "http://127.0.0.1:9/down", Up: "http://127.0.0.1:9/up", Pings: 2, Spend: 100 * time.Millisecond,
		Chunk: 1 << 20, Streams: 2, DownCap: 4 << 20, UpCap: 4 << 20, Client: &http.Client{Timeout: time.Second}}
	res, _ = runSpeedTest(context.Background(), dead, time.Now)
	if res.PingMs != -1 || res.LossPct != 100 || !strings.HasPrefix(res.Error, "download:") {
		t.Fatalf("an unreachable target: %+v", res)
	}
}

// Without the Referer the server refuses and the test says so, with nothing measured.
func TestSpeedTestNeedsReferer(t *testing.T) {
	f := newFakeCloudflare(t)
	target := f.target(t)
	target.Referer = ""
	res, _ := runSpeedTest(context.Background(), target, time.Now)
	if !strings.HasPrefix(res.Error, "download: 403") || res.DownBps != 0 {
		t.Fatalf("a refused download: %+v", res)
	}
}

// The volume is capped, not only the time: with plenty of time left a direction stops at
// its cap, and the download never asks for more than the cap in all.
func TestSpeedTestVolumeCap(t *testing.T) {
	f := newFakeCloudflare(t)
	target := f.target(t)
	target.Spend = 30 * time.Second
	target.Streams = 3
	target.Chunk = 1 << 20
	target.DownCap = 5<<20 + 123 // not a multiple of the chunk: the last request is short
	target.UpCap = 6 << 20       // 4 MB and 2 MB
	began := time.Now()
	res, err := runSpeedTest(context.Background(), target, time.Now)
	if err != nil || res.Error != "" || res.DownBps <= 0 || res.UpBps <= 0 {
		t.Fatalf("a capped test: %+v, %v", res, err)
	}
	if took := time.Since(began); took > 20*time.Second {
		t.Fatalf("the cap did not stop it: %s", took)
	}
	if f.asked.Load() != target.DownCap {
		t.Fatalf("asked for %d bytes down, the cap is %d", f.asked.Load(), target.DownCap)
	}
	if f.uploaded.Load() != target.UpCap {
		t.Fatalf("sent %d bytes up, the cap is %d", f.uploaded.Load(), target.UpCap)
	}
}

// The real limits are what the panel and the UI text promise.
func TestSpeedTestLimitsAreTheDocumentedOnes(t *testing.T) {
	if cloudflare.DownCap != 250<<20 || cloudflare.UpCap != 100<<20 || cloudflare.Chunk > 25<<20 || cloudflare.Referer == "" {
		t.Fatalf("limits: %+v", cloudflare)
	}
}
