package node

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"mikan/internal/nodeapi"
)

func updateEngine(t *testing.T) (*Engine, http.Handler, string) {
	t.Helper()
	dir := t.TempDir()
	e := &Engine{dataDir: dir, log: quiet(), Reg: NewRegistry("e1", 0, time.Minute, time.Now), sys: newSysSampler()}
	return e, Handler(e, quiet()), dir
}

func post(h http.Handler, path, body string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, path, strings.NewReader(body)))
	return rec
}

func health(t *testing.T, h http.Handler) nodeapi.Health {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/health", nil))
	var out nodeapi.Health
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &out) != nil {
		t.Fatalf("health: %d %s", rec.Code, rec.Body)
	}
	return out
}

// The panel's request becomes update/request in the node's data directory, whole or not
// at all: the file the host's path unit fires on is never half written.
func TestUpdateRequestIsWrittenForTheHost(t *testing.T) {
	_, h, dir := updateEngine(t)
	rec := post(h, "/v1/update", `{"version":"0.5.0.2"}`)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("request: %d %s", rec.Code, rec.Body)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "update", "request"))
	if err != nil || string(raw) != `{"version":"0.5.0.2"}` {
		t.Fatalf("update/request: %q, %v", raw, err)
	}
	// a second request replaces the first, and no temporary file stays
	if rec := post(h, "/v1/update", `{"version":"0.5.1.0-rc.1"}`); rec.Code != http.StatusAccepted {
		t.Fatalf("second request: %d %s", rec.Code, rec.Body)
	}
	if raw, _ := os.ReadFile(filepath.Join(dir, "update", "request")); string(raw) != `{"version":"0.5.1.0-rc.1"}` {
		t.Fatalf("update/request: %q", raw)
	}
	entries, _ := os.ReadDir(filepath.Join(dir, "update"))
	if len(entries) != 1 {
		t.Fatalf("a temporary file is left behind: %v", entries)
	}
}

// Only a release version goes to the host's disk: nothing a caller sends beside it, and no
// image, address or path in its place.
func TestUpdateRequestValidatesTheVersion(t *testing.T) {
	_, h, dir := updateEngine(t)
	for name, body := range map[string]string{
		"empty":         ``,
		"not json":      `0.5.0.2`,
		"no version":    `{}`,
		"number":        `{"version":5}`,
		"dev":           `{"version":"dev"}`,
		"a tag":         `{"version":"v0.5.0.2"}`,
		"latest":        `{"version":"latest"}`,
		"spaces":        `{"version":" 0.5.0.2"}`,
		"newline":       `{"version":"0.5.0.2\n"}`,
		"three numbers": `{"version":"0.5"}`,
		"five numbers":  `{"version":"0.5.0.2.1"}`,
		"path":          `{"version":"../../etc/passwd"}`,
		"image":         `{"version":"ghcr.io/x/mikan@sha256:aa"}`,
		"long":          `{"version":"0.5.0.2-` + strings.Repeat("x", 100) + `"}`,
	} {
		rec := post(h, "/v1/update", body)
		if rec.Code != http.StatusBadRequest && rec.Code != http.StatusUnprocessableEntity {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body)
		}
		if _, err := os.Stat(filepath.Join(dir, "update", "request")); err == nil {
			t.Fatalf("%s: a request was written", name)
		}
	}
	// a body past a few kilobytes is not read at all
	big := `{"version":"0.5.0.2","pad":"` + strings.Repeat("x", 8192) + `"}`
	if rec := post(h, "/v1/update", big); rec.Code != http.StatusBadRequest {
		t.Errorf("oversized body: %d", rec.Code)
	}
	if _, err := os.Stat(filepath.Join(dir, "update", "request")); err == nil {
		t.Fatal("an oversized body wrote a request")
	}
	// extra fields beside a good version are ignored, and only the version is kept
	if rec := post(h, "/v1/update", `{"version":"0.5.0.2","image":"evil"}`); rec.Code != http.StatusAccepted {
		t.Fatalf("extra field: %d %s", rec.Code, rec.Body)
	}
	if raw, _ := os.ReadFile(filepath.Join(dir, "update", "request")); string(raw) != `{"version":"0.5.0.2"}` {
		t.Fatalf("update/request: %q", raw)
	}
}

