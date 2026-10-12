// SPDX-License-Identifier: GPL-3.0-or-later
package backup

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/s2script/s2script-cs2-egg/internal/events"
	node "github.com/s2script/s2script-cs2-egg/internal/nodetypes"
	"github.com/s2script/s2script-cs2-egg/internal/protocol"
	"github.com/s2script/s2script-cs2-egg/internal/server"
	"github.com/s2script/s2script-cs2-egg/internal/storeio"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type backupHost struct {
	srv    node.Server
	mu     sync.Mutex
	events []events.OperationalEvent
}

func (h *backupHost) Servers() []node.Server               { return []node.Server{h.srv} }
func (h *backupHost) ModuleHealth() map[string]node.Health { return nil }
func (h *backupHost) PublishOperational(_ context.Context, e events.OperationalEvent) (events.Admission, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.events = append(h.events, e)
	return events.Admission{}, nil
}
func (h *backupHost) Subscribe(int) (*node.Subscription, error) { return nil, errors.New("unused") }
func (h *backupHost) Publish(context.Context, events.Event) error {
	return errors.New("runtime ownership path must not be used")
}
func (h *backupHost) Status(string) server.Status { return server.Status{} }
func (h *backupHost) Dispatch(context.Context, string, string, any) (any, error) {
	return nil, errors.New("no lifecycle changes allowed")
}
func moduleFixture(t *testing.T) (*Store, *Module, *backupHost) {
	t.Helper()
	s := fixture(t)
	put(t, s, "egg/server.json", "secret fixture contents")
	socket := filepath.Join(t.TempDir(), "h.sock")
	l, e := net.Listen("unix", socket)
	if e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			c, e := l.Accept()
			if e != nil {
				return
			}
			first, e := protocol.Read(c)
			if e == nil {
				Serve(ctx, c, s.volume, s.policy, first)
			}
			c.Close()
		}
	}()
	h := &backupHost{srv: node.Server{ID: "server-a", Volume: s.volume, Helper: &node.PrivateHelper{Socket: socket, UID: uint32(os.Geteuid())}}}
	m := &Module{Root: t.TempDir()}
	if e = m.Start(ctx, h); e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { cancel(); l.Close(); m.Close(context.Background()); <-done })
	return s, m, h
}
func awaitJob(t *testing.T, m *Module, id string) Job {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		m.mu.Lock()
		j := *m.jobs[id]
		m.mu.Unlock()
		if j.State != "pending" && j.State != "running" {
			return j
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("job did not complete")
	return Job{}
}
func TestBackupJobPublishesFrozenHostOutcomeWithoutArchiveContents(t *testing.T) {
	s, m, h := moduleFixture(t)
	j, e := m.submit(Request{ServerID: "server-a", Operation: "create", RequestID: "fixture"})
	if e != nil {
		t.Fatal(e)
	}
	j = awaitJob(t, m, j.ID)
	if j.State != "complete" || len(j.Result.Entries) != 1 {
		t.Fatalf("%+v", j)
	}
	if _, e = s.manifest(j.Result.Entries[0].ID); e != nil {
		t.Fatal(e)
	}
	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		h.mu.Lock()
		n := len(h.events)
		h.mu.Unlock()
		if n > 0 {
			break
		}
		time.Sleep(time.Millisecond)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.events) == 0 || h.events[0].At != j.Event.At || h.events[0].Type != "backup" || h.events[0].ActionID != j.ID {
		t.Fatal("missing stable host outcome", h.events)
	}
	b, _ := json.Marshal(h.events)
	if strings.Contains(string(b), "secret fixture") {
		t.Fatal("archive contents leaked")
	}
}
func TestNodeRestartQueriesRunningBackupReceiptWithoutReexecution(t *testing.T) {
	s, m, h := moduleFixture(t)
	r := Request{ServerID: "server-a", Operation: "create", RequestID: "already-started"}
	reply, e := s.Handle(context.Background(), r)
	if e != nil || reply.State != "complete" {
		t.Fatalf("%+v %v", reply, e)
	}
	key := storeio.Hash([]byte("server-a\x00already-started"))
	j := Job{ID: key, Server: "server-a", State: "running", Request: r, CreatedAt: time.Now().UTC()}
	if e = m.Close(context.Background()); e != nil {
		t.Fatal(e)
	}
	if e = storeio.Save(filepath.Join(m.Root, "jobs"), key+".json", j); e != nil {
		t.Fatal(e)
	}
	restarted := &Module{Root: m.Root}
	if e = restarted.Start(context.Background(), h); e != nil {
		t.Fatal(e)
	}
	defer restarted.Close(context.Background())
	result := awaitJob(t, restarted, key)
	if result.State != "complete" {
		t.Fatalf("%+v", result)
	}
	entries, e := s.List()
	if e != nil || len(entries) != 1 {
		t.Fatal("backup repeated after restart", entries, e)
	}
}
