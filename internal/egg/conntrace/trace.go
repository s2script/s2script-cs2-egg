// SPDX-License-Identifier: GPL-3.0-or-later

// Package conntrace observes connection evidence without issuing game commands
// or changing Guard state. A nil Tracer is disabled. Feed only buffers events;
// the existing egg heartbeat drains them so stdout consumption never waits for
// diagnostic disk or socket writes.
package conntrace

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/K4ryuu/CS2-Egg-Go/internal/proto"
)

const (
	maxAttempts    = 32
	maxClients     = 256
	maxEvents      = 128
	observationGap = 2 * time.Minute
)

// Options limits capture to one source address and a short diagnostic window.
// Duration defaults to ten minutes and must be between one and thirty minutes.
// Now may be supplied for log replay; callers must make it concurrency-safe.
type Options struct {
	Enabled  bool
	TargetIP string
	Duration time.Duration
	Now      func() time.Time
}

// Event contains no raw address, port, player name, SteamID, or console text.
// Attempt zero means session-level evidence, not evidence of a specific attempt.
type Event struct {
	Version     int    `json:"v"`
	Session     string `json:"session"`
	Time        string `json:"time"`
	Attempt     uint64 `json:"attempt,omitempty"`
	ElapsedMS   int64  `json:"elapsed_ms,omitempty"`
	Stage       string `json:"stage"`
	Detail      string `json:"detail,omitempty"`
	Signon      string `json:"signon,omitempty"`
	Correlation string `json:"correlation,omitempty"`
	SampledAt   string `json:"sampled_at,omitempty"`
	Packets     uint64 `json:"packets,omitempty"`
	Bytes       uint64 `json:"bytes,omitempty"`
	Lost        uint64 `json:"lost,omitempty"`
}

// Line is a grep-friendly JSON record for the existing console/log pipeline.
func (e Event) Line() string {
	b, _ := json.Marshal(e)
	return "[connection-trace] " + string(b)
}

type attempt struct {
	id                uint64
	endpoint          netip.AddrPort
	uid, name, signon string
	start, last       time.Time
	joined            bool
	transport         string
}

type clientIdentity struct {
	name     string
	endpoint netip.AddrPort
}

// Tracer is safe for concurrent console, heartbeat and node callbacks. State and
// output are bounded even if the selected source floods the server with logs.
type Tracer struct {
	mu              sync.Mutex
	target          netip.Addr
	session         string
	now             func() time.Time
	until           time.Time
	closed          bool
	stopped         atomic.Bool // avoids even parsing after the capture ends
	next            uint64
	attempts        map[netip.AddrPort]*attempt
	events          []Event
	lost            uint64
	clients         map[string]clientIdentity
	namesIncomplete bool
	retiredNames    map[string]bool
}

// New returns nil without allocating or validating other options when disabled.
// Invalid options fail only tracing, never the game server.
func New(o Options) (*Tracer, error) {
	if !o.Enabled {
		return nil, nil
	}
	ip, err := netip.ParseAddr(strings.TrimSpace(o.TargetIP))
	if err != nil || ip.Zone() != "" || ip.IsUnspecified() || ip.IsMulticast() {
		return nil, errors.New("connection trace requires one unicast IP address")
	}
	if o.Duration == 0 {
		o.Duration = 10 * time.Minute
	}
	if o.Duration < time.Minute || o.Duration > 30*time.Minute {
		return nil, errors.New("connection trace duration must be 60 to 1800 seconds")
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	var nonce [8]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return nil, errors.New("connection trace session ID unavailable")
	}
	now := o.Now()
	t := &Tracer{target: ip.Unmap(), session: hex.EncodeToString(nonce[:]), now: o.Now, until: now.Add(o.Duration), attempts: make(map[netip.AddrPort]*attempt)}
	t.clients = make(map[string]clientIdentity)
	t.retiredNames = make(map[string]bool)
	t.add(now, nil, "trace_started", "network_evidence_unavailable_until_sample")
	return t, nil
}

// FromEnv reads the opt-in variables. A malformed duration disables tracing.
func FromEnv(getenv func(string) string) (*Tracer, error) {
	if strings.TrimSpace(getenv("ENABLE_CONNECTION_TRACE")) != "1" {
		return nil, nil
	}
	secs := 600
	if value := strings.TrimSpace(getenv("CONNECTION_TRACE_SECONDS")); value != "" {
		var err error
		secs, err = strconv.Atoi(value)
		if err != nil || secs < 60 || secs > 1800 {
			return nil, errors.New("CONNECTION_TRACE_SECONDS must be 60 to 1800")
		}
	}
	return New(Options{Enabled: true, TargetIP: getenv("CONNECTION_TRACE_IP"), Duration: time.Duration(secs) * time.Second})
}

