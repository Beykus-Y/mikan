package nodeapi

import "testing"

// A node before 0.5 sends no code: its error text still tells a busy port, in the wording
// of Linux and of Windows. Other failures, and a code the panel does not know, are not one.
func TestListenerBusy(t *testing.T) {
	for _, c := range []struct {
		l    ListenerStatus
		busy bool
	}{
		{ListenerStatus{Name: "a", Error: "x", Code: ListenerAddrInUse}, true},
		{ListenerStatus{Name: "a", Error: "listen tcp :443: bind: address already in use"}, true},
		{ListenerStatus{Name: "a", Error: "listen udp :443: bind: Only one usage of each socket address (protocol/network address/port) is normally permitted."}, true},
		{ListenerStatus{Name: "a", Error: "listen tcp :443: bind: permission denied"}, false},
		{ListenerStatus{Name: "a", Error: "address already in use", Code: "something_else"}, false},
		{ListenerStatus{Name: "a", OK: true, Code: ListenerAddrInUse}, false},
	} {
		if got := c.l.Busy(); got != c.busy {
			t.Errorf("%+v: busy %v, want %v", c.l, got, c.busy)
		}
	}
}
