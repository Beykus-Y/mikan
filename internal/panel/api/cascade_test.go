package api

import (
	"testing"

	"mikan/internal/nodeapi"
	"mikan/internal/panel/nodesync"
)

// The cascade page reads the relay's listener against the port the node runs it on: a
// busy port is named so, and a node that has not caught up with a move says nothing.
func TestRelayListener(t *testing.T) {
	inUse := nodeapi.ListenerStatus{Name: nodeapi.RelayListener, Error: "listen tcp 0.0.0.0:2053: bind: address already in use", Code: nodeapi.ListenerAddrInUse}
	denied := nodeapi.ListenerStatus{Name: nodeapi.RelayListener, Error: "listen tcp 0.0.0.0:2053: bind: permission denied"}
	up := nodeapi.ListenerStatus{Name: nodeapi.RelayListener, OK: true}
	other := nodeapi.ListenerStatus{Name: "vless", OK: true}
	view := func(ok bool, port string, ls ...nodeapi.ListenerStatus) nodesync.HealthView {
		return nodesync.HealthView{OK: ok, Ports: map[string]string{nodeapi.RelayListener: port}, Listeners: ls}
	}
	for _, c := range []struct {
		name      string
		hv        nodesync.HealthView
		port      string
		state     string
		hasReason bool
	}{
		{"up", view(true, "2053", other, up), "2053", relayUp, false},
		{"busy", view(true, "2053", other, inUse), "2053", relayBusy, true},
		{"another failure", view(true, "2053", other, denied), "2053", relayFailed, true},
		{"the node runs the old port", view(true, "2053", other, inUse), "2083", relayUnknown, false},
		{"no applied state", nodesync.HealthView{OK: true, Listeners: []nodeapi.ListenerStatus{inUse}}, "2053", relayUnknown, false},
		{"the node does not answer", view(false, "2053", inUse), "2053", relayUnknown, false},
		{"no relay listener yet", view(true, "2053", other), "2053", relayUnknown, false},
	} {
		state, why := relayListener(c.hv, c.port)
		if state != c.state || (why != "") != c.hasReason {
			t.Errorf("%s: %q %q, want %q (reason %v)", c.name, state, why, c.state, c.hasReason)
		}
	}
}
