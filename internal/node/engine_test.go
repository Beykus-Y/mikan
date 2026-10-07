package node

import (
	"encoding/json"
	"net"
	"testing"

	"mikan/internal/nodeapi"
)

func TestConnLinesStayOutOfLogs(t *testing.T) {
	for _, msg := range []string{
		"[TCP] dial DIRECT (match Match/) 203.0.113.9:57314 --> [2001:db8::a]:443 error: connect: network is unreachable",
		"[UDP] dial DIRECT (match Match/) 203.0.113.9:4000 --> example.com:443 error: dns resolve failed",
	} {
		if !connLine(msg) {
			t.Errorf("per-connection line must be dropped: %q", msg)
		}
	}
	for _, msg := range []string{
		"Listener vless-vision listen err: listen tcp :443: bind: address already in use",
		"mikan-sync 00ff",
	} {
		if connLine(msg) {
			t.Errorf("line must be kept: %q", msg)
		}
	}
	if name, reason, ok := parseListenErr("Listener tuic listen err: bind: address already in use"); !ok || name != "tuic" || reason != "bind: address already in use" {
		t.Errorf("parseListenErr = %q %q %v", name, reason, ok)
	}
}

// A port another program holds reaches the panel as a code, whatever the OS calls it: the
// error here is this OS's own, from a second listen on a taken port, over TCP and UDP.
func TestBusyPortIsReportedByCode(t *testing.T) {
	tcp, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer tcp.Close()
	udp, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer udp.Close()
	_, errTCP := net.Listen("tcp", tcp.Addr().String())
	_, errUDP := net.ListenPacket("udp", udp.LocalAddr().String())
	for _, e := range []error{errTCP, errUDP} {
		if e == nil {
			t.Fatal("a second listen on a taken port succeeded")
		}
		name, reason, ok := parseListenErr("Listener vless-xhttp listen err: " + e.Error())
		if !ok {
			t.Fatalf("not a listen error: %v", e)
		}
		ls := listenFailed(name, reason)
		if ls.OK || ls.Code != nodeapi.ListenerAddrInUse || !ls.Busy() || ls.Error != e.Error() {
			t.Fatalf("status of %q: %+v", e, ls)
		}
	}
	for _, msg := range []string{"listen tcp: address 99999: invalid port", "listen tcp :443: bind: permission denied", "tls: no certificate"} {
		if ls := listenFailed("tuic", msg); ls.Code != "" || ls.Busy() {
			t.Errorf("%q is not a busy port: %+v", msg, ls)
		}
	}
	// The code is new: a status without one keeps the JSON older panels read.
	raw, _ := json.Marshal(nodeapi.ListenerStatus{Name: "tuic", OK: true})
	if string(raw) != `{"name":"tuic","ok":true}` {
		t.Fatalf("JSON: %s", raw)
	}
}
