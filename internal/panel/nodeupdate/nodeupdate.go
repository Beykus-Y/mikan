// Package nodeupdate updates the panel's remote nodes. A node is asked to update to the
// panel's own version (never another one: the panel and its nodes move together), by the
// admin's button or by the panel itself when "nodes follow the panel" is on. The node hands
// the request to the updater on its own server, which updates to a signed release or
// refuses (installer/src/update.rs); the panel only asks, and watches the node's health for
// the new version or for the updater's report of a failure.
//
// What the panel has asked is kept in the database (the setting KeyState), so a restart of
// the panel loses neither an update it waits for nor the failure it must not repeat:
//
//   - nodes follow one at a time: the next is asked only when the last has the new version;
//   - a node whose update failed is not asked again for the same version by the panel, and
//     the rollout stops there (an alert is sent); the admin's button still works, and so
//     does switching "follow" off and on, or "update all", which forget the failures;
//   - the panel's own node is never asked: it updates with the panel.
package nodeupdate

import (
	"context"
	"errors"
	"log/slog"
	"sort"
	"strconv"
	"sync"
	"time"

	"mikan/internal/nodeapi"
	"mikan/internal/panel/nodesync"
	"mikan/internal/panel/settings"
	"mikan/internal/panel/store"
	"mikan/internal/panel/store/db"
	"mikan/internal/release"
)

// KeyState is the setting that keeps the attempts and the rollout.
const KeyState = "node_updates"

// MinNodeVersion is the first node release with the update endpoint. Older nodes are
// updated once by hand (`mikan update` on their server).
const MinNodeVersion = "0.5.0.2"

// Timeout is how long a node may take to come back on the new version, hops and a rollback
// included.
const Timeout = 20 * time.Minute

// The states of an attempt.
const (
	Running = nodeapi.UpdateRunning
	OK      = nodeapi.UpdateOK
	Failed  = nodeapi.UpdateFailed
)

// Why the panel refuses to ask a node; the API turns each into its code.
var (
	ErrLocal       = errors.New("local_node")
	ErrNotRelease  = errors.New("panel_not_release")
	ErrOffline     = errors.New("node_offline")
	ErrNotBehind   = errors.New("node_not_behind")
	ErrUnsupported = errors.New("node_cannot_update")
	ErrBusy        = errors.New("update_running")
)

// Attempt is the panel's last request to one node.
type Attempt struct {
	// Version is the release the node was asked for.
	Version string `json:"version"`
	// From is the version the node ran then.
	From string `json:"from,omitempty"`
	// At is when it was asked, Unix time.
	At    int64  `json:"at"`
	State string `json:"state" enum:"running,ok,failed"`
	Error string `json:"error,omitempty"`
}

type state struct {
	// Nodes holds the last attempt per node id.
	Nodes map[string]Attempt `json:"nodes"`
	// Rollout is the version the admin pressed "update all" for: nodes are updated one at
	// a time to it even with "follow" off.
	Rollout string `json:"rollout,omitempty"`
}

// Runtime is what the service needs from the running nodes.
type Runtime interface {
	Health(id int64) (nodesync.HealthView, bool)
	RequestUpdate(ctx context.Context, id int64, version string) error
}

type Service struct {
	st      *store.Store
	set     *settings.Settings
	rt      Runtime
	version string
	log     *slog.Logger
	now     func() time.Time

	// mu keeps the changes of the state one at a time in this process; the transaction
	// keeps them whole against the CLI and a second panel process.
	mu sync.Mutex
}

// New makes the service of a panel of the given version.
func New(st *store.Store, set *settings.Settings, rt Runtime, version string, log *slog.Logger, now func() time.Time) *Service {
	return &Service{st: st, set: set, rt: rt, version: version, log: log, now: now}
}

// Behind says whether a node of version node trails a panel of version panel. A panel
// that runs no release (a development build) has no node to be ahead of.
func Behind(panel, node string) bool {
	return release.Valid(panel) && release.Newer(panel, node)
}

// CanUpdate says whether a node of this version has the update endpoint.
func CanUpdate(node string) bool { return release.AtLeast(node, MinNodeVersion) }

// Reported is the update status a node's health carries, as far as the panel believes it:
// a node is a server somebody else may run, so the state must be a known one and the
// words are cut short. nil when the node reports none.
func Reported(u *nodeapi.UpdateStatus) *nodeapi.UpdateStatus {
	if u == nil {
		return nil
	}
	c, ok := u.Clean()
	if !ok {
		return nil
	}
	return &c
}

// Local says whether a node row is the panel's own node.
func Local(n db.Node) bool { return n.Address == "" }

func (s *Service) load(ctx context.Context, set *settings.Settings) (state, error) {
	st, _, err := settings.Get[state](ctx, set, KeyState)
	if st.Nodes == nil {
		st.Nodes = map[string]Attempt{}
	}
	return st, err
}