// The status of an earlier update goes with a new request, so the panel does not take its
// ending for the new one's.
func TestUpdateRequestClearsTheOldStatus(t *testing.T) {
	e, h, dir := updateEngine(t)
	if err := os.MkdirAll(filepath.Join(dir, "update"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "update", "status.json"), []byte(`{"state":"failed","version":"0.5.0.2","from":"0.5.0.1","error":"x","at":"2026-10-04T10:00:00Z"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if e.UpdateStatus() == nil {
		t.Fatal("the status is not read")
	}
	if rec := post(h, "/v1/update", `{"version":"0.5.0.2"}`); rec.Code != http.StatusAccepted {
		t.Fatalf("request: %d", rec.Code)
	}
	if s := health(t, h).Update; s != nil {
		t.Fatalf("the old status is still there: %+v", s)
	}
}

// The updater's report reaches the panel in the node's health; a file that is missing,
// damaged, too large or not a file is no report, and what is too long is cut.
func TestHealthCarriesTheUpdatersStatus(t *testing.T) {
	_, h, dir := updateEngine(t)
	if s := health(t, h).Update; s != nil {
		t.Fatalf("no file: %+v", s)
	}
	raw, _ := json.Marshal(health(t, h))
	if bytes.Contains(raw, []byte(`"update"`)) {
		t.Fatalf("a node with nothing to report sends no field, as before: %s", raw)
	}
	status := filepath.Join(dir, "update", "status.json")
	if err := os.MkdirAll(filepath.Dir(status), 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(body string) {
		t.Helper()
		_ = os.Remove(status)
		if err := os.WriteFile(status, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(`{"state":"running","version":"0.5.0.2","from":"0.5.0.1","error":"","at":"2026-10-04T10:00:00Z"}`)
	got := health(t, h).Update
	if got == nil || got.State != "running" || got.Version != "0.5.0.2" || got.From != "0.5.0.1" || got.At != "2026-10-04T10:00:00Z" {
		t.Fatalf("running: %+v", got)
	}
	write(`{"state":"failed","version":"0.5.0.2","from":"0.5.0.1","error":"0.5.0.2 did not start: going back","at":"x"}`)
	if got := health(t, h).Update; got == nil || got.State != "failed" || got.Error != "0.5.0.2 did not start: going back" {
		t.Fatalf("failed: %+v", got)
	}
	// What a damaged or hostile file may hold.
	for name, body := range map[string]string{
		"garbage":         "not json at all",
		"empty":           "",
		"a list":          `[]`,
		"no state":        `{"version":"0.5.0.2"}`,
		"unknown state":   `{"state":"great","version":"0.5.0.2"}`,
		"state of a kind": `{"state":["ok"]}`,
		"oversized":       `{"state":"ok","error":"` + strings.Repeat("x", 20<<10) + `"}`,
	} {
		write(body)
		if got := health(t, h).Update; got != nil {
			t.Errorf("%s: %+v", name, got)
		}
	}
	// a long error is cut, not refused
	write(`{"state":"failed","version":"` + strings.Repeat("9", 500) + `","error":"` + strings.Repeat("é", 2000) + `"}`)
	got = health(t, h).Update
	if got == nil || len(got.Error) > nodeapi.MaxUpdateError || len(got.Version) > nodeapi.MaxUpdateField {
		t.Fatalf("long fields: %+v", got)
	}
	if !strings.HasPrefix(got.Error, "é") || strings.ContainsRune(got.Error, '�') {
		t.Fatalf("a character was cut in two: %q", got.Error)
	}
	// a link and a directory are not files; neither is followed
	secret := filepath.Join(dir, "secret.json")
	if err := os.WriteFile(secret, []byte(`{"state":"ok","version":"9.9.9"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	_ = os.Remove(status)
	if err := os.Symlink(secret, status); err == nil {
		if got := health(t, h).Update; got != nil {
			t.Errorf("a link was followed: %+v", got)
		}
		_ = os.Remove(status)
	}
	if err := os.Mkdir(status, 0o755); err != nil {
		t.Fatal(err)
	}
	if got := health(t, h).Update; got != nil {
		t.Errorf("a directory: %+v", got)
	}
}

// The health of an older node has no update field, and a panel before this one ignores it.
func TestHealthUpdateFieldIsOptional(t *testing.T) {
	var old nodeapi.Health
	if err := json.Unmarshal([]byte(`{"version":"0.5.0.1","core":"mihomo","revision":3,"listeners":[],"conns":0,"system":{}}`), &old); err != nil || old.Update != nil {
		t.Fatalf("%+v, %v", old, err)
	}
}
