package infraalerts

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"log/slog"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"mikan/internal/nodeapi"
	"mikan/internal/panel/acme"
	"mikan/internal/panel/autotune"
	"mikan/internal/panel/nodesync"
	"mikan/internal/panel/nodeupdate"
	"mikan/internal/panel/settings"
	"mikan/internal/panel/store"
	"mikan/internal/panel/store/db"
	"mikan/internal/panel/tgbot"
	"mikan/internal/panel/updates"
)

const stateKey = "monitor"

type Runtime interface {
	Health(id int64) (nodesync.HealthView, bool)
	Warp(ctx context.Context, id int64, force bool) (nodeapi.WarpStatus, error)
	Probe(ctx context.Context, id int64, proxy string) (nodeapi.ProbeResult, error)
}

type Tuner interface {
	Status(inboundID int64) (autotune.Status, bool)
}

type CertificateSource func() acme.Status
type UpdateSource interface {
	Host() (updates.HostStatus, bool)
}

// NodeUpdateSource knows the updates of the remote nodes that failed (nodeupdate.Service).
type NodeUpdateSource interface {
	Failures(ctx context.Context) []nodeupdate.Failure
}

type sampleState struct {
	Tracker
	Checked time.Time `json:"checked,omitempty"`
}

type delivery struct {
	Key           string `json:"key"`
	Target        string `json:"target"`
	Chat          int64  `json:"chat,omitempty"`
	Text          string `json:"text"`
	CreatedAt     int64  `json:"created_at,omitempty"`
	Attempts      int    `json:"attempts,omitempty"`
	NextAttemptAt int64  `json:"next_attempt_at,omitempty"`
}

type persistentState struct {
	Samples            map[string]sampleState `json:"samples"`
	Stuck              map[string]string      `json:"stuck"`
	AutoCursor         int64                  `json:"auto_cursor"`
	TorrentCursor      int64                  `json:"torrent_cursor,omitempty"`
	TLSBad             bool                   `json:"tls_bad"`
	UpdateAt           string                 `json:"update_at"`
	NodeUpdateAt       map[string]int64       `json:"node_update_at,omitempty"`
	PublicTarget       string                 `json:"public_target"`
	PublicMessage      int64                  `json:"public_message"`
	PublicText         string                 `json:"public_text"`
	PublicLevels       map[int64]Level        `json:"public_levels"`
	PublicPinned       bool                   `json:"public_pinned"`
	PublicPinAttempted bool                   `json:"public_pin_attempted"`
	Pending            []delivery             `json:"pending"`
}

type Monitor struct {
	store       *store.Store
	settings    *settings.Settings
	runtime     Runtime
	tuner       Tuner
	cert        CertificateSource
	updates     UpdateSource
	nodeUpdates NodeUpdateSource
	bot         *tgbot.Bot
	log         *slog.Logger
	now         func() time.Time
	roundMu     sync.Mutex
	dispatchMu  sync.Mutex
	dispatching bool
}

func New(st *store.Store, set *settings.Settings, runtime Runtime, tuner Tuner, cert CertificateSource, updates UpdateSource,
	bot *tgbot.Bot, log *slog.Logger, now func() time.Time) *Monitor {
	return &Monitor{store: st, settings: set, runtime: runtime, tuner: tuner, cert: cert, updates: updates, bot: bot, log: log, now: now}
}

// WatchNodeUpdates makes the monitor tell the admin about the updates of nodes that failed
// (the "update" event); call it before Run.
func (m *Monitor) WatchNodeUpdates(src NodeUpdateSource) { m.nodeUpdates = src }

func (m *Monitor) Run(ctx context.Context) {
	// Node health arrives every five seconds. Outbound checks are cached by the node for a
	// minute, and observe ignores an unchanged CheckedAt so this cadence adds no extra probes.
	t := time.NewTicker(5 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			m.round(ctx)
		}
	}
}

func (m *Monitor) load(ctx context.Context) (persistentState, error) {
	var st persistentState
	raw, err := m.store.Q.GetInfrastructureAlertState(ctx, stateKey)
	if errors.Is(err, sql.ErrNoRows) {
		st.AutoCursor, err = m.store.Q.MaxInboundEventID(ctx)
		if err == nil {
			st.TorrentCursor, err = m.store.Q.LastTorrentHitID(ctx)
		}
	} else if err == nil {
		err = json.Unmarshal([]byte(raw), &st)
	}
	if st.Samples == nil {
		st.Samples = map[string]sampleState{}
	}
	if st.Stuck == nil {
		st.Stuck = map[string]string{}
	}
	return st, err
}