// change reads the state, lets fn change it and saves it when it did, in one transaction.
// fn runs again when the transaction is retried, so it only works on the state it is given.
func (s *Service) change(ctx context.Context, fn func(st *state)) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.st.Tx(ctx, func(q *db.Queries) error {
		set := settings.New(q)
		st, err := s.load(ctx, set)
		if err != nil {
			return err
		}
		before := st.clone()
		fn(&st)
		if st.equal(before) {
			return nil
		}
		return settings.Set(ctx, set, KeyState, st)
	})
}

func (st state) clone() state {
	c := state{Nodes: make(map[string]Attempt, len(st.Nodes)), Rollout: st.Rollout}
	for k, v := range st.Nodes {
		c.Nodes[k] = v
	}
	return c
}

func (st state) equal(o state) bool {
	if st.Rollout != o.Rollout || len(st.Nodes) != len(o.Nodes) {
		return false
	}
	for k, v := range st.Nodes {
		if o.Nodes[k] != v {
			return false
		}
	}
	return true
}

func key(id int64) string { return strconv.FormatInt(id, 10) }

// Attempt returns the panel's last request to a node.
func (s *Service) Attempt(ctx context.Context, id int64) (Attempt, bool) {
	st, err := s.load(ctx, s.set)
	if err != nil {
		return Attempt{}, false
	}
	a, ok := st.Nodes[key(id)]
	return a, ok
}

// Following says whether the panel updates its nodes on its own.
func (s *Service) Following(ctx context.Context) (bool, error) {
	return s.set.On(ctx, settings.NodesFollow)
}

// SetFollowing switches "nodes follow the panel". Turning it on forgets the failures: the
// admin asks for another try.
func (s *Service) SetFollowing(ctx context.Context, on bool) error {
	was, err := s.Following(ctx)
	if err != nil {
		return err
	}
	if err := settings.Set(ctx, s.set, settings.KeyNodesFollow, on); err != nil {
		return err
	}
	if on && !was {
		return s.forgetFailures(ctx)
	}
	return nil
}

func (s *Service) forgetFailures(ctx context.Context) error {
	return s.change(ctx, func(st *state) {
		for k, a := range st.Nodes {
			if a.State == Failed {
				delete(st.Nodes, k)
			}
		}
	})
}

// fresh says whether a running attempt may still finish.
func (s *Service) fresh(a Attempt) bool {
	return a.State == Running && s.now().Sub(time.Unix(a.At, 0)) <= Timeout
}

// Request asks a remote node to update to the panel's version. It refuses what is not
// worth asking: the panel's own node, a node that does not answer, one that is not behind
// or cannot update itself, a panel that runs no release, a node already being updated.
func (s *Service) Request(ctx context.Context, n db.Node) error {
	if Local(n) {
		return ErrLocal
	}
	if !release.Valid(s.version) {
		return ErrNotRelease
	}
	hv, ok := s.rt.Health(n.ID)
	if !ok || !hv.OK {
		return ErrOffline
	}
	if !Behind(s.version, hv.Health.Version) {
		return ErrNotBehind
	}
	if !CanUpdate(hv.Health.Version) {
		return ErrUnsupported
	}
	// An attempt that has failed, as the node's own report says, is no reason to wait for
	// the next look of the worker.
	if a, ok := s.Attempt(ctx, n.ID); ok && s.fresh(a) {
		if _, done := s.outcome(n.ID, a); !done {
			return ErrBusy
		}
	}
	return s.ask(ctx, n.ID, hv.Health.Version)
}

// ask records the attempt before the node hears of it, so that a panel that stops in
// between still knows. A node that cannot be reached leaves no attempt behind.
func (s *Service) ask(ctx context.Context, id int64, from string) error {
	mine := Attempt{Version: s.version, From: from, At: s.now().Unix(), State: Running}
	if err := s.change(ctx, func(st *state) { st.Nodes[key(id)] = mine }); err != nil {
		return err
	}
	if err := s.rt.RequestUpdate(ctx, id, s.version); err != nil {
		if cerr := s.change(ctx, func(st *state) {
			if st.Nodes[key(id)] == mine {
				delete(st.Nodes, key(id))
			}
		}); cerr != nil {
			s.log.Warn("node update: cannot forget a request that was not delivered", "node", id, "err", cerr)
		}
		return err
	}
	s.log.Info("node update: asked", "node", id, "from", from, "version", s.version)
	return nil
}

// RequestAll starts the rollout to the panel's version over all the nodes that are behind,
// one at a time, whatever "follow" says; failures of this version are forgotten first.
func (s *Service) RequestAll(ctx context.Context) error {
	if !release.Valid(s.version) {
		return ErrNotRelease
	}
	err := s.change(ctx, func(st *state) {
		for k, a := range st.Nodes {
			if a.State == Failed && a.Version == s.version {
				delete(st.Nodes, k)
			}
		}
		st.Rollout = s.version
	})
	if err != nil {
		return err
	}
	return s.Tick(ctx)
}

