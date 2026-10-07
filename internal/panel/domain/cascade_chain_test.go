package domain

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"testing"
	"time"

	"mikan/internal/nodeapi"
	"mikan/internal/panel/store/db"
)

// A chain longer than maxChain is refused as too long, not as a loop: the admin who sees
// "loop" looks for a cycle that is not there.
func TestLongChainIsNotACycle(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	st, _, _ := setup(t, &now)
	ctx := context.Background()
	q := st.Q
	var nodes []db.Node
	for i := 0; i < maxChain+2; i++ {
		n, err := q.CreateNode(ctx, db.CreateNodeParams{Name: fmt.Sprintf("N%d", i), Address: fmt.Sprintf("203.0.113.%d:25305", i+10), PublicHost: fmt.Sprintf("203.0.113.%d", i+10),
			CreatedAt: now.Unix(), UpdatedAt: now.Unix()})
		if err != nil {
			t.Fatal(err)
		}
		nodes = append(nodes, n)
	}
	// N0 → N1 → … → N9, each node's relay going on to the next.
	for i, n := range nodes {
		if _, err := EnsureRelay(ctx, q, n, now, nil); err != nil {
			t.Fatal(err)
		}
		if i+1 < len(nodes) {
			if err := q.SetNodeRelayRoute(ctx, db.SetNodeRelayRouteParams{Outbound: "direct", ExitNodeID: sql.NullInt64{Int64: nodes[i+1].ID, Valid: true}, NodeID: n.ID}); err != nil {
				t.Fatal(err)
			}
		}
	}
	// The panel's own node going out through N0 would make a chain of eleven hops.
	err := CheckExit(ctx, q, 1, nodes[0].ID)
	if !errors.Is(err, ErrExitLong) || errors.Is(err, ErrExitCycle) {
		t.Fatalf("a chain of %d hops: %v, want exit_too_long", len(nodes)+1, err)
	}
	// A chain within the limit is fine.
	if err := CheckExit(ctx, q, 1, nodes[len(nodes)-3].ID); err != nil {
		t.Fatalf("a short chain: %v", err)
	}
	// And a real cycle still is one.
	if err := CheckExit(ctx, q, nodes[len(nodes)-1].ID, nodes[len(nodes)-3].ID); !errors.Is(err, ErrExitCycle) {
		t.Fatalf("a cycle: %v", err)
	}
}

// Another program's port is not the relay's: the third-party Xray on 2053 of a real node
// made the first relay listen where it could not, and the source node reached that Xray.
func TestEnsureRelaySkipsPortsTheServerHolds(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	st, _, _ := setup(t, &now)
	ctx := context.Background()
	mk := func(i int) db.Node {
		n, err := st.Q.CreateNode(ctx, db.CreateNodeParams{Name: fmt.Sprintf("N%d", i), Address: fmt.Sprintf("203.0.113.%d:25305", i+10), PublicHost: fmt.Sprintf("203.0.113.%d", i+10),
			CreatedAt: now.Unix(), UpdatedAt: now.Unix()})
		if err != nil {
			t.Fatal(err)
		}
		return n
	}
	// 2053 and 2083 over TCP; 2087 only over UDP, which a TCP relay does not mind.
	r, err := EnsureRelay(ctx, st.Q, mk(0), now, &nodeapi.HostPorts{TCP: []int{2053, 2083}, UDP: []int{2087}})
	if err != nil || r.Port != "2087" {
		t.Fatalf("relay on %q, %v; want 2087", r.Port, err)
	}
	// The whole pool held: a high port, none of the busy ones.
	all := &nodeapi.HostPorts{TCP: slices.Clone(PortPool)}
	slices.Sort(all.TCP)
	r, err = EnsureRelay(ctx, st.Q, mk(1), now, all)
	if p, _ := strconv.Atoi(r.Port); err != nil || p < 30000 {
		t.Fatalf("relay on %q, %v; want a high port", r.Port, err)
	}
	// An exit chosen for an inbound makes its relay the same way, from what the lookup knows.
	x := mk(2)
	if err := UseExit(ctx, st.Q, mk(3).ID, x.ID, now, func(node int64) *nodeapi.HostPorts {
		if node != x.ID {
			t.Errorf("asked about node %d, not the exit's", node)
		}
		return &nodeapi.HostPorts{TCP: []int{2053}}
	}); err != nil {
		t.Fatal(err)
	}
	if r, err := st.Q.GetNodeRelay(ctx, x.ID); err != nil || r.Port != "2083" {
		t.Fatalf("the exit's relay: %+v, %v; want 2083", r, err)
	}
}

// The relay moves off a port another program holds, and only from the port it is on.
func TestMoveRelay(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	st, _, _ := setup(t, &now)
	ctx := context.Background()
	n, err := st.Q.CreateNode(ctx, db.CreateNodeParams{Name: "B", Address: "198.51.100.20:40000", PublicHost: "198.51.100.20", CreatedAt: now.Unix(), UpdatedAt: now.Unix()})
	if err != nil {
		t.Fatal(err)
	}
	if err := MoveRelay(ctx, st, n, "2053", "2083"); !errors.Is(err, ErrRelayChanged) {
		t.Fatalf("a node without a relay: %v", err)
	}
	if _, err := EnsureRelay(ctx, st.Q, n, now, nil); err != nil {
		t.Fatal(err)
	}
	port := func() string {
		r, err := st.Q.GetNodeRelay(ctx, n.ID)
		if err != nil {
			t.Fatal(err)
		}
		return r.Port
	}
	if p := port(); p != "2053" {
		t.Fatalf("relay on %s", p)
	}
	if err := MoveRelay(ctx, st, n, "2087", "2096"); !errors.Is(err, ErrRelayChanged) || port() != "2053" {
		t.Fatalf("a move for a port the relay is not on: %v, now %s", err, port())
	}
	var busy *PortInUseError
	if err := MoveRelay(ctx, st, n, "2053", "40000"); !errors.As(err, &busy) || busy.Kind != PortNodeAPI || port() != "2053" {
		t.Fatalf("a move onto the node API's port: %v, now %s", err, port())
	}
	if err := MoveRelay(ctx, st, n, "2053", "2083"); err != nil || port() != "2083" {
		t.Fatalf("move: %v, now %s", err, port())
	}
	if err := MoveRelay(ctx, st, n, "2053", "2096"); !errors.Is(err, ErrRelayChanged) || port() != "2083" {
		t.Fatalf("the same move twice: %v, now %s", err, port())
	}
}
