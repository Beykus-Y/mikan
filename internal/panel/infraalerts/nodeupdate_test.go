package infraalerts

import (
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	"mikan/internal/panel/nodeupdate"
	"mikan/internal/panel/settings"
	"mikan/internal/panel/store/db"
)

type failedUpdates struct{ list []nodeupdate.Failure }

func (f *failedUpdates) Failures(context.Context) []nodeupdate.Failure { return f.list }

// A node whose update failed is told once, in the admin's language, with the reason and the
// versions; the same failure is not told again, a new one is, and a removed node is not.
func TestNodeUpdateFailuresAreToldOnce(t *testing.T) {
	st, ctx := testMonitorStore(t)
	m := New(st, settings.New(st.Q), nil, nil, nil, nil, nil, slog.Default(), time.Now)
	src := &failedUpdates{list: []nodeupdate.Failure{{NodeID: 2, Version: "0.5.0.3", From: "0.5.0.2", At: 100, Error: "mikan 0.5.0.3 did not start: <going back>"}}}
	m.WatchNodeUpdates(src)
	nodes := map[int64]db.Node{2: {ID: 2, Name: "NL & co"}, 3: {ID: 3, Name: "DE"}}
	var state persistentState
	cfg := Default()

	m.checkNodeUpdates(ctx, &state, cfg, nodes, "en")
	if len(state.Pending) != 1 {
		t.Fatalf("pending: %+v", state.Pending)
	}
	text := state.Pending[0].Text
	for _, want := range []string{"Node update failed", "NL &amp; co", "&lt;going back&gt;", "0.5.0.2", "0.5.0.3", "stopped"} {
		if !strings.Contains(text, want) {
			t.Errorf("no %q in %q", want, text)
		}
	}
	if strings.Contains(text, "—") {
		t.Errorf("an em dash in %q", text)
	}
	if state.Pending[0].Target != "admin" {
		t.Errorf("target %q", state.Pending[0].Target)
	}
	// the same failure again and again: nothing new
	for range 3 {
		m.checkNodeUpdates(ctx, &state, cfg, nodes, "en")
	}
	if len(state.Pending) != 1 {
		t.Fatalf("told again: %+v", state.Pending)
	}
	// another try that failed too is another failure
	src.list[0].At = 200
	m.checkNodeUpdates(ctx, &state, cfg, nodes, "ru")
	if len(state.Pending) != 2 || !strings.Contains(state.Pending[1].Text, "Ошибка обновления ноды") || strings.Contains(state.Pending[1].Text, "—") {
		t.Fatalf("pending: %+v", state.Pending)
	}
	// a node that is gone, and one without the event switched on, tell nothing
	src.list = []nodeupdate.Failure{{NodeID: 9, Version: "0.5.0.3", At: 300}}
	m.checkNodeUpdates(ctx, &state, cfg, nodes, "en")
	cfg.Events.Update = false
	src.list = []nodeupdate.Failure{{NodeID: 3, Version: "0.5.0.3", At: 400}}
	m.checkNodeUpdates(ctx, &state, cfg, nodes, "en")
	if len(state.Pending) != 2 {
		t.Fatalf("pending: %+v", state.Pending)
	}
	// the failure is gone (the admin tried again): it is forgotten, and a later one is told
	cfg.Events.Update = true
	src.list = nil
	m.checkNodeUpdates(ctx, &state, cfg, nodes, "en")
	if len(state.NodeUpdateAt) != 0 {
		t.Fatalf("remembered: %+v", state.NodeUpdateAt)
	}
	src.list = []nodeupdate.Failure{{NodeID: 3, Version: "0.5.0.3", At: 500, Error: "timeout"}}
	m.checkNodeUpdates(ctx, &state, cfg, nodes, "en")
	if len(state.Pending) != 3 || !strings.Contains(state.Pending[2].Text, "DE") {
		t.Fatalf("pending: %+v", state.Pending)
	}
	// without a source the monitor does nothing
	var none Monitor
	none.checkNodeUpdates(ctx, &state, cfg, nodes, "en")
}