// Failure is an update that did not end well, for the alerts.
type Failure struct {
	NodeID  int64
	Version string
	From    string
	At      int64
	Error   string
}

// Failures lists the failed attempts; each has its own At, by which a reader tells the
// ones it has told about already.
func (s *Service) Failures(ctx context.Context) []Failure {
	st, err := s.load(ctx, s.set)
	if err != nil {
		return nil
	}
	var out []Failure
	for k, a := range st.Nodes {
		id, err := strconv.ParseInt(k, 10, 64)
		if err == nil && a.State == Failed {
			out = append(out, Failure{NodeID: id, Version: a.Version, From: a.From, At: a.At, Error: a.Error})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].NodeID < out[j].NodeID })
	return out
}

// Run looks at the nodes every so often, the first time a little after the start so that
// the nodes' health is in.
func (s *Service) Run(ctx context.Context) {
	t := time.NewTimer(30 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := s.Tick(ctx); err != nil && ctx.Err() == nil {
				s.log.Warn("node update", "err", err)
			}
			t.Reset(15 * time.Second)
		}
	}
}

// Tick settles the attempts that are running and, when nodes follow the panel (or the
// admin asked for all), asks the next node that is behind.
func (s *Service) Tick(ctx context.Context) error {
	nodes, err := s.st.Q.ListNodes(ctx)
	if err != nil {
		return err
	}
	remote := map[int64]db.Node{}
	for _, n := range nodes {
		if !Local(n) {
			remote[n.ID] = n
		}
	}
	if err := s.settle(ctx, remote); err != nil {
		return err
	}
	st, err := s.load(ctx, s.set)
	if err != nil {
		return err
	}
	follow, err := s.Following(ctx)
	if err != nil {
		return err
	}
	if !release.Valid(s.version) || !(follow || st.Rollout == s.version) {
		return nil
	}
	ids := make([]int64, 0, len(remote))
	for id := range remote {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	var next int64
	for _, id := range ids {
		a, tried := st.Nodes[key(id)]
		hv, ok := s.rt.Health(id)
		healthy := ok && hv.OK
		switch {
		case tried && s.fresh(a):
			return nil // one at a time
		case tried && a.State == Failed && a.Version == s.version && !(healthy && release.AtLeast(hv.Health.Version, a.Version)):
			return nil // the rollout stops at a failure, and the node is not asked again
		case next == 0 && healthy && Behind(s.version, hv.Health.Version) && CanUpdate(hv.Health.Version):
			next = id
		}
	}
	if next == 0 {
		if st.Rollout != "" {
			return s.change(ctx, func(st *state) { st.Rollout = "" })
		}
		return nil
	}
	hv, _ := s.rt.Health(next)
	return s.ask(ctx, next, hv.Health.Version)
}

// outcome says how a running attempt stands now: the node runs the version it was asked for
// (or a later one), or the updater on its server says it failed, or the time is up. done
// is false while it may still finish.
func (s *Service) outcome(id int64, a Attempt) (_ Attempt, done bool) {
	hv, ok := s.rt.Health(id)
	up := Reported(hv.Health.Update)
	switch {
	case ok && hv.OK && release.AtLeast(hv.Health.Version, a.Version):
		a.State, a.Error = OK, ""
	case ok && hv.OK && up != nil && up.State == nodeapi.UpdateFailed:
		a.State, a.Error = Failed, up.Error
		if a.Error == "" {
			a.Error = "the update failed"
		}
	case s.now().Sub(time.Unix(a.At, 0)) > Timeout:
		a.State, a.Error = Failed, "the node did not run "+a.Version+" in "+Timeout.String()
	default:
		return a, false
	}
	return a, true
}

// settle ends the attempts that are running: the node runs the version it was asked for
// (or a later one), or the updater on its server says it failed, or the time is up. It also
// forgets the nodes that are gone.
func (s *Service) settle(ctx context.Context, remote map[int64]db.Node) error {
	var ended []struct {
		id int64
		a  Attempt
	}
	err := s.change(ctx, func(st *state) {
		ended = ended[:0] // the transaction may run again
		for k, a := range st.Nodes {
			id, err := strconv.ParseInt(k, 10, 64)
			if _, ok := remote[id]; err != nil || !ok {
				delete(st.Nodes, k)
				continue
			}
			if a.State != Running {
				continue
			}
			a, done := s.outcome(id, a)
			if !done {
				continue
			}
			ended = append(ended, struct {
				id int64
				a  Attempt
			}{id, a})
			st.Nodes[k] = a
		}
	})
	if err != nil {
		return err
	}
	for _, e := range ended {
		if e.a.State == Failed {
			s.log.Warn("node update: failed", "node", e.id, "version", e.a.Version, "err", e.a.Error)
		} else {
			s.log.Info("node update: done", "node", e.id, "version", e.a.Version)
		}
	}
	return nil
}
