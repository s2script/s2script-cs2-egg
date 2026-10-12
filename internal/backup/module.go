// SPDX-License-Identifier: GPL-3.0-or-later
package backup

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/s2script/s2script-cs2-egg/internal/config"
	"github.com/s2script/s2script-cs2-egg/internal/events"
	node "github.com/s2script/s2script-cs2-egg/internal/nodetypes"
	"github.com/s2script/s2script-cs2-egg/internal/protocol"
	"github.com/s2script/s2script-cs2-egg/internal/redaction"
	"github.com/s2script/s2script-cs2-egg/internal/storeio"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

type Job struct {
	ID, Server, State, Error string
	Request                  Request
	Result                   Reply
	CreatedAt                time.Time
	Event                    events.OperationalEvent
	Published                bool
	recover                  bool
	dirty                    bool
}
type Module struct {
	Root    string
	Config  config.BackupNode
	Secrets []string
	host    node.Host
	mu      sync.Mutex
	jobs    map[string]*Job
	busy    map[string]bool
	wake    chan struct{}
	cancel  context.CancelFunc
	wg      sync.WaitGroup
	health  node.Health
	next    map[string]time.Time
}

func (m *Module) Name() string           { return "backup" }
func (m *Module) Capabilities() []string { return []string{protocol.CapBackupAdmin} }
func (m *Module) Health() node.Health    { m.mu.Lock(); defer m.mu.Unlock(); return m.health }
func (m *Module) Start(ctx context.Context, h node.Host) error {
	if e := m.Config.Validate(); e != nil {
		return e
	}
	for _, s := range h.Servers() {
		if redaction.Redact(s.ID, m.Secrets) != s.ID {
			return errors.New("secret-bearing server identity refused")
		}
	}
	m.host = h
	m.jobs = map[string]*Job{}
	m.busy = map[string]bool{}
	m.next = map[string]time.Time{}
	m.wake = make(chan struct{}, 2)
	m.health = node.Health{Status: "healthy"}
	if e := os.MkdirAll(filepath.Join(m.Root, "jobs"), 0700); e != nil {
		return e
	}
	files, e := os.ReadDir(filepath.Join(m.Root, "jobs"))
	if e != nil {
		return e
	}
	if len(files) > 128*len(h.Servers()) {
		return errors.New("backup job history quota")
	}
	for _, f := range files {
		var j Job
		if e = readJSON(filepath.Join(m.Root, "jobs"), f.Name(), &j, 512<<10); e != nil {
			return e
		}
		if !storeio.Digest(j.ID) || f.Name() != j.ID+".json" || m.server(j.Server).ID == "" {
			return errors.New("invalid backup job")
		}
		if j.State == "running" {
			j.recover = true
			j.State = "pending"
		}
		m.jobs[j.ID] = &j
	}
	if e = readJSON(m.Root, "schedule.json", &m.next, 64<<10); e != nil && !errors.Is(e, os.ErrNotExist) {
		return e
	}
	life, cancel := context.WithCancel(ctx)
	m.cancel = cancel
	for range 2 {
		m.wg.Add(1)
		go m.worker(life)
	}
	m.wg.Add(1)
	go m.maintenance(life)
	m.signal()
	return nil
}
func (m *Module) Close(ctx context.Context) error {
	if m.cancel != nil {
		m.cancel()
	}
	done := make(chan struct{})
	go func() { m.wg.Wait(); close(done) }()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (m *Module) server(id string) node.Server {
	for _, s := range m.host.Servers() {
		if s.ID == id {
			return s
		}
	}
	return node.Server{}
}
func (m *Module) signal() {
	for range 2 {
		select {
		case m.wake <- struct{}{}:
		default:
		}
	}
}
func (m *Module) save(j *Job) error {
	return storeio.Save(filepath.Join(m.Root, "jobs"), j.ID+".json", j)
}
func (m *Module) Handle(ctx context.Context, _ node.Server, _ string, msg protocol.Message) (any, error) {
	var r Request
	if e := protocol.Decode(msg.Payload, &r); e != nil {
		return nil, e
	}
	s := m.server(r.ServerID)
	if s.ID == "" || s.Helper == nil {
		return nil, errors.New("registered server with backup helper required")
	}
	for _, v := range []string{r.ServerID, r.RequestID, r.ID, r.Confirm, r.Operation} {
		if redaction.Redact(v, m.Secrets) != v {
			return nil, errors.New("secret-bearing backup identity refused")
		}
	}
	switch r.Operation {
	case "job":
		m.mu.Lock()
		defer m.mu.Unlock()
		j := m.jobs[r.ID]
		if j == nil || j.Server != s.ID {
			return nil, errors.New("unknown scoped backup job")
		}
		return *j, nil
	case "list", "document", "status":
		return Call(ctx, s, r)
	case "create", "preview", "restore", "recovery-release":
		return m.submit(r)
	}
	return nil, errors.New("invalid backup administration")
}
func (m *Module) submit(r Request) (Job, error) {
	if !identifier(r.RequestID) {
		return Job{}, errors.New("stable request_id required")
	}
	if r.Operation == "restore" || r.Operation == "recovery-release" {
		if !storeio.Digest(r.Confirm) {
			return Job{}, errors.New("exact digest confirmation required")
		}
	}
	if r.Operation != "create" && !identifier(r.ID) {
		return Job{}, errors.New("catalog or preview ID required")
	}
	if r.Mode != "" && r.Mode != "consistent" && r.Mode != "live-best-effort" {
		return Job{}, errors.New("invalid backup mode")
	}
	key := storeio.Hash([]byte(r.ServerID + "\x00" + r.RequestID))
	m.mu.Lock()
	defer m.mu.Unlock()
	if j := m.jobs[key]; j != nil {
		b, _ := json.Marshal(j.Request)
		want, _ := json.Marshal(r)
		if string(b) != string(want) {
			return Job{}, errors.New("backup request identity conflict")
		}
		return *j, nil
	}
	outstanding, count := 0, 0
	var retire []*Job
	for _, j := range m.jobs {
		if j.Server != r.ServerID {
			continue
		}
		count++
		if j.State == "pending" || j.State == "running" {
			outstanding++
		} else if j.State != "uncertain" && j.Published && !j.dirty {
			retire = append(retire, j)
		}
	}
	if outstanding >= 16 {
		return Job{}, errors.New("server backup queue full")
	}
	if count >= 128 {
		sort.Slice(retire, func(i, j int) bool { return retire[i].CreatedAt.Before(retire[j].CreatedAt) })
		if len(retire) == 0 {
			return Job{}, errors.New("backup history pinned by active/uncertain/unpublished jobs")
		}
		if e := os.Remove(filepath.Join(m.Root, "jobs", retire[0].ID+".json")); e != nil {
			return Job{}, e
		}
		delete(m.jobs, retire[0].ID)
	}
	j := &Job{ID: key, Server: r.ServerID, State: "pending", Request: r, CreatedAt: time.Now().UTC()}
	if e := m.save(j); e != nil {
		return Job{}, e
	}
	m.jobs[key] = j
	m.signal()
	return *j, nil
}
func (m *Module) claim() *Job {
	m.mu.Lock()
	defer m.mu.Unlock()
	var picked *Job
	for _, j := range m.jobs {
		if j.State == "pending" && !m.busy[j.Server] && (picked == nil || j.CreatedAt.Before(picked.CreatedAt)) {
			picked = j
		}
	}
	if picked == nil {
		return nil
	}
	copy := *picked
	copy.State = "running"
	if e := m.save(&copy); e != nil {
		m.health = node.Health{Status: "degraded", Explanation: "backup job persistence unavailable"}
		return nil
	}
	*picked = copy
	m.busy[copy.Server] = true
	return &copy
}
func (m *Module) worker(ctx context.Context) {
	defer m.wg.Done()
	for {
		select {
		case <-ctx.Done():
			return
		case <-m.wake:
		}
		for {
			if ctx.Err() != nil {
				return
			}
			j := m.claim()
			if j == nil {
				break
			}
			r := j.Request
			if j.recover {
				r = Request{Operation: "status", ID: j.ID}
			}
			task, cancel := context.WithTimeout(ctx, 30*time.Minute)
			reply, e := Call(task, m.server(j.Server), r)
			cancel()
			reply.Error = redaction.Redact(reply.Error, m.Secrets)
			j.Result = reply
			j.State = reply.State
			if e != nil {
				j.Error = redaction.Redact(e.Error(), m.Secrets)
				if reply.State == "failed" {
					j.State = "failed"
				} else {
					j.State = "uncertain"
				}
			}
			if j.State != "complete" && j.State != "failed" && j.State != "uncertain" {
				j.State = "uncertain"
			}
			at := reply.CompletedAt
			if at.IsZero() {
				at = time.Now().UTC()
			}
			kind := "backup"
			if j.Request.Operation == "restore" {
				kind = "restore"
			}
			severity := "info"
			if j.State != "complete" {
				severity = "error"
			}
			j.Event = events.OperationalEvent{ServerID: j.Server, ActionID: j.ID, EventKey: "complete", Type: kind, Severity: severity, At: at, Reason: j.Request.Operation, Details: map[string]string{"state": j.State}}
			m.mu.Lock()
			j.dirty = m.save(j) != nil
			m.jobs[j.ID] = j
			delete(m.busy, j.Server)
			if j.dirty {
				m.health = node.Health{Status: "degraded", Explanation: "backup completion awaiting persistence"}
			}
			m.mu.Unlock()
			m.signal()
		}
	}
}
func (m *Module) maintenance(ctx context.Context) {
	defer m.wg.Done()
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
		m.mu.Lock()
		var ready []Job
		for _, j := range m.jobs {
			if j.dirty {
				j.dirty = m.save(j) != nil
			}
			if !j.dirty && !j.Published && (j.State == "complete" || j.State == "failed" || j.State == "uncertain") {
				ready = append(ready, *j)
			}
		}
		m.mu.Unlock()
		for _, j := range ready {
			request, cancel := context.WithTimeout(ctx, 2*time.Second)
			_, e := m.host.PublishOperational(request, j.Event)
			cancel()
			if e == nil {
				m.mu.Lock()
				if current := m.jobs[j.ID]; current != nil {
					current.Published = true
					current.dirty = m.save(current) != nil
				}
				m.mu.Unlock()
			}
		}
		if m.Config.Schedule {
			m.schedule()
		}
		m.signal()
	}
}
func (m *Module) schedule() {
	now := time.Now().UTC()
	for _, s := range m.host.Servers() {
		if s.Helper == nil {
			continue
		}
		m.mu.Lock()
		due := m.next[s.ID]
		m.mu.Unlock()
		if due.After(now) {
			continue
		}
		if due.IsZero() {
			due = now
			m.mu.Lock()
			m.next[s.ID] = due
			e := storeio.Save(m.Root, "schedule.json", m.next)
			if e != nil {
				delete(m.next, s.ID)
			}
			m.mu.Unlock()
			if e != nil {
				continue
			}
		}
		r := Request{Operation: "create", ServerID: s.ID, RequestID: "scheduled-" + strings.ReplaceAll(due.Format("20060102T150405.000000000"), ".", "-"), Mode: m.Config.Mode}
		if _, e := m.submit(r); e != nil {
			continue
		}
		m.mu.Lock()
		m.next[s.ID] = due.Add(time.Duration(m.Config.IntervalSeconds) * time.Second)
		if m.next[s.ID].Before(now) {
			m.next[s.ID] = now.Add(time.Duration(m.Config.IntervalSeconds) * time.Second)
		}
		if e := storeio.Save(m.Root, "schedule.json", m.next); e != nil {
			m.next[s.ID] = due
			m.health = node.Health{Status: "degraded", Explanation: "backup schedule persistence unavailable"}
		}
		m.mu.Unlock()
	}
}
