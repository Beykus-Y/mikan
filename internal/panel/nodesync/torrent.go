package nodesync

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"strings"
	"time"

	"mikan/internal/nodeapi"
	"mikan/internal/panel/store/db"
)

// torrentPullEvery is how often a node is asked for its torrent blocker's hits. The node
// keeps a caught user out for a couple of minutes by itself; the ban reaches the other
// nodes within this and a policy push.
const torrentPullEvery = 5 * time.Second

// torrentKeep is how long the hits are kept; a ban still running is kept until it ends.
const torrentKeep = 90 * 24 * time.Hour

// A hit's time is the node's clock: one a little ahead is let be, one further ahead counts
// from the panel's now, so a node with a wrong clock cannot make a ban longer.
const torrentSkew = 2 * time.Minute

// An HTTP tracker line is plain text a web page can make the victim's browser send, so it
// bans only when it repeats: trackerBanCatches of them within trackerWindow. The handshake,
// DHT, uTP and a UDP tracker connect cannot be forged that way and ban at once. The panel
// counts, not the node: it sees the user's slots on every node, and the hits it stores.
const (
	trackerBanCatches = 3
	trackerWindow     = 10 * time.Minute
)

type torrentSource interface {
	Torrents(ctx context.Context, epoch string, after int64) (nodeapi.TorrentHits, error)
}

// pullTorrents stores the node's new torrent hits. A caught user is banned on every node
// from the panel's clock, so a node's wrong clock cannot make a ban longer or shorter.
func (s *Syncer) pullTorrents(ctx context.Context) {
	snap, err := s.m.snapshot(ctx, s.id)
	if err != nil {
		return
	}
	if !snap.torrent.Enabled {
		s.torrentWas(ctx, false)
		return
	}
	src, ok := s.node.(torrentSource)
	if !ok {
		return
	}
	epoch, seq, err := s.torrentPos(ctx)
	if err != nil {
		s.log.Error("torrent position", "err", err)
		return
	}
	got, err := src.Torrents(ctx, epoch, seq)
	if err != nil {
		return // a node older than the blocker, or one that is down: the health shows which
	}
	if !s.torrentWas(ctx, true) {
		// The blocker was off (or this node is new to it) until now: what the node holds is
		// from before, and a catch made then must not become a fresh ban. Take its position.
		s.skipTorrents(ctx, got)
		return
	}
	if got.Epoch != epoch {
		seq = 0
	}
	var hits []nodeapi.TorrentHit
	last := seq
	for _, h := range got.Hits {
		if h.Seq > seq {
			hits = append(hits, h)
			last = max(last, h.Seq)
		}
	}
	if got.Epoch == epoch && last == seq {
		return
	}
	now := s.m.now()
	cfg := snap.torrent
	banned := false
	err = s.m.st.Tx(ctx, func(q *db.Queries) error {
		banned = false // Tx retries the callback: what it captured must start over
		names := make([]string, 0, len(hits))
		for _, h := range hits {
			names = append(names, h.Slot)
		}
		rows, err := q.SlotOwners(ctx, names)
		if err != nil {
			return err
		}
		owner := make(map[string]int64, len(rows))
		for _, r := range rows {
			owner[r.SlotName] = r.UserID
		}
		for _, h := range hits {
			uid, ok := owner[h.Slot]
			// A slot nobody owns any more, or a user let alone since the catch.
			if !ok || cfg.IsExempt(uid) {
				continue
			}
			// The ban counts from the catch, not from this pull: a node that was out of reach
			// for a while must not turn its old hits into fresh bans.
			at := now.Unix()
			if h.At > 0 && h.At <= at+int64(torrentSkew/time.Second) {
				at = h.At
			}
			count := int64(min(max(h.Count, 1), 1<<30))
			var until int64
			if cfg.BanMinutes > 0 {
				until = at + cfg.BanMinutes*60
				if until <= now.Unix() {
					continue // the ban would be over already
				}
				if h.Network == "tcp" && h.Kind == nodeapi.TorrentTracker {
					prior, err := q.TorrentTrackerCatches(ctx, db.TorrentTrackerCatchesParams{UserID: uid, At: at - int64(trackerWindow/time.Second)})
					if err != nil {
						return err
					}
					if prior+count < trackerBanCatches {
						until = 0 // stored, but one line is not enough
					}
				}
				banned = banned || until > 0
			}
			if _, err := q.AddTorrentHit(ctx, db.AddTorrentHitParams{
				UserID: uid, NodeID: sql.NullInt64{Int64: s.id, Valid: true}, Ip: clip(h.IP, 64),
				Inbound: clip(h.Inbound, 64), Network: clip(h.Network, 8), Kind: clip(h.Kind, 16), Dest: clip(h.Dest, 300),
				Hits: int32(count), At: at, BannedUntil: until,
			}); err != nil {
				return err
			}
		}
		if err := q.SetNodeState(ctx, db.SetNodeStateParams{Key: stateKeyOf("torrent_epoch", s.id), Value: got.Epoch}); err != nil {
			return err
		}
		return q.SetNodeState(ctx, db.SetNodeStateParams{Key: stateKeyOf("torrent_seq", s.id), Value: strconv.FormatInt(last, 10)})
	})
	if err != nil {
		s.log.Error("store torrent hits", "err", err)
		return
	}
	if banned {
		s.m.PoliciesChanged()
	}
}

// torrentWas says whether the blocker was on for this node at the last pull, and records
// that it is on or off now. A node reports a hit only while the blocker is on, so what it
// holds after an off period is stale.
func (s *Syncer) torrentWas(ctx context.Context, on bool) bool {
	key := stateKeyOf("torrent_on", s.id)
	was, err := s.m.st.Q.GetNodeState(ctx, key)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		s.log.Error("torrent state", "err", err)
		return true // do not drop hits over a failed read; the age rule still holds
	}
	want := "0"
	if on {
		want = "1"
	}
	if was != want {
		if err := s.m.st.Q.SetNodeState(ctx, db.SetNodeStateParams{Key: key, Value: want}); err != nil {
			s.log.Error("torrent state", "err", err)
			return true
		}
	}
	return was == "1"
}

// skipTorrents moves the cursor past everything the node holds, storing nothing.
func (s *Syncer) skipTorrents(ctx context.Context, got nodeapi.TorrentHits) {
	var last int64
	for _, h := range got.Hits {
		last = max(last, h.Seq)
	}
	err := s.m.st.Tx(ctx, func(q *db.Queries) error {
		if err := q.SetNodeState(ctx, db.SetNodeStateParams{Key: stateKeyOf("torrent_epoch", s.id), Value: got.Epoch}); err != nil {
			return err
		}
		return q.SetNodeState(ctx, db.SetNodeStateParams{Key: stateKeyOf("torrent_seq", s.id), Value: strconv.FormatInt(last, 10)})
	})
	if err != nil {
		s.log.Error("torrent position", "err", err)
	}
}

func (s *Syncer) torrentPos(ctx context.Context) (string, int64, error) {
	epoch, err := s.m.st.Q.GetNodeState(ctx, stateKeyOf("torrent_epoch", s.id))
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", 0, err
	}
	raw, err := s.m.st.Q.GetNodeState(ctx, stateKeyOf("torrent_seq", s.id))
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", 0, err
	}
	seq, _ := strconv.ParseInt(raw, 10, 64)
	return epoch, seq, nil
}

// clip cuts what a node sent to a sane length: a node is a server somebody else may run.
func clip(s string, n int) string {
	if len(s) > n {
		s = s[:n]
	}
	return strings.ToValidUTF8(s, "")
}
