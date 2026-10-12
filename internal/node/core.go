// SPDX-License-Identifier: GPL-3.0-or-later
// Package node hosts optional services without making the standalone egg depend
// on daemon availability. A server's authority is its configured listener.
package node

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/s2script/s2script-cs2-egg/internal/alerts"
	"github.com/s2script/s2script-cs2-egg/internal/config"
	"github.com/s2script/s2script-cs2-egg/internal/events"
	"github.com/s2script/s2script-cs2-egg/internal/nodetypes"
	"github.com/s2script/s2script-cs2-egg/internal/protocol"
	"github.com/s2script/s2script-cs2-egg/internal/server"
	"github.com/s2script/s2script-cs2-egg/internal/storeio"
)

// runtimeContact is a reporting session, not a read-only socket observer.
// Its run is accessed only under Core.mu.
type runtimeContact struct{ run string }

type Core struct {
	observers     map[*observer]struct{}
	observed      map[string]string
	observedOrder []string
	sequence      uint64
	sessionID     string
	fences        map[string]*runFence

	cfg            config.Node
	sink           events.Sink
	delivery       alerts.AcknowledgedDelivery
	mu             sync.Mutex
	running        bool
	ready          chan struct{}
	servers        map[string]Server
	statuses       map[string]server.Status
	contacts       map[string]map[*runtimeContact]struct{}
	modules        map[string]*moduleEntry
	actions        map[string]*moduleEntry
	state          deliveryState
	lockHeld       bool
	storageFailed  bool
	stateDir, lock *os.File
	wake           chan struct{}
}

func New(cfg config.Node, sink events.Sink) (*Core, error) {
	if e := cfg.Validate(); e != nil {
		return nil, e
	}
	cfg.Servers = append([]config.NodeServer(nil), cfg.Servers...)
	c := &Core{cfg: cfg, sink: sink, ready: make(chan struct{}), servers: map[string]Server{}, statuses: map[string]server.Status{}, contacts: map[string]map[*runtimeContact]struct{}{}, modules: map[string]*moduleEntry{}, actions: map[string]*moduleEntry{}, state: deliveryState{Version: 2, NodeID: cfg.ID, Owners: map[string]string{}, Records: map[string]deliveryRecord{}}, wake: make(chan struct{}, 1), observers: map[*observer]struct{}{}, observed: map[string]string{}, fences: map[string]*runFence{}, sessionID: storeio.ID()}
	if !c.safeIdentifiers(cfg.ID) {
		return nil, errors.New("secret-bearing configured identity refused")
	}
	for _, s := range cfg.Servers {
		if !c.safeIdentifiers(s.ID) {
			return nil, errors.New("secret-bearing configured identity refused")
		}
	}
	if d, ok := sink.(alerts.AcknowledgedDelivery); ok && d.Enabled() {
		c.delivery = d
	}
	for _, s := range cfg.Servers {
		d, e := openDirectory(s.Volume)
		if e != nil {
			return nil, e
		}
		d.Close()
		d, e = openDirectory(filepath.Join(s.Volume, "egg"))
		if e != nil {
			return nil, e
		}
		d.Close()
		if !groupAllowed(s.AccessGID) {
			return nil, errors.New("daemon must belong to configured socket access groups")
		}
		c.servers[s.ID] = Server{ID: s.ID, Volume: s.Volume, SocketPath: filepath.Join(s.Volume, "egg/s2node.sock"), Allocation: s.Allocation}
		if s.HelperSocket != "" {
			srv := c.servers[s.ID]
			srv.Helper = &nodetypes.PrivateHelper{Socket: s.HelperSocket, UID: s.PeerUID, GID: s.AccessGID}
			c.servers[s.ID] = srv
		}
		c.statuses[s.ID] = server.Status{State: "unknown", Versions: map[string]string{}}
	}
	return c, nil
}
func (c *Core) Ready() <-chan struct{} { return c.ready }
func (c *Core) Servers() []Server {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]Server, 0, len(c.servers))
	for _, s := range c.servers {
		out = append(out, copyServer(s))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}