func (m *Monitor) save(ctx context.Context, st persistentState) error {
	raw, err := json.Marshal(st)
	if err != nil {
		return err
	}
	old, err := m.store.Q.GetInfrastructureAlertState(ctx, stateKey)
	if err == nil {
		unchanged, err := sameStateIgnoringSampleChecks(old, st)
		if err != nil {
			return err
		}
		if unchanged {
			return nil
		}
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	return m.store.Q.SetInfrastructureAlertState(ctx, db.SetInfrastructureAlertStateParams{Key: stateKey, Value: string(raw), UpdatedAt: m.now().Unix()})
}

func sameStateIgnoringSampleChecks(oldRaw string, next persistentState) (bool, error) {
	var previous persistentState
	if err := json.Unmarshal([]byte(oldRaw), &previous); err != nil {
		return false, err
	}
	normalize := func(state persistentState) persistentState {
		if state.Samples != nil {
			samples := make(map[string]sampleState, len(state.Samples))
			for key, sample := range state.Samples {
				sample.Checked = time.Time{}
				samples[key] = sample
			}
			state.Samples = samples
		}
		return state
	}
	previousRaw, err := json.Marshal(normalize(previous))
	if err != nil {
		return false, err
	}
	nextRaw, err := json.Marshal(normalize(next))
	if err != nil {
		return false, err
	}
	return string(previousRaw) == string(nextRaw), nil
}

func (m *Monitor) config(ctx context.Context) AlertsConfig {
	c, _, err := settings.GetOver(ctx, m.settings, KeyConfig, Default())
	if err != nil {
		m.logError("infrastructure alerts: settings", err)
		return AlertsConfig{}
	}
	return c
}

// inboundFailAfter is how many failed samples make an inbound or the cascade relay
// unavailable. A port held by another program is moved by the tuner within seconds: such a
// listener is reported only when the move did not help.
func inboundFailAfter(busy, autoPort bool) int {
	if busy && autoPort {
		return 24
	}
	return 3
}

func (m *Monitor) logError(message string, err error) {
	if m.log != nil {
		m.log.Error(message, "err", err)
	}
}

func (m *Monitor) round(ctx context.Context) {
	m.roundMu.Lock()
	dispatch := false
	defer func() {
		m.roundMu.Unlock()
		if dispatch {
			m.startDelivery(ctx)
		}
	}()
	st, err := m.load(ctx)
	if err != nil {
		m.logError("infrastructure alerts: load state", err)
		return
	}
	cfg := m.config(ctx)
	lang := "ru"
	if m.bot != nil {
		lang = m.bot.Config(ctx).Lang
	}
	nodes, err := m.store.Q.ListNodes(ctx)
	if err != nil {
		m.logError("infrastructure alerts: list nodes", err)
		return
	}
	inbounds, err := m.store.Q.ListInbounds(ctx)
	if err != nil {
		m.logError("infrastructure alerts: list inbounds", err)
		return
	}
	byNode := map[int64][]db.Inbound{}
	for _, in := range inbounds {
		byNode[in.NodeID] = append(byNode[in.NodeID], in)
	}
	levels := make(map[int64]Level, len(nodes))
	listenerHealth := make(map[int64]map[string]bool, len(nodes))
	// Whether the tuner moves a busy relay by itself: there is no switch for one relay.
	relayMoves := false
	if m.settings != nil {
		on, err := m.settings.On(ctx, settings.AutoPort)
		if err != nil {
			m.logError("infrastructure alerts: settings", err)
		}
		relayMoves = on && err == nil
	}
	for _, n := range nodes {
		if n.Enabled == 0 {
			continue
		}
		hv, ok := nodesync.HealthView{}, false
		if m.runtime != nil {
			hv, ok = m.runtime.Health(n.ID)
		}
		if !ok || hv.CheckedAt.IsZero() {
			continue
		}
		nodeLevel := Healthy
		if !hv.OK {
			nodeLevel = Unavailable
		}
		m.observe(&st, "node/"+strconv.FormatInt(n.ID, 10), nodeLevel, hv.CheckedAt, 3, 2, eventFor(cfg, "node", n.Name, nodeLevel, lang))
		if nodeLevel == Unavailable {
			levels[n.ID] = Unavailable
			continue
		}
		listenerHealth[n.ID] = make(map[string]bool, len(hv.Listeners))
		busy := map[string]bool{}
		for _, l := range hv.Listeners {
			listenerHealth[n.ID][l.Name] = l.OK
			busy[l.Name] = l.Busy()
		}
		for _, in := range byNode[n.ID] {
			if in.Enabled == 0 {
				continue
			}
			ok := listenerHealth[n.ID][in.Name]
			level := Healthy
			if !ok {
				level = Unavailable
			}
			key := "inbound/" + strconv.FormatInt(in.ID, 10)
			m.observe(&st, key, level, hv.CheckedAt, inboundFailAfter(busy[in.Name], in.AutoPort != 0), 2, eventFor(cfg, "inbound", n.Name+" / "+in.Name, level, lang))
			if level != Healthy {
				nodeLevel = Degraded
			}
		}
		if m.checkRelay(&st, cfg, n, hv, relayMoves, lang) != Healthy {
			nodeLevel = Degraded
		}
		levels[n.ID] = nodeLevel
	}

	// The API calls are cached by each node for one minute. Count only a new probe result,
	// not every panel round that reads the same cached response.
	if m.runtime != nil {
		m.probeExits(ctx, &st, cfg, nodes, byNode, levels, lang)
	}
	if m.runtime != nil {
		m.probeWarp(ctx, &st, cfg, nodes, byNode, levels, lang)
	}
	nodeByID := make(map[int64]db.Node, len(nodes))
	for _, n := range nodes {
		nodeByID[n.ID] = n
	}
	inboundByID := make(map[int64]db.Inbound, len(inbounds))
	for _, in := range inbounds {
		inboundByID[in.ID] = in
	}
	m.autotuneEvents(ctx, &st, cfg, nodeByID, inboundByID, lang)
	m.torrentEvents(ctx, &st, cfg, lang)
	m.checkTuner(&st, cfg, byNode, nodeByID, lang)
	m.checkCertificate(&st, cfg, lang)
	m.checkUpdate(&st, cfg, lang)
	m.checkNodeUpdates(ctx, &st, cfg, nodeByID, lang)
	m.publicStatus(ctx, &st, cfg, nodes, byNode, lang)
	// Samples only matter for configured infrastructure. Drop removed node and inbound
	// state so the JSON snapshot stays bounded as installations change over time.
	valid := make(map[string]bool, len(nodes)+len(inbounds))
	for _, n := range nodes {
		valid["node/"+strconv.FormatInt(n.ID, 10)] = true
		valid["warp/"+strconv.FormatInt(n.ID, 10)] = true
		valid["relay/"+strconv.FormatInt(n.ID, 10)] = true
	}
	for _, list := range byNode {
		for _, in := range list {
			valid["inbound/"+strconv.FormatInt(in.ID, 10)] = true
		}
	}
	for key := range st.Samples {
		if strings.HasPrefix(key, "node/") || strings.HasPrefix(key, "warp/") || strings.HasPrefix(key, "relay/") || strings.HasPrefix(key, "inbound/") {
			if !valid[key] {
				delete(st.Samples, key)
			}
		}
	}
	for key := range st.Stuck {
		found := false
		for nodeID, list := range byNode {
			for _, in := range list {
				if key == fmt.Sprintf("%d/%d", nodeID, in.ID) {
					found = true
					break
				}
			}
			if found {
				break
			}
		}
		if !found {
			delete(st.Stuck, key)
		}
	}
	enabled := m.bot != nil && m.bot.InfrastructureEnabled(ctx) // read once per round
	if !enabled {
		st.Pending = nil
	}
	if err := m.save(ctx, st); err != nil {
		m.logError("infrastructure alerts: save state", err)
	} else if enabled && len(st.Pending) > 0 {
		dispatch = true
	}
}

// startDelivery runs Telegram calls separately from health sampling. State is merged only
// after the network calls finish, so a slow Telegram API never delays the next monitor round.
func (m *Monitor) startDelivery(ctx context.Context) {
	m.dispatchMu.Lock()
	if m.dispatching {
		m.dispatchMu.Unlock()
		return
	}
	m.dispatching = true
	m.dispatchMu.Unlock()
	go func() {
		defer func() { m.dispatchMu.Lock(); m.dispatching = false; m.dispatchMu.Unlock() }()
		m.roundMu.Lock()
		st, err := m.load(ctx)
		m.roundMu.Unlock()
		if err != nil {
			m.logError("infrastructure alerts: load delivery queue", err)
			return
		}
		before := append([]delivery(nil), st.Pending...)
		if len(before) == 0 {
			return
		}
		cfg := m.config(ctx)
		m.deliver(ctx, &st, cfg)
		m.roundMu.Lock()
		defer m.roundMu.Unlock()
		latest, err := m.load(ctx)
		if err != nil {
			m.logError("infrastructure alerts: reload delivery queue", err)
			return
		}
		latest.Pending = mergeDeliveryResults(latest.Pending, before, st.Pending)
		if latest.PublicTarget == st.PublicTarget {
			latest.PublicMessage = st.PublicMessage
			latest.PublicPinned = st.PublicPinned
			latest.PublicPinAttempted = st.PublicPinAttempted
		}
		if err := m.save(ctx, latest); err != nil {
			m.logError("infrastructure alerts: save delivery queue", err)
		}
	}()
}

func mergeDeliveryResults(latest, before, after []delivery) []delivery {
	result := append([]delivery(nil), latest...)
	afterByKey := make(map[string]delivery, len(after))
	for _, d := range after {
		afterByKey[d.Key] = d
	}
	for _, old := range before {
		position := -1
		for i := range result {
			if result[i].Key == old.Key {
				position = i
				break
			}
		}
		if position < 0 {
			continue
		}
		updated, remains := afterByKey[old.Key]
		if !remains {
			result = append(result[:position], result[position+1:]...)
			continue
		}
		result[position].Attempts = updated.Attempts
		result[position].NextAttemptAt = updated.NextAttemptAt
		if result[position].CreatedAt == 0 {
			result[position].CreatedAt = updated.CreatedAt
		}
	}
	return result
}

// observe records a new sample, with a stable failure threshold and two-sample recovery.
// A false return means no stable transition was emitted.
func (m *Monitor) observe(st *persistentState, key string, level Level, checked time.Time, failAfter, recoverAfter int, text string) bool {
	s := st.Samples[key]
	if !s.Checked.IsZero() && !checked.After(s.Checked) {
		return false
	}
	s.Checked = checked
	tr := s.Tracker.Observe(level, failAfter, recoverAfter)
	st.Samples[key] = s
	if tr == nil || text == "" {
		return tr != nil
	}
	st.Pending = appendPending(st.Pending, delivery{Key: key + "/" + checked.UTC().Format(time.RFC3339Nano), Target: "admin", Text: text})
	return true
}

func appendPending(all []delivery, next delivery) []delivery {
	for _, d := range all {
		if d.Key == next.Key {
			return all
		}
	}
	if next.CreatedAt == 0 {
		next.CreatedAt = time.Now().Unix()
	}
	const maxPending = 200
	if len(all) >= maxPending {
		all = append([]delivery(nil), all[len(all)-maxPending+1:]...)
	}
	return append(all, next)
}

func eventFor(c AlertsConfig, event, name string, level Level, lang string) string {
	var enabled bool
	switch event {
	case "node":
		enabled = c.Events.Node
	case "warp":
		enabled = c.Events.Warp
	case "exit":
		enabled = c.Events.Exit
	case "inbound":
		enabled = c.Events.Inbound
	}
	if !enabled {
		return ""
	}
	icon, state := "🔴", "Недоступен"
	if lang == "en" {
		state = "Unavailable"
	}
	if level == Healthy {
		icon = "🟢"
		if lang == "en" {
			state = "Recovered"
		} else {
			state = "Восстановлен"
		}
	}
	if level == Degraded {
		icon = "🟡"
		if lang == "en" {
			state = "Degraded"
		} else {
			state = "Есть проблемы"
		}
	}
	return icon + " <b>" + html.EscapeString(name) + "</b> — " + state
}

// checkRelay watches the cascade relay's listener: other nodes leave through it, and a port
// another program holds keeps it down without a word. A node without a relay has no such
// listener and is Healthy here. moves says whether the tuner moves a busy relay by itself,
// which is given time to, as an inbound's move is.
func (m *Monitor) checkRelay(st *persistentState, cfg AlertsConfig, n db.Node, hv nodesync.HealthView, moves bool, lang string) Level {
	i := slices.IndexFunc(hv.Listeners, func(l nodeapi.ListenerStatus) bool { return l.Name == nodeapi.RelayListener })
	if i < 0 {
		return Healthy
	}
	l, level := hv.Listeners[i], Healthy
	if !l.OK {
		level = Unavailable
	}
	label := n.Name + " / " + relayLabel(lang)
	m.observe(st, "relay/"+strconv.FormatInt(n.ID, 10), level, hv.CheckedAt, inboundFailAfter(l.Busy(), moves), 2, eventFor(cfg, "exit", label, level, lang))
	return level
}

// relayLabel names the cascade relay in an alert, as the Nodes page does.
func relayLabel(lang string) string {
	if lang == "en" {
		return "cascade relay"
	}
	return "служебный вход каскада"
}

// warpReason says in a line why the node's WARP check failed, so the alert is not just
// "down". Codes come from the node (nodeapi.WarpStatus); the node's own detail is
// English only, so it is the fallback for codes this panel does not know.
func warpReason(s nodeapi.WarpStatus, endpoint, lang string) string {
	ru := lang != "en"
	var text string
	switch s.Error {
	case "timeout":
		text = "no answer from the WARP endpoint " + endpoint + " over UDP: the host may block UDP, try another endpoint"
		if ru {
			text = "нет ответа от endpoint " + endpoint + " по UDP: возможно, хостер блокирует UDP, попробуйте другой endpoint"
		}
	case "https_timeout":
		text = "the tunnel is up, but cloudflare.com did not answer in time"
		if ru {
			text = "туннель поднят, но cloudflare.com не ответил вовремя"
		}
	case "dns":
		text = "WARP could not resolve a name"
		if ru {
			text = "WARP не смог разрешить имя"
		}
	case "tls":
		text = "TLS error through WARP"
		if ru {
			text = "ошибка TLS через WARP"
		}
	case "refused":
		text = "the connection was refused"
		if ru {
			text = "соединение отклонено"
		}
	case "not_loaded":
		text = "WARP is not loaded on the node yet"
		if ru {
			text = "WARP ещё не загружен на ноде"
		}
	case "bad_answer":
		text = "Cloudflare answered something unexpected"
		if ru {
			text = "Cloudflare ответил непонятно"
		}
	default:
		text = s.Detail
		if text == "" {
			text = s.Error
		}
		if text == "" {
			text = "check failed"
			if ru {
				text = "проверка не прошла"
			}
		}
	}
	if ru {
		return "Причина: " + text
	}
	return "Reason: " + text
}

func (m *Monitor) probeWarp(ctx context.Context, st *persistentState, cfg AlertsConfig, nodes []db.Node, inbounds map[int64][]db.Inbound, levels map[int64]Level, lang string) {
	for _, n := range nodes {
		if n.Enabled == 0 || levels[n.ID] == Unavailable {
			continue
		}
		w, err := m.store.Q.GetNodeWarp(ctx, n.ID)
		if err != nil || w.Enabled == 0 {
			continue
		}
		s, err := m.runtime.Warp(ctx, n.ID, false)
		if err != nil || s.CheckedAt.IsZero() {
			continue
		}
		level := Healthy
		if !s.OK {
			level = Degraded
		}
		text := eventFor(cfg, "warp", n.Name+" / WARP", level, lang)
		if text != "" && level == Degraded {
			text += "\n" + html.EscapeString(warpReason(s, w.Endpoint, lang))
		}
		m.observe(st, "warp/"+strconv.FormatInt(n.ID, 10), level, s.CheckedAt, 2, 2, text)
		if level != Healthy {
			levels[n.ID] = Degraded
		}
	}
}

func (m *Monitor) probeExits(ctx context.Context, st *persistentState, cfg AlertsConfig, nodes []db.Node, inbounds map[int64][]db.Inbound, levels map[int64]Level, lang string) {
	name := map[int64]string{}
	for _, n := range nodes {
		name[n.ID] = n.Name
	}
	seen := map[string]bool{}
	for _, n := range nodes {
		if n.Enabled == 0 || levels[n.ID] == Unavailable {
			continue
		}
		exits := map[int64]bool{}
		for _, in := range inbounds[n.ID] {
			if in.Enabled != 0 && in.Outbound == "node" && in.ExitNodeID.Valid {
				exits[in.ExitNodeID.Int64] = true
			}
		}
		relay, err := m.store.Q.GetNodeRelay(ctx, n.ID)
		if err == nil && relay.Outbound == "node" && relay.ExitNodeID.Valid {
			exits[relay.ExitNodeID.Int64] = true
		}
		for exitID := range exits {
			key := fmt.Sprintf("exit/%d/%d", n.ID, exitID)
			if seen[key] {
				continue
			}
			seen[key] = true
			p, err := m.runtime.Probe(ctx, n.ID, nodeapi.ExitName(exitID))
			if err != nil || p.CheckedAt.IsZero() {
				continue
			}
			level := Healthy
			if !p.OK {
				level = Degraded
			}
			label := name[n.ID] + " → " + name[exitID]
			m.observe(st, key, level, p.CheckedAt, 2, 2, eventFor(cfg, "exit", label, level, lang))
			if level != Healthy {
				levels[n.ID] = Degraded
			}
		}
	}
}

// autotuneEvents names the nodes and inbounds from the round's lists: an event of a node
// removed since is passed over, as before. A move off a port another program held is an
// inbound's event: the inbound did not listen at all until then.
func (m *Monitor) autotuneEvents(ctx context.Context, st *persistentState, cfg AlertsConfig, nodes map[int64]db.Node, inbounds map[int64]db.Inbound, lang string) {
	events, err := m.store.Q.InboundEventsAfter(ctx, st.AutoCursor)
	if err != nil {
		m.logError("infrastructure alerts: autotune events", err)
		return
	}
	for _, e := range events {
		st.AutoCursor = e.ID
		if e.Reason == autotune.ReasonBusy {
			n, ok := nodes[e.NodeID]
			in, known := inbounds[e.InboundID]
			if !cfg.Events.Inbound || !ok || !known {
				continue
			}
			text := fmt.Sprintf("🛠 <b>Подключение перенесено</b>: %s / %s с порта %s на %s, порт занят другой программой",
				html.EscapeString(n.Name), html.EscapeString(in.Name), html.EscapeString(e.OldValue), html.EscapeString(e.NewValue))
			if lang == "en" {
				text = fmt.Sprintf("🛠 <b>Inbound moved</b>: %s / %s from port %s to %s, the port is held by another program",
					html.EscapeString(n.Name), html.EscapeString(in.Name), html.EscapeString(e.OldValue), html.EscapeString(e.NewValue))
			}
			st.Pending = appendPending(st.Pending, delivery{Key: fmt.Sprintf("autotune/%d", e.ID), Target: "admin", Text: text})
			continue
		}
		if !cfg.Events.Autotune {
			continue
		}
		n, ok := nodes[e.NodeID]
		if !ok {
			continue
		}
		verb := "Автонастройка изменила подключение"
		if lang == "en" {
			verb = "Autotune changed an inbound"
		}
		st.Pending = appendPending(st.Pending, delivery{Key: fmt.Sprintf("autotune/%d", e.ID), Target: "admin",
			Text: "🛠 <b>" + verb + "</b>: " + html.EscapeString(n.Name) + " / " + html.EscapeString(e.Kind) + " — " + html.EscapeString(e.OldValue) + " → " + html.EscapeString(e.NewValue)})
	}
}

func (m *Monitor) checkTuner(st *persistentState, cfg AlertsConfig, inbounds map[int64][]db.Inbound, nodes map[int64]db.Node, lang string) {
	if m.tuner == nil {
		return
	}
	for nodeID, list := range inbounds {
		for _, in := range list {
			status, ok := m.tuner.Status(in.ID)
			if !ok {
				continue
			}
			key := fmt.Sprintf("%d/%d", nodeID, in.ID)
			prev := st.Stuck[key]
			if status.Stuck == prev {
				continue
			}
			st.Stuck[key] = status.Stuck
			if autotuneFailure(status.Stuck) && cfg.Events.AutotuneRecovery {
				node, ok := nodes[nodeID]
				if !ok {
					continue
				}
				text := fmt.Sprintf("⚠️ <b>Автонастройка не восстановила подключение</b>: %s / %s (%s)", html.EscapeString(node.Name), html.EscapeString(in.Name), html.EscapeString(status.Stuck))
				if lang == "en" {
					text = fmt.Sprintf("⚠️ <b>Autotune could not restore an inbound</b>: %s / %s (%s)", html.EscapeString(node.Name), html.EscapeString(in.Name), html.EscapeString(status.Stuck))
				}
				st.Pending = appendPending(st.Pending, delivery{Key: "autotune-stuck/" + key + "/" + status.Stuck, Target: "admin", Text: text})
			}
		}
	}
}

func (m *Monitor) checkCertificate(st *persistentState, cfg AlertsConfig, lang string) {
	if m.cert == nil || !cfg.Events.TLS {
		return
	}
	s := m.cert()
	bad := s.Error != "" || (!s.NotAfter.IsZero() && s.NotAfter.Sub(m.now()) < 7*24*time.Hour)
	if bad == st.TLSBad {
		return
	}
	st.TLSBad = bad
	text := "🟡 <b>Проблема с TLS-сертификатом</b>"
	if !bad {
		text = "🟢 <b>TLS-сертификат снова в порядке</b>"
	}
	if lang == "en" {
		text = "🟡 <b>TLS certificate problem</b>"
		if !bad {
			text = "🟢 <b>TLS certificate is healthy again</b>"
		}
	}
	if s.Error != "" {
		text += ": " + html.EscapeString(s.Error)
	}
	st.Pending = appendPending(st.Pending, delivery{Key: fmt.Sprintf("tls/%t/%d", bad, s.CheckedAt.Unix()), Target: "admin", Text: text})
}

func (m *Monitor) checkUpdate(st *persistentState, cfg AlertsConfig, lang string) {
	if m.updates == nil || !cfg.Events.Update {
		return
	}
	s, ok := m.updates.Host()
	if !ok || s.At == "" || s.State == "running" || s.At == st.UpdateAt {
		return
	}
	st.UpdateAt = s.At
	if s.State != "failed" {
		return
	}
	text := "🔴 <b>Ошибка обновления Mikan</b>"
	if lang == "en" {
		text = "🔴 <b>Mikan update failed</b>"
	}
	if s.Error != "" {
		text += ": " + html.EscapeString(s.Error)
	}
	if s.From != "" {
		text += " (" + html.EscapeString(s.From) + " → " + html.EscapeString(s.Version) + ")"
	}
	st.Pending = appendPending(st.Pending, delivery{Key: "update/" + s.At, Target: "admin", Text: text})
}

// checkNodeUpdates tells about each failed update of a node once, by the time it was asked
// for. The panel stops its rollout at the failure; the admin decides what to do next.
func (m *Monitor) checkNodeUpdates(ctx context.Context, st *persistentState, cfg AlertsConfig, nodes map[int64]db.Node, lang string) {
	if m.nodeUpdates == nil || !cfg.Events.Update {
		return
	}
	failures := m.nodeUpdates.Failures(ctx)
	for _, f := range failures {
		n, ok := nodes[f.NodeID]
		id := strconv.FormatInt(f.NodeID, 10)
		if !ok || st.NodeUpdateAt[id] == f.At {
			continue
		}
		if st.NodeUpdateAt == nil {
			st.NodeUpdateAt = map[string]int64{}
		}
		st.NodeUpdateAt[id] = f.At
		text := "🔴 <b>Ошибка обновления ноды</b> " + html.EscapeString(n.Name)
		if lang == "en" {
			text = "🔴 <b>Node update failed</b> " + html.EscapeString(n.Name)
		}
		if f.Error != "" {
			text += ": " + html.EscapeString(f.Error)
		}
		if f.From != "" {
			text += " (" + html.EscapeString(f.From) + " → " + html.EscapeString(f.Version) + ")"
		}
		if lang == "en" {
			text += ". The rollout to the other nodes is stopped; the node can be updated again from the Nodes page."
		} else {
			text += ". Обновление остальных нод остановлено; ноду можно обновить снова на странице «Ноды»."
		}
		st.Pending = appendPending(st.Pending, delivery{Key: "node-update/" + id + "/" + strconv.FormatInt(f.At, 10), Target: "admin", Text: text})
	}
	// A node that was removed, or whose failure is gone, is not remembered.
	for id := range st.NodeUpdateAt {
		found := false
		for _, f := range failures {
			found = found || strconv.FormatInt(f.NodeID, 10) == id
		}
		if !found {
			delete(st.NodeUpdateAt, id)
		}
	}
}

func (m *Monitor) publicStatus(ctx context.Context, st *persistentState, cfg AlertsConfig, nodes []db.Node,
	inbounds map[int64][]db.Inbound, lang string) {
	if !cfg.PublicEnabled {
		st.PublicText = ""
		st.PublicLevels = map[int64]Level{}
		kept := st.Pending[:0]
		for _, d := range st.Pending {
			if d.Target != "summary" && d.Target != "public-change" {
				kept = append(kept, d)
			}
		}
		st.Pending = kept
		return
	}
	if st.PublicLevels == nil {
		st.PublicLevels = map[int64]Level{}
	}
	type row struct {
		id    int64
		name  string
		level Level
	}
	var rows []row
	for _, n := range nodes {
		if n.Enabled == 0 || strings.TrimSpace(n.PublicName) == "" {
			continue
		}
		level := publicNodeLevel(ctx, st, m.store, n.ID, inbounds[n.ID])
		rows = append(rows, row{n.ID, n.PublicName, level})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].id < rows[j].id })
	var lines []string
	if lang == "en" {
		lines = append(lines, "🌐 <b>Server status</b>")
	} else {
		lines = append(lines, "🌐 <b>Состояние серверов</b>")
	}
	if len(rows) == 0 {
		if lang == "en" {
			lines = append(lines, "No servers are published yet.")
		} else {
			lines = append(lines, "Публичные серверы пока не настроены.")
		}
	}
	for _, r := range rows {
		rowText := PublicText(html.EscapeString(r.name), r.level)
		if lang == "en" {
			rowText = PublicTextEN(html.EscapeString(r.name), r.level)
		}
		lines = append(lines, rowText)
	}
	text := strings.Join(lines, "\n")
	for _, r := range rows {
		previous, seen := st.PublicLevels[r.id]
		if seen && previous != r.level && previous != Unknown && r.level != Unknown && cfg.PublicChanges {
			change := PublicText(html.EscapeString(r.name), r.level)
			if lang == "en" {
				change = PublicTextEN(html.EscapeString(r.name), r.level)
			}
			st.Pending = appendPending(st.Pending, delivery{Key: fmt.Sprintf("public/%d/%d/%d", r.id, previous, r.level), Target: "public-change", Text: change})
		}
		st.PublicLevels[r.id] = r.level
	}
	for id := range st.PublicLevels {
		if !containsNode(nodes, id) {
			delete(st.PublicLevels, id)
		}
	}
	if st.PublicTarget != cfg.PublicChannel {
		st.PublicTarget, st.PublicMessage, st.PublicText, st.PublicPinned, st.PublicPinAttempted = cfg.PublicChannel, 0, "", false, false
		kept := st.Pending[:0]
		for _, d := range st.Pending {
			if d.Target != "summary" && d.Target != "public-change" {
				kept = append(kept, d)
			}
		}
		st.Pending = kept
	}
	if !cfg.PublicSummary {
		st.PublicText = ""
		kept := st.Pending[:0]
		for _, d := range st.Pending {
			if d.Target != "summary" {
				kept = append(kept, d)
			}
		}
		st.Pending = kept
	} else if text != st.PublicText || st.PublicMessage > 0 && !st.PublicPinned && !st.PublicPinAttempted {
		st.PublicText = text
		updated := false
		for i := range st.Pending {
			if st.Pending[i].Target == "summary" {
				st.Pending[i].Text = text
				updated = true
				break
			}
		}
		if !updated {
			st.Pending = append(st.Pending, delivery{Key: "summary/current", Target: "summary", Text: text})
		}
	}
}

