package acme

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
	"time"
)

func TestChallengeListen(t *testing.T) {
	for in, want := range map[string]string{
		"":                ":80",
		"  ":              ":80",
		":80":             ":80",
		"127.0.0.1:18080": "127.0.0.1:18080",
		"localhost:18080": "localhost:18080",
		"[::1]:18080":     "[::1]:18080",
		"0.0.0.0:8080":    "0.0.0.0:8080",
		":080":            ":80",
	} {
		if got, err := ChallengeListen(in); err != nil || got != want {
			t.Errorf("%q: got %q %v, want %q", in, got, err, want)
		}
	}
	for _, in := range []string{"80", "127.0.0.1", ":0", ":65536", ":-1", "127.0.0.1:http", "example.com:80", "127.0.0.1:80/path", "::1:80"} {
		if got, err := ChallengeListen(in); err == nil {
			t.Errorf("%q: accepted as %q", in, got)
		}
	}
}

// The panel reads the address at start; a wrong one is logged and the challenge stays on
// port 80, so certificates keep working where nothing else holds it.
func TestManagerTakesChallengeListenFromEnv(t *testing.T) {
	t.Setenv("MIKAN_ACME_LISTEN", "127.0.0.1:18080")
	if m := New(t.TempDir(), nil, nil, nil, slog.Default(), time.Now); m.challenge != "127.0.0.1:18080" {
		t.Fatalf("challenge on %q", m.challenge)
	}
	var logs bytes.Buffer
	t.Setenv("MIKAN_ACME_LISTEN", "127.0.0.1:99999")
	if m := New(t.TempDir(), nil, nil, nil, slog.New(slog.NewTextHandler(&logs, nil)), time.Now); m.challenge != ":80" {
		t.Fatalf("an invalid value: challenge on %q", m.challenge)
	}
	if !strings.Contains(logs.String(), "MIKAN_ACME_LISTEN") || !strings.Contains(logs.String(), "level=WARN") {
		t.Fatalf("no warning: %s", logs.String())
	}
}