func cloneStatus(s server.Status) server.Status {
	v := map[string]string{}
	for k, x := range s.Versions {
		v[k] = x
	}
	s.Versions = v
	return s
}
func (c *Core) Status(id string) server.Status {
	c.mu.Lock()
	defer c.mu.Unlock()
	s := cloneStatus(c.statuses[id])
	max := 15 * time.Second
	for _, configured := range c.cfg.Servers {
		if configured.ID == id && configured.Updates.Policy.FreshnessSeconds > 0 {
			max = time.Duration(configured.Updates.Policy.FreshnessSeconds) * time.Second
		}
	}
	now := time.Now().UTC()
	if !s.SampleAt.IsZero() && (s.SampleAt.After(now) || s.ReceivedAt.IsZero() || s.ReceivedAt.After(now) || now.Sub(s.SampleAt) > max || now.Sub(s.ReceivedAt) > max) {
		s.Players = server.Players{}
		s.Ready = false
	}
	return c.cleanStatus(s)
}
func (c *Core) Publish(ctx context.Context, e events.Event) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if _, ok := c.servers[e.ServerID]; !ok {
		return errors.New("unknown configured server")
	}
	_, err := c.admitSource(e.ServerID, e, "local_runtime")
	return err
}
func (c *Core) Dispatch(ctx context.Context, id, action string, payload any) (any, error) {
	s, ok := c.servers[id]
	if !ok {
		return nil, errors.New("unknown configured server")
	}
	b, e := json.Marshal(payload)
	if e != nil {
		return nil, e
	}
	m := protocol.Message{Version: protocol.Version, Type: action, Payload: b}
	if e = validateAction(m); e != nil {
		return nil, e
	}
	return c.invoke(ctx, s, action, m)
}

// Run is single-use; durable lock is held for its entire lifetime. Optional
// modules must return from Start promptly and honor all supplied contexts.
func (c *Core) Run(ctx context.Context) (err error) {
	c.mu.Lock()
	if c.running {
		c.mu.Unlock()
		return errors.New("core already started")
	}
	c.running = true
	c.mu.Unlock()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	defer func() {
		if c.lock != nil {
			c.lock.Close()
		}
		if c.stateDir != nil {
			c.stateDir.Close()
		}
	}()
	if err = c.openState(); err != nil {
		return err
	}
	var sockets []*socket
	defer func() {
		for _, s := range sockets {
			s.close()
		}
	}()
	type binding struct {
		s     *socket
		id    string
		uid   uint32
		admin bool
	}
	bindings := []binding{}
	for _, record := range c.cfg.Servers {
		d, e := openDirectory(filepath.Join(record.Volume, "egg"))
		if e != nil {
			return e
		}
		s, e := bindSocket(d, "s2node.sock", record.AccessGID, false)
		if e != nil {
			d.Close()
			return e
		}
		sockets = append(sockets, s)
		bindings = append(bindings, binding{s: s, id: record.ID, uid: record.PeerUID})
	}
	if err = os.MkdirAll(filepath.Dir(c.cfg.AdminSocket), 0700); err != nil {
		return err
	}
	d, e := openDirectory(filepath.Dir(c.cfg.AdminSocket))
	if e != nil {
		return e
	}
	info, e := d.Stat()
	if e != nil || info.Mode().Perm()&0077 != 0 {
		d.Close()
		return errors.New("admin socket directory must be private (0700)")
	}
	s, e := bindSocket(d, filepath.Base(c.cfg.AdminSocket), os.Getgid(), true)
	if e != nil {
		d.Close()
		return e
	}
	sockets = append(sockets, s)
	bindings = append(bindings, binding{s: s, admin: true, uid: uint32(os.Geteuid())})
	c.startModules(ctx)
	defer func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		c.closeModules(closeCtx)
	}()
	var wg sync.WaitGroup
	stop := context.AfterFunc(ctx, func() {
		for _, s := range sockets {
			s.listener.Close()
		}
	})
	defer stop()
	for _, b := range bindings {
		wg.Add(1)
		go func() { defer wg.Done(); c.serve(ctx, b.s.listener, b.id, b.uid, b.admin) }()
	}
	wg.Add(1)
	go func() { defer wg.Done(); c.deliveryWorker(ctx) }()
	close(c.ready)
	<-ctx.Done()
	for _, s := range sockets {
		s.listener.Close()
	}
	wg.Wait()
	return nil
}
func (c *Core) serve(ctx context.Context, l *net.UnixListener, id string, uid uint32, admin bool) {
	var wg sync.WaitGroup
	defer wg.Wait()
	slots := make(chan struct{}, 32)
	for {
		conn, e := l.AcceptUnix()
		if e != nil {
			return
		}
		peer, e := peerUID(conn)
		if e != nil || (peer != uid && !(admin && peer == 0)) {
			conn.Close()
			continue
		}
		select {
		case slots <- struct{}{}:
		default:
			conn.Close()
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-slots }()
			contact := &runtimeContact{}
			if !admin {
				defer c.disconnect(id, contact)
			}

			hello := false
			caps := map[string]bool{}
			protocol.ServeConn(ctx, conn, func(ctx context.Context, m protocol.Message) (any, error) {
				if !hello {
					if m.Type != "hello" {
						return nil, errors.New("hello required")
					}
					var h protocol.Hello
					if protocol.Decode(m.Payload, &h) != nil {
						return nil, errors.New("invalid hello")
					}
					available := []string{protocol.CapStatus}
					if admin {
						available = append(available, protocol.CapOperations)
						c.mu.Lock()
						if c.actions[protocol.CapUpdateAdmin] != nil {
							available = append(available, protocol.CapUpdateAdmin)
						}
						c.mu.Unlock()
					}
					if !admin {
						available = append(available, protocol.CapEvents)
						c.mu.Lock()
						for cap := range c.actions {
							if cap != protocol.CapOperations && cap != protocol.CapUpdateExecute && cap != protocol.CapUpdateAdmin {
								available = append(available, cap)
							}
						}
						c.mu.Unlock()
					}
					for _, cap := range available {
						if len(h.Capabilities) == 0 {
							caps[cap] = true
						} else {
							for _, asked := range h.Capabilities {
								if cap == asked {
									caps[cap] = true
								}
							}
						}
					}
					selected := []string{}
					for cap := range caps {
						selected = append(selected, cap)
					}
					sort.Strings(selected)
					hello = true
					return protocol.Welcome{Capabilities: selected, ServerID: id, NodeID: c.cfg.ID}, nil
				}
				return c.handle(ctx, id, admin, caps, m, contact)
			})
		}()
	}
}

