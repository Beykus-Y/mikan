package node

import (
	"bytes"
	"encoding/binary"

	"mikan/internal/nodeapi"
)

// The torrent blocker's patterns. Each is a fixed shape BitTorrent itself defines, so
// other traffic does not match by chance: a false catch bans a paying user. Encrypted
// BitTorrent (MSE/PE) is noise to these and goes through.

var (
	btProtocol = []byte("\x13BitTorrent protocol") // BEP 3: the peer wire handshake
	dhtQuery   = []byte("d1:ad2:id20:")            // BEP 5: a query, keys in bencode order
	dhtReply   = []byte("d1:rd2:id20:")            // and a reply
)

// udpTrackerMagic opens a UDP tracker's connect request (BEP 15).
const udpTrackerMagic = 0x41727101980

// torrentTCP says what BitTorrent the first bytes of a TCP stream are, "" when none.
func torrentTCP(b []byte) string {
	if bytes.HasPrefix(b, btProtocol) {
		return nodeapi.TorrentHandshake
	}
	if httpTracker(b) {
		return nodeapi.TorrentTracker
	}
	return ""
}

// httpTracker: an HTTP tracker's announce or scrape (BEP 3, BEP 48), a GET whose query
// carries the torrent's info_hash.
func httpTracker(b []byte) bool {
	if !bytes.HasPrefix(b, []byte("GET /")) {
		return false
	}
	line := b
	if i := bytes.IndexByte(b, '\n'); i >= 0 {
		line = b[:i]
	}
	if !bytes.Contains(line, []byte("info_hash=")) {
		return false
	}
	return bytes.Contains(line, []byte("/announce")) || bytes.Contains(line, []byte("/scrape")) ||
		bytes.Contains(line, []byte("peer_id="))
}

// torrentUDP says what BitTorrent a UDP packet is, "" when none.
func torrentUDP(b []byte) string {
	switch {
	case bytes.HasPrefix(b, dhtQuery), bytes.HasPrefix(b, dhtReply):
		return nodeapi.TorrentDHT
	case len(b) >= 16 && binary.BigEndian.Uint64(b) == udpTrackerMagic && binary.BigEndian.Uint32(b[8:]) == 0:
		return nodeapi.TorrentTracker
	case utpSyn(b):
		return nodeapi.TorrentUTP
	}
	return ""
}

// utpSyn: the packet that opens a uTP connection (BEP 29). Only the SYN is looked at: its
// type and version byte, the zero timestamp difference and an extension chain that ends
// exactly at the end of the packet, which carries no data. A QUIC packet whose first
// byte happens to be the same does not get past the rest.
func utpSyn(b []byte) bool {
	const header = 20
	if len(b) < header || b[0] != 0x41 || binary.BigEndian.Uint32(b[8:]) != 0 {
		return false
	}
	next, pos := b[1], header
	for next != 0 {
		if next > 4 || pos+2 > len(b) {
			return false
		}
		next = b[pos]
		pos += 2 + int(b[pos+1])
	}
	return pos == len(b)
}
