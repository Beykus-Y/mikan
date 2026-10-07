package nodeapi

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"testing"
)

// endless writes a JSON object that never ends.
func endless(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = io.WriteString(w, `{"epoch":"e","seq":1,"slots":{`)
	chunk := strings.Repeat(`"s000001":{"up":1,"down":1},`, 4096)
	for {
		if _, err := io.WriteString(w, chunk); err != nil {
			return
		}
	}
}

// A node is a server somebody else may run: an answer that never ends must be refused
// at a size, not buffered until the panel runs out of memory.
func TestAnswerSizeIsCapped(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(endless))
	defer srv.Close()
	c := &Client{hc: srv.Client(), base: srv.URL}
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	_, err := c.Counters(context.Background())
	runtime.ReadMemStats(&after)
	if !errors.Is(err, ErrTooLarge) {
		t.Fatalf("an endless answer: %v", err)
	}
	if grew := after.TotalAlloc - before.TotalAlloc; grew > 8*MaxResponse {
		t.Fatalf("the panel allocated %d MiB for one answer", grew>>20)
	}
}

func TestAnswerWithinTheCapIsRead(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"epoch":"e","seq":3,"slots":{"s1":{"up":1,"down":2}},"future_field":true}`)
	}))
	defer srv.Close()
	c := &Client{hc: srv.Client(), base: srv.URL}
	got, err := c.Counters(context.Background())
	if err != nil || got.Seq != 3 || got.Slots["s1"].Down != 2 {
		t.Fatalf("%+v %v", got, err)
	}
}

// A failure without an Error body is a typed *StatusError, so that callers tell a node
// that predates an endpoint (404) without matching text; a coded failure stays an *Error.
func TestFailureIsTyped(t *testing.T) {
	old := httptest.NewServer(http.NotFoundHandler()) // a mux without the route: plain text
	defer old.Close()
	var se *StatusError
	_, err := (&Client{hc: old.Client(), base: old.URL}).SpeedTest(context.Background())
	if !errors.As(err, &se) || se.Status != http.StatusNotFound || se.Path != "/v1/speedtest" || !strings.Contains(err.Error(), "status 404") {
		t.Fatalf("an old node: %v", err)
	}
	busy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusConflict)
		_, _ = io.WriteString(w, `{"code":"speed_test_busy","message":"a speed test is running"}`)
	}))
	defer busy.Close()
	var ne *Error
	var typed *StatusError
	_, err = (&Client{hc: busy.Client(), base: busy.URL}).SpeedTest(context.Background())
	if !errors.As(err, &ne) || ne.Code != "speed_test_busy" || errors.As(err, &typed) {
		t.Fatalf("a busy node: %v", err)
	}
}