// reportContactLocked registers only a successful report/claim for the current
// run. Status reads and unrelated prior-run sessions cannot keep it alive.
func (c *Core) reportContactLocked(id, run string, contact *runtimeContact) {
	if current := c.statuses[id].RunID; current != "" && current != run {
		return
	}
	if c.contacts[id] == nil {
		c.contacts[id] = map[*runtimeContact]struct{}{}
	}
	contact.run = run
	c.contacts[id][contact] = struct{}{}
}
func (c *Core) disconnect(id string, contact *runtimeContact) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, reported := c.contacts[id][contact]; !reported {
		return
	}
	delete(c.contacts[id], contact)
	s := c.statuses[id]
	if contact.run != s.RunID || s.State == "stopped" {
		return
	}
	for remaining := range c.contacts[id] {
		if remaining.run == s.RunID {
			return
		}
	}
	s.State = "lost_contact"
	s.Ready = false
	s.Players = server.Players{}
	c.statuses[id] = s
}

func (c *Core) handle(ctx context.Context, id string, admin bool, caps map[string]bool, m protocol.Message, contact *runtimeContact) (any, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	cap := m.Type
	switch m.Type {
	case "run.claim", "event":
		cap = protocol.CapEvents
	case "status.report":
		cap = protocol.CapStatus
	}
	if !caps[cap] {
		return nil, errors.New("capability was not negotiated")
	}
	switch m.Type {
	case protocol.CapOperations:
		if !admin {
			return nil, errors.New("administrator socket required")
		}
		var r protocol.OperationsRequest
		if protocol.Decode(m.Payload, &r) != nil {
			return nil, errors.New("invalid operations request")
		}
		switch r.Operation {
		case "archive":
			return c.Archive(ctx)
		case "archive-status":
			c.mu.Lock()
			defer c.mu.Unlock()
			return c.archiveUsage(), nil
		case "finalize":
			return struct{}{}, c.FinalizeRun(ctx, r.ServerID, r.RunID)
		}
		return c.invoke(ctx, Server{}, protocol.CapOperations, m)
	case "status":
		if protocol.Decode(m.Payload, &struct{}{}) != nil {
			return nil, errors.New("status takes no server selector")
		}
		if admin {
			statuses := map[string]server.Status{}
			for _, s := range c.Servers() {
				statuses[s.ID] = c.Status(s.ID)
			}
			return struct {
				Servers map[string]server.Status `json:"servers"`
				Modules map[string]Health        `json:"modules"`
			}{statuses, c.ModuleHealth()}, nil
		}
		return c.Status(id), nil
	case "status.report":
		if admin {
			return nil, errors.New("server socket required")
		}
		var s server.Status
		if len(m.Payload) > 16<<10 || protocol.Decode(m.Payload, &s) != nil || s.RunID == "" || len(s.RunID) > 128 {
			return nil, errors.New("invalid runtime status")
		}
		if !c.safeIdentifiers(s.RunID, s.ScheduledActionID) {
			return nil, errors.New("secret-bearing status identity refused")
		}
		switch s.State {
		case "preparing", "starting", "ready", "unresponsive", "stopping", "stopped":
		default:
			return nil, errors.New("invalid runtime state")
		}
		if s.Ready && s.State != "ready" {
			return nil, errors.New("inconsistent runtime readiness")
		}
		c.mu.Lock()
		if current := c.statuses[id].RunID; current != "" && current != s.RunID {
			c.mu.Unlock()
			return nil, errors.New("status belongs to a different negotiated run")
		}
		c.touch(key(id, s.RunID))
		c.reportContactLocked(id, s.RunID, contact)
		// A terminal observation dominates queued same-run reports. The runtime
		// deliberately delivers lifecycle events ahead of raw status backlog.
		if c.statuses[id].State != "stopped" {
			c.statuses[id] = receiveStatus(s, c.statuses[id], time.Now().UTC())
		}
		c.mu.Unlock()
		return struct{}{}, nil
	case "run.claim":
		var r protocol.Claim
		if protocol.Decode(m.Payload, &r) != nil {
			return nil, errors.New("invalid run claim")
		}
		owner, e := c.claim(id, r.RunID)
		if e == nil {
			c.mu.Lock()
			c.reportContactLocked(id, r.RunID, contact)
			c.mu.Unlock()
		}
		return protocol.Ownership{Owner: owner}, e
	case "event":
		var e events.Event
		if protocol.Decode(m.Payload, &e) != nil {
			return nil, errors.New("invalid event payload")
		}
		out, err := c.admit(id, e)
		if err == nil {
			c.observe(id, e, contact)
		}
		return protocol.Receipt{Result: out}, err
	default:
		if m.Type == protocol.CapUpdateExecute {
			return nil, errors.New("host dispatcher required")
		}
		if m.Type == protocol.CapUpdateAdmin {
			if !admin {
				return nil, errors.New("administrator socket required")
			}
			if e := validateAction(m); e != nil {
				return nil, e
			}
			return c.invoke(ctx, Server{}, m.Type, m)
		}
		if admin {
			return nil, errors.New("server socket required")
		}
		if e := validateAction(m); e != nil {
			return nil, e
		}
		return c.invoke(ctx, c.servers[id], m.Type, m)
	}
}
func (c *Core) observe(id string, e events.Event, contact *runtimeContact) {
	c.mu.Lock()
	defer c.mu.Unlock()
	s := c.statuses[id]
	if s.RunID != "" && s.RunID != e.RunID {
		return
	}
	c.reportContactLocked(id, e.RunID, contact)
	if s.State == "stopped" {
		return
	}
	s.RunID = e.RunID
	switch e.Type {
	case "server.up":
		s.State = "ready"
		s.Ready = true
		s.Map = e.Details["map"]
	case "server.down", "server.start_failed":
		s.State = "stopped"
		s.Ready = false
		s.Players = server.Players{}
		s.IntendedStopReason = e.Reason
		s.ScheduledActionID = e.Details["action_id"]
		s.OperatorVeto = e.Details["operator_veto"] == "true"
	case "server.status":
		if raw := e.Details["runtime_status"]; raw != "" {
			var reported server.Status
			if json.Unmarshal([]byte(raw), &reported) == nil && reported.RunID == e.RunID {
				s = receiveStatus(reported, s, time.Now().UTC())
			}
		} else if e.Reason == "query_ok" && !e.At.IsZero() && !e.At.Before(s.SampleAt) && !e.At.After(time.Now().UTC()) {
			if !e.At.IsZero() && e.At.After(s.SampleAt) && !e.At.After(time.Now().UTC()) {
				s.SampleAt = e.At
				s.ReceivedAt = time.Now().UTC()
			}
			s.State = "ready"
			s.Ready = true
			s.Map = e.Details["map"]
			if players, err := strconv.Atoi(e.Details["players"]); err == nil && players >= 0 {
				s.Players = server.Players{Known: true, Count: players}
			}
		}
		if e.Reason == "query_failed" {
			s.State = "unresponsive"
			s.Ready = false
			s.Players = server.Players{}
		}
	}
	c.statuses[id] = s
}
func validateAction(m protocol.Message) error {
	switch m.Type {
	case protocol.CapUpdateControl:
		var r protocol.ControlRequest
		if protocol.Decode(m.Payload, &r) != nil || r.RunID == "" || len(r.RunID) > 128 {
			return errors.New("bounded current run required")
		}
	case protocol.CapUpdateExecute:
		var r protocol.ExecuteUpdate
		if protocol.Decode(m.Payload, &r) != nil || len(r.JobID) != 32 {
			return errors.New("invalid job identity")
		}
	case protocol.CapUpdateAdmin:
		var r protocol.UpdateAdmin
		if protocol.Decode(m.Payload, &r) != nil {
			return errors.New("invalid update administration")
		}
		if r.Operation != "list" && r.Operation != "retry" && r.Operation != "cancel" {
			return errors.New("invalid update administration operation")
		}
	case protocol.CapPrepare:
		var r protocol.PrepareRequest
		if protocol.Decode(m.Payload, &r) != nil || r.AppID != 730 {
			return errors.New("preparation requires app 730 and no server path")
		}
	case protocol.CapCache:
		var r protocol.CacheRequest
		if protocol.Decode(m.Payload, &r) != nil || !protocol.CacheOperation(r.Operation) {
			return errors.New("unsupported cache request")
		}
	case protocol.CapWorkshop:
		var r protocol.WorkshopRequest
		if protocol.Decode(m.Payload, &r) != nil || !protocol.WorkshopOperation(r.Operation) || len(r.ItemID) > 32 {
			return errors.New("invalid workshop request")
		}
		for _, ch := range r.ItemID {
			if ch < '0' || ch > '9' {
				return errors.New("invalid workshop item")
			}
		}
	case protocol.CapRestart:
		var r protocol.RestartRequest
		if protocol.Decode(m.Payload, &r) != nil || r.MaxDelaySeconds < 1 || r.MaxDelaySeconds > 604800 {
			return errors.New("restart requires a bounded maximum delay")
		}
		switch r.Policy {
		case "immediate", "when_empty":
		case "maintenance_window":
			if _, e := time.Parse("15:04", r.WindowStart); e != nil {
				return errors.New("invalid maintenance window")
			}
			if _, e := time.Parse("15:04", r.WindowEnd); e != nil {
				return errors.New("invalid maintenance window")
			}
		default:
			return errors.New("invalid restart policy")
		}
	default:
		return errors.New("unsupported action")
	}
	return nil
}

// A repeated or backlogged sample cannot renew freshness, even over a live socket.
func receiveStatus(s, previous server.Status, now time.Time) server.Status {
	if previous.RunID == s.RunID && !previous.SampleAt.IsZero() && s.SampleAt.Before(previous.SampleAt) {
		return previous
	}
	s.ReceivedAt = previous.ReceivedAt
	if s.SampleAt.IsZero() || s.SampleAt.After(now) {
		s.Players = server.Players{}
		s.Ready = false
		s.ReceivedAt = time.Time{}
	} else if previous.RunID != s.RunID || s.SampleAt.After(previous.SampleAt) {
		s.ReceivedAt = now
	}
	return cloneStatus(s)
}
