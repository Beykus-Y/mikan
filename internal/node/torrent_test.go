package node

import (
	"bytes"
	"encoding/binary"
	"io"
	"net"
	"net/netip"
	"testing"
	"time"

	C "github.com/metacubex/mihomo/constant"

	"mikan/internal/nodeapi"
)

func btHandshake() []byte {
	b := append([]byte("\x13BitTorrent protocol"), make([]byte, 8)...)
	b = append(b, bytes.Repeat([]byte{0xab}, 20)...) // info hash
	return append(b, []byte("-qB4650-0123456789ab")...)
}

func utpSynPacket(ext ...[]byte) []byte {
	b := make([]byte, 20)
	b[0] = 0x41
	binary.BigEndian.PutUint16(b[2:], 0x1234)    // connection id
	binary.BigEndian.PutUint32(b[4:], 123456789) // timestamp
	binary.BigEndian.PutUint32(b[12:], 1<<20)    // window
	binary.BigEndian.PutUint16(b[16:], 1)        // seq
	for i, e := range ext {
		if i == 0 {
			b[1] = 2
		}
		next := byte(0)
		if i < len(ext)-1 {
			next = 2
		}
		b = append(append(b, next, byte(len(e))), e...)
	}
	return b
}

func TestTorrentTCP(t *testing.T) {
	cases := []struct {
		name string
		in   []byte
		want string
	}{
		{"peer handshake", btHandshake(), nodeapi.TorrentHandshake},
		{"announce", []byte("GET /announce?info_hash=%ab%cd&peer_id=-qB4650-x&port=6881 HTTP/1.1\r\nHost: t.example\r\n\r\n"), nodeapi.TorrentTracker},
		{"scrape", []byte("GET /scrape?info_hash=%ab%cd HTTP/1.1\r\n\r\n"), nodeapi.TorrentTracker},
		{"private tracker path", []byte("GET /a1b2c3/announce?info_hash=%ab HTTP/1.1\r\n"), nodeapi.TorrentTracker},
		{"tls", []byte{0x16, 0x03, 0x01, 0x02, 0x00, 0x01, 0x00, 0x01, 0xfc, 0x03, 0x03}, ""},
		{"plain http", []byte("GET /watch?v=abc HTTP/1.1\r\nHost: example.com\r\n\r\n"), ""},
		{"info_hash only in a header", []byte("GET / HTTP/1.1\r\nX-Note: info_hash=1&peer_id=2\r\n\r\n"), ""},
		{"post", []byte("POST /announce?info_hash=1 HTTP/1.1\r\n"), ""},
		{"handshake cut short", btHandshake()[:10], ""},
		{"nothing", nil, ""},
	}
	for _, c := range cases {
		if got := torrentTCP(c.in); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}

func TestTorrentUDP(t *testing.T) {
	tracker := make([]byte, 16)
	binary.BigEndian.PutUint64(tracker, udpTrackerMagic)
	binary.BigEndian.PutUint32(tracker[12:], 0xdeadbeef) // transaction id
	announce := append([]byte(nil), tracker...)
	binary.BigEndian.PutUint32(announce[8:], 1) // not a connect

	quic := make([]byte, 1200)
	quic[0] = 0x41 // a short header with the same first byte as a uTP SYN
	for i := 1; i < len(quic); i++ {
		quic[i] = byte(i*7 + 3)
	}
	cases := []struct {
		name string
		in   []byte
		want string
	}{
		{"dht query", []byte("d1:ad2:id20:abcdefghij0123456789e1:q4:ping1:t2:aa1:y1:qe"), nodeapi.TorrentDHT},
		{"dht reply", []byte("d1:rd2:id20:abcdefghij0123456789e1:t2:aa1:y1:re"), nodeapi.TorrentDHT},
		{"tracker connect", tracker, nodeapi.TorrentTracker},
		{"tracker, not a connect", announce, ""},
		{"utp syn", utpSynPacket(), nodeapi.TorrentUTP},
		{"utp syn with extensions", utpSynPacket(make([]byte, 8), []byte{1, 2, 3, 4}), nodeapi.TorrentUTP},
		{"utp syn with data after it", append(utpSynPacket(), 1, 2, 3), ""},
		{"quic short header", quic, ""},
		{"quic initial", append([]byte{0xc3, 0, 0, 0, 1}, make([]byte, 1195)...), ""},
		{"dns", []byte{0x12, 0x34, 0x01, 0x00, 0, 1, 0, 0, 0, 0, 0, 0, 7, 'e', 'x', 'a', 'm', 'p', 'l', 'e', 3, 'c', 'o', 'm', 0, 0, 1, 0, 1}, ""},
		{"nothing", nil, ""},
	}
	for _, c := range cases {
		if got := torrentUDP(c.in); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}

func torrentRegistry(now *time.Time, ban int64) *Registry {
	r := NewRegistry("e1", 0, time.Minute, func() time.Time { return *now })
	r.SetSlots([]nodeapi.Slot{{Name: "s1", UUID: "u1"}, {Name: "s2", UUID: "u2"}})
	r.SetPolicies("e1", []nodeapi.Policy{
		{Slot: "s1", Allowed: true, QuotaRemaining: -1},
		{Slot: "s2", Allowed: true, QuotaRemaining: -1, TorrentExempt: true},
	})
	r.SetTorrent(&nodeapi.TorrentBlock{BanSeconds: ban})
	return r
}

// A catch keeps the slot out of this node for a short while and is reported once a
// minute; the ban that lasts is the panel's, through the policy.
func TestTorrentBanAndHits(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	r := torrentRegistry(&now, 3600)
	s1 := r.admit("u1", "vless", "203.0.113.1", false)
	if !r.torrentWatch(s1) || r.torrentWatch(r.admit("u2", "vless", "203.0.113.2", false)) {
		t.Fatal("s1 is watched, s2 is exempt")
	}

	r.caught(s1, "203.0.113.1", "vless", "tcp", nodeapi.TorrentHandshake, "198.51.100.9:6881")
	r.caught(s1, "203.0.113.1", "vless", "udp", nodeapi.TorrentDHT, "198.51.100.9:6881")
	if r.admit("u1", "vless", "203.0.113.1", false) != nil {
		t.Fatal("a caught slot must be kept out")
	}
	hits := r.TorrentHits("", 0)
	if len(hits.Hits) != 1 || hits.Hits[0].Count != 1 || hits.Hits[0].BannedUntil != now.Unix()+3600 || hits.Hits[0].Seq != 1 {
		t.Fatalf("one hit a minute: %+v", hits)
	}

	now = now.Add(torrentLocalBan + time.Second)
	if r.admit("u1", "vless", "203.0.113.1", false) == nil {
		t.Fatal("the node's own ban is short: the panel's holds after it")
	}
	r.SetPolicies("e1", []nodeapi.Policy{{Slot: "s1", Allowed: true, QuotaRemaining: -1, BannedUntil: now.Unix() + 600}})
	if r.admit("u1", "vless", "203.0.113.1", false) != nil {
		t.Fatal("the panel's ban must keep the slot out")
	}
	r.SetPolicies("e1", []nodeapi.Policy{{Slot: "s1", Allowed: true, QuotaRemaining: -1}})
	if r.admit("u1", "vless", "203.0.113.1", false) == nil {
		t.Fatal("a ban the panel lifted must not hold")
	}

	r.caught(s1, "203.0.113.1", "vless", "tcp", nodeapi.TorrentTracker, "198.51.100.9:80")
	got := r.TorrentHits(hits.Epoch, 1)
	if len(got.Hits) != 1 || got.Hits[0].Seq != 2 || got.Hits[0].Count != 2 || got.Hits[0].Kind != nodeapi.TorrentTracker {
		t.Fatalf("the catch held back is counted in the next hit: %+v", got)
	}
	if all := r.TorrentHits("another-epoch", 2); len(all.Hits) != 2 {
		t.Fatalf("a panel that knew another epoch gets every hit: %+v", all)
	}
}

// A ban the admin lifted in the panel also ends the node's own short ban: the slot is let
// back in at once, not after torrentLocalBan.
func TestTorrentLiftClearsLocalBan(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	r := torrentRegistry(&now, 3600)
	s1 := r.admit("u1", "vless", "203.0.113.1", false)
	r.caught(s1, "203.0.113.1", "vless", "tcp", nodeapi.TorrentHandshake, "198.51.100.9:6881")
	r.SetPolicies("e1", []nodeapi.Policy{{Slot: "s1", Allowed: true, QuotaRemaining: -1, BannedUntil: now.Unix() + 3600}})
	if r.admit("u1", "vless", "203.0.113.1", false) != nil {
		t.Fatal("caught and banned by the panel")
	}
	now = now.Add(10 * time.Second)
	r.SetPolicies("e1", []nodeapi.Policy{{Slot: "s1", Allowed: true, QuotaRemaining: -1}})
	if r.admit("u1", "vless", "203.0.113.1", false) == nil {
		t.Fatal("a lifted ban must not leave the node's local ban holding")
	}

	// A policy push before the panel has read the catch must not undo the local ban.
	now = now.Add(time.Minute)
	s1 = r.admit("u1", "vless", "203.0.113.1", false)
	r.caught(s1, "203.0.113.1", "vless", "tcp", nodeapi.TorrentHandshake, "198.51.100.9:6881")
	r.SetPolicies("e1", []nodeapi.Policy{{Slot: "s1", Allowed: true, QuotaRemaining: -1}})
	if r.admit("u1", "vless", "203.0.113.1", false) != nil {
		t.Fatal("no ban was lifted: the local ban holds")
	}
}

// Switched off, the blocker lets the slot in at once, the node's own ban included.
func TestTorrentOffClearsLocalBan(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	r := torrentRegistry(&now, 3600)
	s1 := r.admit("u1", "vless", "203.0.113.1", false)
	r.caught(s1, "203.0.113.1", "vless", "udp", nodeapi.TorrentDHT, "198.51.100.9:6881")
	if r.admit("u1", "vless", "203.0.113.1", false) != nil {
		t.Fatal("caught")
	}
	r.SetTorrent(nil)
	if r.admit("u1", "vless", "203.0.113.1", false) == nil {
		t.Fatal("the blocker is off: the local ban must go")
	}
}

// An HTTP tracker line is plain text a web page can make the victim's browser send, so it
// is dropped and reported but bans nothing on the node; the panel bans on repeats.
func TestTorrentHTTPTrackerNoLocalBan(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	r := torrentRegistry(&now, 3600)
	s1 := r.admit("u1", "vless", "203.0.113.1", false)
	r.caught(s1, "203.0.113.1", "vless", "tcp", nodeapi.TorrentTracker, "198.51.100.9:80")
	if r.admit("u1", "vless", "203.0.113.1", false) == nil {
		t.Fatal("one HTTP tracker line must not keep the slot out")
	}
	h := r.TorrentHits("", 0).Hits
	if len(h) != 1 || h[0].Kind != nodeapi.TorrentTracker || h[0].BannedUntil != 0 {
		t.Fatalf("the line is still reported, without a ban: %+v", h)
	}

	// A UDP tracker connect cannot be forged by a browser: it bans at once.
	now = now.Add(time.Minute)
	r.caught(s1, "203.0.113.1", "vless", "udp", nodeapi.TorrentTracker, "198.51.100.9:6969")
	if r.admit("u1", "vless", "203.0.113.1", false) != nil {
		t.Fatal("a UDP tracker connect bans at once")
	}
}

func TestTorrentWithoutBanOnlyDrops(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	r := torrentRegistry(&now, 0)
	s1 := r.admit("u1", "vless", "203.0.113.1", false)
	r.caught(s1, "203.0.113.1", "vless", "udp", nodeapi.TorrentUTP, "198.51.100.9:6881")
	if r.admit("u1", "vless", "203.0.113.1", false) == nil {
		t.Fatal("without a ban the slot keeps its access")
	}
	if h := r.TorrentHits("", 0).Hits; len(h) != 1 || h[0].BannedUntil != 0 {
		t.Fatalf("the hit is still reported: %+v", h)
	}
	r.SetTorrent(nil)
	if r.torrentWatch(s1) {
		t.Fatal("the blocker is off")
	}
}

type readingTunnel struct {
	C.Tunnel
	got chan []byte
}

func (t *readingTunnel) HandleTCPConn(conn net.Conn, m *C.Metadata) {
	b := make([]byte, 64)
	n, _ := io.ReadAtLeast(conn, b, 1)
	t.got <- b[:n]
}

// The first bytes are looked at and still reach the destination; BitTorrent does not.
func TestTunnelPeeksFirstBytes(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	r := torrentRegistry(&now, 3600)
	inner := &readingTunnel{got: make(chan []byte, 1)}
	tun := &Tunnel{inner: inner, reg: r}
	meta := func() *C.Metadata {
		return &C.Metadata{Type: C.VLESS, InUser: "u1", InName: "vless", SrcIP: netip.MustParseAddr("203.0.113.1"), DstPort: 443, Host: "example.com"}
	}

	client, server := net.Pipe()
	go func() { _, _ = client.Write([]byte("GET / HTTP/1.1\r\n\r\n")) }()
	tun.HandleTCPConn(server, meta())
	if got := <-inner.got; string(got) != "GET / HTTP/1.1\r\n\r\n" {
		t.Fatalf("the peeked bytes must reach the tunnel: %q", got)
	}
	client.Close()

	client, server = net.Pipe()
	go func() { _, _ = client.Write(btHandshake()) }()
	tun.HandleTCPConn(server, meta())
	select {
	case got := <-inner.got:
		t.Fatalf("BitTorrent must not reach the tunnel: %q", got)
	default:
	}
	if h := r.TorrentHits("", 0).Hits; len(h) != 1 || h[0].Dest != "example.com:443" {
		t.Fatalf("the catch is reported: %+v", h)
	}
	client.Close()

	// A server-first protocol: the client says nothing, the connection goes on after the wait.
	now = now.Add(torrentLocalBan + time.Second)
	client, server = net.Pipe()
	done := make(chan struct{})
	go func() {
		tun.HandleTCPConn(server, meta())
		close(done)
	}()
	time.Sleep(torrentPeekWait + 50*time.Millisecond)
	_, _ = client.Write([]byte("EHLO x\r\n"))
	<-done
	if got := <-inner.got; string(got) != "EHLO x\r\n" {
		t.Fatalf("a silent client must still get through: %q", got)
	}
	client.Close()
}