func publicNodeLevel(ctx context.Context, st *persistentState, store *store.Store, nodeID int64, inbounds []db.Inbound) Level {
	node := stateLevel(st, "node/"+strconv.FormatInt(nodeID, 10))
	if node == Unknown {
		return Unknown
	}
	if node == Unavailable {
		return Unavailable
	}
	total, working, unknown := 0, 0, false
	for _, in := range inbounds {
		if in.Enabled == 0 {
			continue
		}
		total++
		level := stateLevel(st, "inbound/"+strconv.FormatInt(in.ID, 10))
		if level == Unknown {
			unknown = true
			continue
		}
		if level != Healthy {
			continue
		}
		switch in.Outbound {
		case "warp":
			w, err := store.Q.GetNodeWarp(ctx, nodeID)
			if err == nil && w.Enabled != 0 {
				out := stateLevel(st, "warp/"+strconv.FormatInt(nodeID, 10))
				if out == Unknown {
					unknown = true
					continue
				}
				if out != Healthy {
					continue
				}
			}
		case "node":
			if in.ExitNodeID.Valid {
				out := stateLevel(st, fmt.Sprintf("exit/%d/%d", nodeID, in.ExitNodeID.Int64))
				if out == Unknown {
					unknown = true
					continue
				}
				if out != Healthy {
					continue
				}
			}
		}
		working++
	}
	if total == 0 {
		return Unavailable
	}
	if working == total {
		return Healthy
	}
	if working > 0 {
		return Degraded
	}
	if unknown {
		return Unknown
	}
	return Unavailable
}

