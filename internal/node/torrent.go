package node

import (
	"crypto/rand"
	"encoding/hex"
	"sync"
	"time"

	"mikan/internal/nodeapi"
)

const (
	// torrentReportEvery: a slot is reported at most this often; the catches in between
	// go into the Count of its next hit.
	torrentReportEvery = time.Minute
	// torrentKeep is how many hits the node holds for the panel to read.
	torrentKeep = 512
	// torrentLocalBan is how long the node keeps a caught slot out by itself. The ban
	// itself is the panel's: it reads the hit within seconds and sends the slot's
	// BannedUntil to every node, and lifting it there must not leave this node holding on.
	torrentLocalBan = 2 * time.Minute
	// torrentPeekWait is how long a TCP connection may wait for its first bytes. Clients
	// speak first in nearly every protocol, so it is only waited out by the few where the
	// server does (SMTP, FTP).
	torrentPeekWait = 300 * time.Millisecond
)

// torrents is the torrent blocker's part of the registry: its settings and the hits not
// yet pushed out of the ring.
type torrents struct {
	mu     sync.Mutex
	epoch  string
	seq    int64
	hits   []nodeapi.TorrentHit
	bySlot map[string]*torrentSlot
}

type torrentSlot struct {
	reported time.Time // when the slot was last reported
	skipped  int       // catches since then that were not
}

func newTorrents() torrents {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return torrents{epoch: hex.EncodeToString(b), bySlot: map[string]*torrentSlot{}}
}

// SetTorrent turns the blocker on with these settings, or off with nil.
func (r *Registry) SetTorrent(cfg *nodeapi.TorrentBlock) {
	if cfg != nil {
		c := *cfg
		cfg = &c
	}
	r.torrentCfg.Store(cfg)
	if cfg == nil {
		// Off: nothing is held out any more, the node's own short bans included.
		r.mu.RLock()
		for _, s := range r.byName {
			s.localBan.Store(0)
		}
		r.mu.RUnlock()
	}
}

// torrentWatch says whether the slot's traffic is to be looked at.
func (r *Registry) torrentWatch(s *slot) bool {
	return r.torrentCfg.Load() != nil && !s.torrentExempt.Load()
}

// banned says whether the torrent blocker keeps the slot out at now.
func (s *slot) banned(now time.Time) bool {
	t := now.Unix()
	return t < s.policyBan.Load() || t < s.localBan.Load()
}

// caught records BitTorrent the slot was caught with. With a ban set the slot is kept out
// and its open connections are cut. A listener's shared key has nobody behind it to ban
// or report: its connection is only dropped.
func (r *Registry) caught(s *slot, ip, inName, network, kind, dest string) {
	cfg := r.torrentCfg.Load()
	if cfg == nil || s.shared {
		return
	}
	now := r.now()
	hit := nodeapi.TorrentHit{Slot: s.name, IP: ip, Inbound: inName, Network: network, Kind: kind, Dest: dest,
		At: now.Unix(), Count: 1}
	// An HTTP tracker line is plain text: a web page can make the victim's browser send one,
	// so it is dropped and reported but never bans by itself. The panel bans on repeats.
	if cfg.BanSeconds > 0 && !(network == "tcp" && kind == nodeapi.TorrentTracker) {
		hit.BannedUntil = now.Unix() + cfg.BanSeconds
		s.localBan.Store(now.Add(min(torrentLocalBan, time.Duration(cfg.BanSeconds)*time.Second)).Unix())
		s.mu.Lock()
		conns := s.connsLocked()
		s.mu.Unlock()
		// Called on the connection's own way in: close asynchronously.
		go closeAll(conns)
	}
	t := &r.torrent
	t.mu.Lock()
	defer t.mu.Unlock()
	ts := t.bySlot[s.name]
	if ts == nil {
		ts = &torrentSlot{}
		t.bySlot[s.name] = ts
	}
	if now.Sub(ts.reported) < torrentReportEvery {
		ts.skipped++
		return
	}
	hit.Count += ts.skipped
	ts.reported, ts.skipped = now, 0
	t.seq++
	hit.Seq = t.seq
	t.hits = append(t.hits, hit)
	if len(t.hits) > torrentKeep {
		t.hits = append(t.hits[:0], t.hits[len(t.hits)-torrentKeep:]...)
	}
	// Slots quiet for longer than the report window have nothing left to add up.
	if len(t.bySlot) > 4*torrentKeep {
		for name, v := range t.bySlot {
			if now.Sub(v.reported) >= torrentReportEvery {
				delete(t.bySlot, name)
			}
		}
	}
}

// TorrentHits returns the hits after seq of epoch. When epoch is not the node's (it
// restarted since, and the sequence started over) every hit it holds is returned.
func (r *Registry) TorrentHits(epoch string, after int64) nodeapi.TorrentHits {
	t := &r.torrent
	t.mu.Lock()
	defer t.mu.Unlock()
	if epoch != t.epoch {
		after = 0
	}
	out := nodeapi.TorrentHits{Epoch: t.epoch, Hits: []nodeapi.TorrentHit{}}
	for _, h := range t.hits {
		if h.Seq > after {
			out.Hits = append(out.Hits, h)
		}
	}
	return out
}
