package api

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"mikan/internal/nodeapi"
	"mikan/internal/panel/store/db"
)

func saveWarp(t *testing.T, f *speedFixture, source, reserved string) {
	t.Helper()
	err := f.st.SaveNodeWarp(context.Background(), db.SaveNodeWarpParams{NodeID: 1, Source: source, PrivateKey: "priv", PeerPublicKey: "pub",
		Endpoint: "162.159.192.1:2408", Ipv4: "172.16.0.2", Reserved: reserved, Mtu: 1280, Routes: "[]", CreatedAt: 1, UpdatedAt: 1})
	if err != nil {
		t.Fatal(err)
	}
}

// Why WARP does not work reaches the drawer: the node's code and detail, "the node did not
// answer" instead of silence, and force only when the admin's button asks for it.
func TestWarpStatusShowsWhy(t *testing.T) {
	f := newSpeedFixture(t)
	saveWarp(t, f, "register", base64.StdEncoding.EncodeToString([]byte{1, 2, 3}))
	const path = "/api/v1/nodes/1/warp"
	get := func(p string) WarpView {
		t.Helper()
		code, body := f.call(http.MethodGet, p)
		var v WarpView
		if code != http.StatusOK || json.Unmarshal([]byte(body), &v) != nil {
			t.Fatalf("%s: %d %s", p, code, body)
		}
		if strings.Contains(body, "priv") {
			t.Fatalf("the private key leaked: %s", body)
		}
		return v
	}

	f.nodes.warp = nodeapi.WarpStatus{Configured: true, Error: "timeout", Detail: "no answer from the WARP endpoint 162.159.192.1:2408 over UDP", CheckedAt: f.now}
	v := get(path)
	if v.Status == nil || v.Status.OK || v.Status.Error != "timeout" || !strings.Contains(v.Status.Detail, "over UDP") || v.NoReserved {
		t.Fatalf("status: %+v", v.Status)
	}
	if got := f.nodes.warpForce; len(got) != 1 || got[0] {
		t.Fatalf("a plain read must not force: %v", got)
	}
	get(path + "?force=true")
	if got := f.nodes.warpForce; len(got) != 2 || !got[1] {
		t.Fatalf("the check button forces: %v", got)
	}

	// A node that does not answer is a status of its own, with the reason; it is logged.
	f.nodes.warpErr = errors.New("dial tcp 203.0.113.5:9443: i/o timeout")
	v = get(path)
	if v.Status == nil || v.Status.OK || v.Status.Error != "node_unreachable" || !strings.Contains(v.Status.Detail, "i/o timeout") || v.Status.CheckedAt.IsZero() {
		t.Fatalf("node down: %+v", v.Status)
	}

	// A node with nothing configured yet has no status; WARP off is not checked at all.
	f.nodes.warpErr, f.nodes.warp = nil, nodeapi.WarpStatus{}
	if v = get(path); v.Status != nil {
		t.Fatalf("not configured on the node: %+v", v.Status)
	}
	calls := len(f.nodes.warpForce)
	if err := f.st.SetNodeWarpOptions(context.Background(), db.SetNodeWarpOptionsParams{Enabled: 0, Routes: "[]", UpdatedAt: 2, NodeID: 1}); err != nil {
		t.Fatal(err)
	}
	if v = get(path); v.Status != nil || len(f.nodes.warpForce) != calls {
		t.Fatalf("WARP off: %+v", v.Status)
	}
}

// An imported account without Reserved is a known cause of silent handshake drops: the
// drawer is told, for imports only.
func TestWarpNoReservedHint(t *testing.T) {
	for name, tc := range map[string]struct {
		source, reserved string
		want             bool
	}{
		"import without":  {"import", "", true},
		"import with":     {"import", base64.StdEncoding.EncodeToString([]byte{1, 2, 3}), false},
		"registered":      {"register", base64.StdEncoding.EncodeToString([]byte{1, 2, 3}), false},
		"register, empty": {"register", "", false},
	} {
		t.Run(name, func(t *testing.T) {
			f := newSpeedFixture(t)
			saveWarp(t, f, tc.source, tc.reserved)
			code, body := f.call(http.MethodGet, "/api/v1/nodes/1/warp")
			var v WarpView
			if code != http.StatusOK || json.Unmarshal([]byte(body), &v) != nil || v.NoReserved != tc.want {
				t.Fatalf("%d %s", code, body)
			}
		})
	}
}