func stateLevel(st *persistentState, key string) Level {
	if s, ok := st.Samples[key]; ok {
		return s.Level
	}
	return Unknown
}

func (m *Monitor) deliver(ctx context.Context, st *persistentState, cfg AlertsConfig) {
	if m.bot == nil || !m.bot.InfrastructureEnabled(ctx) {
		st.Pending = nil
		return
	}
	if len(st.Pending) == 0 {
		return
	}
	client, err := m.bot.InfrastructureClient(ctx)
	if err != nil {
		m.deferPending(st, err)
		return
	}
	adminID, adminSet, _ := m.bot.InfrastructureAdminChat(ctx)
	keep := st.Pending[:0]
	for _, d := range st.Pending {
		now := m.now().Unix()
		if d.CreatedAt != 0 && now-d.CreatedAt > int64(24*time.Hour/time.Second) {
			continue
		}
		if d.NextAttemptAt != 0 && d.NextAttemptAt > now {
			keep = append(keep, d)
			continue
		}
		err = nil
		if d.Target == "admin" {
			if !cfg.AdminEnabled || !adminSet {
				continue
			}
			cctx, cancel := context.WithTimeout(ctx, 12*time.Second)
			err = sendAdmin(cctx, client, adminID, d.Text)
			cancel()
		} else if d.Target == "summary" {
			if !cfg.PublicEnabled || !cfg.PublicSummary {
				continue
			}
			cctx, cancel := context.WithTimeout(ctx, 12*time.Second)
			if st.PublicMessage == 0 {
				var msg tgbot.Message
				msg, err = client.SendTo(cctx, cfg.PublicChannel, d.Text, true)
				if err == nil {
					st.PublicMessage = msg.MessageID
					st.PublicPinned, st.PublicPinAttempted = false, false
				}
			}
			if err == nil && !st.PublicPinned && !st.PublicPinAttempted {
				pinErr := client.PinTo(cctx, cfg.PublicChannel, st.PublicMessage)
				m.recordPublicPinResult(st, pinErr)
			}
			if err == nil {
				err = client.EditTo(cctx, cfg.PublicChannel, st.PublicMessage, d.Text)
				var ae *tgbot.APIError
				if errors.As(err, &ae) && strings.Contains(strings.ToLower(ae.Description), "message to edit not found") {
					st.PublicMessage, st.PublicPinned, st.PublicPinAttempted = 0, false, false
				}
			}
			cancel()
		} else if d.Target == "public-change" {
			if !cfg.PublicEnabled || !cfg.PublicChanges {
				continue
			}
			cctx, cancel := context.WithTimeout(ctx, 12*time.Second)
			_, err = client.SendTo(cctx, cfg.PublicChannel, d.Text, false)
			cancel()
		}
		if err != nil {
			if permanentTelegramError(err) {
				continue
			}
			if retryDelivery(&d, now, err) {
				keep = append(keep, d)
			}
			if m.log != nil {
				m.log.Warn("infrastructure alerts: Telegram delivery", "target", d.Target, "err", err)
			}
		}
	}
	st.Pending = keep
}