func (t *Tracer) add(now time.Time, a *attempt, stage, detail string) {
	e := Event{Version: 1, Session: t.session, Time: now.UTC().Format(time.RFC3339Nano), Stage: stage, Detail: detail}
	if a != nil {
		e.Attempt, e.ElapsedMS, e.Signon = a.id, now.Sub(a.start).Milliseconds(), a.signon
	}
	t.enqueue(e)
}

func (t *Tracer) enqueue(e Event) {
	if len(t.events) >= maxEvents {
		t.lost++
		return
	}
	t.events = append(t.events, e)
}

func (t *Tracer) active(now time.Time) bool {
	if t.closed {
		return false
	}
	if !now.Before(t.until) {
		t.stop(now, "window_expired")
		return false
	}
	return true
}

func (t *Tracer) stop(now time.Time, reason string) {
	for _, a := range t.attempts {
		if !a.joined {
			t.add(now, a, "incomplete", reason)
		}
	}
	t.attempts = nil
	t.clients = nil
	t.retiredNames = nil
	t.target = netip.Addr{}
	t.closed = true
	t.stopped.Store(true)
	t.add(now, nil, "trace_stopped", reason)
}

func (t *Tracer) get(now time.Time, ep netip.AddrPort) *attempt {
	if a := t.attempts[ep]; a != nil {
		return a
	}
	if len(t.attempts) >= maxAttempts {
		var oldest *attempt
		for _, a := range t.attempts {
			if oldest == nil || a.id < oldest.id {
				oldest = a
			}
		}
		if !oldest.joined {
			t.add(now, oldest, "incomplete", "capacity_eviction")
		}
		t.retire(oldest)
		delete(t.attempts, oldest.endpoint)
	}
	t.next++
	a := &attempt{id: t.next, endpoint: ep, start: now, last: now}
	t.attempts[ep] = a
	return a
}

// An old name-only disconnect can arrive after a retry starts. Once a name
// loses its definitive boundary, do not use it for terminal attribution again
// in this capture. Overflow conservatively disables all name-only attribution.
func (t *Tracer) retire(a *attempt) {
	if a.name == "" {
		return
	}
	if len(t.retiredNames) < maxAttempts {
		t.retiredNames[a.name] = true
	} else {
		t.namesIncomplete = true
	}
}