func (m *Monitor) deferPending(st *persistentState, err error) {
	now := m.now().Unix()
	keep := st.Pending[:0]
	for _, d := range st.Pending {
		if retryDelivery(&d, now, err) {
			keep = append(keep, d)
		}
	}
	st.Pending = keep
}

func (m *Monitor) recordPublicPinResult(st *persistentState, err error) {
	if st.PublicPinAttempted || st.PublicPinned {
		return
	}
	st.PublicPinAttempted = true
	if err == nil {
		st.PublicPinned = true
		return
	}
	if m.log != nil {
		m.log.Warn("infrastructure alerts: pin public status failed; will not retry until channel changes", "channel", st.PublicTarget, "err", err)
	}
}

func retryDelivery(d *delivery, now int64, err error) bool {
	d.Attempts++
	if d.Attempts >= 8 || d.CreatedAt != 0 && now-d.CreatedAt > int64(24*time.Hour/time.Second) {
		return false
	}
	delay := 5 * time.Second * time.Duration(1<<min(d.Attempts-1, 10))
	var ae *tgbot.APIError
	if errors.As(err, &ae) && ae.Code == 429 && ae.RetryAfter > delay {
		delay = ae.RetryAfter
	}
	if delay > time.Hour {
		delay = time.Hour
	}
	d.NextAttemptAt = now + int64(delay/time.Second)
	return true
}

func permanentTelegramError(err error) bool {
	var ae *tgbot.APIError
	return errors.As(err, &ae) && ae.Code >= 400 && ae.Code < 500 && ae.Code != 429
}

func sendAdmin(ctx context.Context, c *tgbot.Client, chat int64, text string) error {
	_, err := c.Send(ctx, chat, text, nil, false)
	return err
}

func autotuneFailure(s string) bool { return s == "no_port" || s == "no_target" || s == "exhausted" }

func containsNode(nodes []db.Node, id int64) bool {
	for _, n := range nodes {
		if n.ID == id {
			return true
		}
	}
	return false
}

func PublicTextEN(name string, level Level) string {
	icon, label := "⚪", "Checking"
	switch level {
	case Healthy:
		icon, label = "🟢", "Working"
	case Degraded:
		icon, label = "🟡", "Issues"
	case Unavailable:
		icon, label = "🔴", "Unavailable"
	}
	return icon + " " + name + " — " + label
}

// torrentShown is how many catches one message lists; the rest are counted.
const torrentShown = 10

// torrentEvents tells the admin about the torrent blocker's new catches: one message a
// round, so a wave of them does not flood the chat.
func (m *Monitor) torrentEvents(ctx context.Context, st *persistentState, cfg AlertsConfig, lang string) {
	hits, err := m.store.Q.TorrentHitsAfter(ctx, db.TorrentHitsAfterParams{ID: st.TorrentCursor, Limit: 200})
	if err != nil {
		m.logError("infrastructure alerts: torrent hits", err)
		return
	}
	if len(hits) == 0 {
		return
	}
	st.TorrentCursor = hits[len(hits)-1].ID
	if !cfg.Events.Torrent {
		return
	}
	title, banned, more := "🧲 <b>Пойман торрент</b>", "бан до %s UTC", "и ещё %d"
	if lang == "en" {
		title, banned, more = "🧲 <b>Torrent caught</b>", "banned until %s UTC", "and %d more"
	}
	var b strings.Builder
	b.WriteString(title)
	for i, h := range hits {
		if i == torrentShown {
			b.WriteString("\n" + fmt.Sprintf(more, len(hits)-torrentShown))
			break
		}
		b.WriteString("\n• <b>" + html.EscapeString(h.UserName) + "</b>: ")
		if h.NodeName != "" {
			b.WriteString(html.EscapeString(h.NodeName) + ", ")
		}
		b.WriteString(html.EscapeString(h.Kind) + " → " + html.EscapeString(h.Dest) + " (" + html.EscapeString(h.Ip) + ")")
		if h.Hits > 1 {
			b.WriteString(" ×" + strconv.Itoa(int(h.Hits)))
		}
		if h.BannedUntil > 0 {
			b.WriteString(", " + fmt.Sprintf(banned, time.Unix(h.BannedUntil, 0).UTC().Format("02.01 15:04")))
		}
	}
	st.Pending = appendPending(st.Pending, delivery{Key: fmt.Sprintf("torrent/%d", st.TorrentCursor), Target: "admin", Text: b.String()})
}