// Feed observes known engine log shapes before Guard or cosmetic filtering.
// It never calls a writer, starts a goroutine, or sends a protocol message.
func (t *Tracer) Feed(line string) {
	if t == nil || t.stopped.Load() || len(line) > 8192 {
		return
	}
	p, ok := parse(line)
	if !ok {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	now := t.now()
	if !t.active(now) {
		return
	}
	var a *attempt
	if p.endpoint.IsValid() {
		if p.uid != "" {
			// Keep bounded identity-only context for other sources too: a
			// name-only drop must not terminate a different client's attempt.
			if _, known := t.clients[p.uid]; known || len(t.clients) < maxClients {
				t.clients[p.uid] = clientIdentity{name: p.name, endpoint: p.endpoint}
			} else {
				t.namesIncomplete = true
			}
			for ep, old := range t.attempts {
				if old.uid == p.uid && ep != p.endpoint {
					if !old.joined {
						t.add(now, old, "incomplete", "userid_reused")
					}
					t.retire(old)
					delete(t.attempts, ep)
				}
			}
		}
		if p.endpoint.Addr() != t.target {
			return
		}
		a = t.attempts[p.endpoint]
		// An explicit new client or transport after FULL is a new attempt.
		// Repeated C2S before a terminal event stays in the same attempt:
		// the log cannot distinguish a retransmission from a manual retry.
		newTransport := a != nil && p.transport != "" && a.transport != "" && p.transport != a.transport
		if a != nil && (newTransport || a.joined && (p.stage == "c2s_connect" || p.stage == "client_connected") || p.uid != "" && a.uid != "" && p.uid != a.uid) {
			t.retire(a)
			if !a.joined {
				t.add(now, a, "incomplete", "connection_replaced")
			}
			if a.uid != p.uid {
				delete(t.clients, a.uid)
			}
			delete(t.attempts, p.endpoint)
			a = nil
		}
		if a == nil {
			a = t.get(now, p.endpoint)
		}
		if p.transport != "" {
			a.transport = p.transport
		}
		if p.uid != "" {
			a.uid, a.name = p.uid, p.name
		}
	} else {
		if p.uid == "" {
			matches, targetMatch := 0, false
			var matchedUID string
			for uid, c := range t.clients {
				if c.name == p.name {
					matches++
					matchedUID = uid
					targetMatch = targetMatch || c.endpoint.Addr() == t.target
				}
			}
			if t.namesIncomplete || t.retiredNames[p.name] || matches > 1 {
				if targetMatch {
					t.add(now, nil, "ambiguous_disconnect", "identity_not_unique")
				}
				return
			}
			if p.stage == "dropped" || p.stage == "forced_reconnect" {
				delete(t.clients, matchedUID)
			}
		}
		for _, candidate := range t.attempts {
			match := p.uid != "" && candidate.uid == p.uid && candidate.name == p.name
			if p.uid == "" {
				match = candidate.name != "" && candidate.name == p.name
			}
			if match {
				if a != nil {
					t.add(now, nil, "ambiguous_disconnect", "duplicate_name")
					return
				}
				a = candidate
			}
		}
		if a == nil {
			return
		} // never guess the source of a signon-only line
	}
	a.last = now
	if p.signon != "" {
		a.signon = p.signon
	}
	if p.signon == "FULL" {
		p.stage = "joined"
		a.joined = true
	}
	e := Event{Version: 1, Session: t.session, Time: now.UTC().Format(time.RFC3339Nano), Attempt: a.id, ElapsedMS: now.Sub(a.start).Milliseconds(), Stage: p.stage, Detail: p.detail, Signon: a.signon}
	if !p.endpoint.IsValid() && p.uid == "" {
		e.Correlation = "name_only"
	}
	t.enqueue(e)
	if p.stage == "forced_reconnect" {
		t.retire(a)
	}
	if p.stage == "dropped" || p.stage == "rejected" || p.stage == "forced_reconnect" {
		delete(t.attempts, a.endpoint)
	}
}

// Tick expires quiet incomplete attempts and the capture window. Call it from
// the existing heartbeat; a gap is missing evidence, not a claimed failure.
func (t *Tracer) Tick() {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	now := t.now()
	if !t.active(now) {
		return
	}
	for ep, a := range t.attempts {
		if !a.joined && now.Sub(a.last) >= observationGap {
			t.add(now, a, "incomplete", "observation_gap")
			t.retire(a)
			delete(t.attempts, ep)
		}
	}
}

// Query returns a read-only request for an already-running node Guard sampler.
// It never asks the node to enable monitoring, change rules, or capture packets.
func (t *Tracer) Query() *proto.ConnectionTraceQuery {
	if t == nil {
		return nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.active(t.now()) {
		return nil
	}
	return &proto.ConnectionTraceQuery{IP: t.target.String()}
}

// Network records source-wide evidence separately from per-connection events.
// Cached counters span all protected ports and cannot identify an attempt.
func (t *Tracer) Network(s *proto.ConnectionTraceSample) {
	if t == nil || s == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	now := t.now()
	ip, err := netip.ParseAddr(s.IP)
	if !t.active(now) || err != nil || ip.Unmap() != t.target {
		return
	}
	detail := "unavailable"
	if s.Status == "sampled" {
		detail = "not_observed"
		if s.Present {
			detail = "counter_present"
		}
	}
	e := Event{Version: 1, Session: t.session, Time: now.UTC().Format(time.RFC3339Nano), Stage: "network_sample", Detail: detail, Correlation: "source_ip_all_game_ports"}
	if stamp, err := time.Parse(time.RFC3339Nano, s.SampledAt); err == nil && s.Status == "sampled" {
		e.SampledAt = stamp.UTC().Format(time.RFC3339Nano)
		e.Packets, e.Bytes = s.Packets, s.Bytes
	}
	t.enqueue(e)
}

// Block observes existing notices without forwarding hits or invoking Guard.
func (t *Tracer) Block(b *proto.GuardBlock) {
	if t == nil || b == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	now := t.now()
	ip, err := netip.ParseAddr(b.IP)
	if !t.active(now) || err != nil || ip.Unmap() != t.target {
		return
	}
	stage := "host_block"
	if b.Dry {
		stage = "host_would_block"
	}
	t.add(now, nil, stage, "notice_only")
}

// Drain returns and clears a bounded batch. Callers perform output after the
// lock is released. Overflow is explicit so lost evidence is never silent.
func (t *Tracer) Drain() []Event {
	if t == nil {
		return nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	out := t.events
	t.events = nil
	if t.lost > 0 {
		out = append(out, Event{Version: 1, Session: t.session, Time: t.now().UTC().Format(time.RFC3339Nano), Stage: "events_lost", Lost: t.lost})
		t.lost = 0
	}
	return out
}

// Close releases identifying state and marks unfinished observations incomplete.
func (t *Tracer) Close() {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.closed {
		t.stop(t.now(), "server_stopped")
	}
}
