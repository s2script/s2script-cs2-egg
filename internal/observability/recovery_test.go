// SPDX-License-Identifier: GPL-3.0-or-later
package observability

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/s2script/s2script-cs2-egg/internal/cleanup"
	"github.com/s2script/s2script-cs2-egg/internal/events"
	node "github.com/s2script/s2script-cs2-egg/internal/nodetypes"
	"github.com/s2script/s2script-cs2-egg/internal/protocol"
	"github.com/s2script/s2script-cs2-egg/internal/server"
	"github.com/s2script/s2script-cs2-egg/internal/storeio"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type recoveryHost struct{ seen []events.OperationalEvent }

func (h *recoveryHost) Servers() []node.Server                                     { return []node.Server{{ID: "a"}} }
func (h *recoveryHost) Status(string) server.Status                                { return server.Status{} }
func (h *recoveryHost) ModuleHealth() map[string]node.Health                       { return nil }
func (h *recoveryHost) Subscribe(int) (*node.Subscription, error)                  { return nil, nil }
func (h *recoveryHost) Publish(context.Context, events.Event) error                { return nil }
func (h *recoveryHost) Dispatch(context.Context, string, string, any) (any, error) { return nil, nil }
func (h *recoveryHost) PublishOperational(_ context.Context, e events.OperationalEvent) (events.Admission, error) {
	h.seen = append(h.seen, e)
	return events.Admission{Result: "acknowledged"}, nil
}
func recoveryFixture(t *testing.T) (*Module, protocol.OperationsRequest, string) {
	t.Helper()
	root := t.TempDir()
	os.Mkdir(filepath.Join(root, "outcomes"), 0700)
	art := filepath.Join(root, "a", "logs")
	if e := cleanup.Initialize(art, "logs"); e != nil {
		t.Fatal(e)
	}
	file := filepath.Join(art, "old.log")
	os.WriteFile(file, []byte("original"), 0600)
	cleanup.Seal(art, "old.log")
	plan, e := cleanup.PreviewPlan(context.Background(), art, []cleanup.Rule{{Pattern: "*"}})
	if e != nil {
		t.Fatal(e)
	}
	return &Module{Root: root, host: &recoveryHost{}}, protocol.OperationsRequest{Operation: "cleanup-apply", ServerID: "a", Namespace: "logs", ID: plan.ID, RequestID: "stable"}, file
}
func TestOutcomeCapacityReservedBeforeCleanup(t *testing.T) {
	m, r, file := recoveryFixture(t)
	for i := 0; i < 1024; i++ {
		os.WriteFile(filepath.Join(m.Root, "outcomes", fmt.Sprintf("%064d.json", i)), []byte("{}"), 0600)
	}
	b, _ := json.Marshal(r)
	if _, e := m.execute(context.Background(), "a", b); e == nil {
		t.Fatal("quota admitted mutation")
	}
	if _, e := os.Stat(file); e != nil {
		t.Fatal("cleanup executed before reserving outcome capacity")
	}
}
func TestCompletionPersistenceFailureRecoversWithoutReplay(t *testing.T) {
	m, r, file := recoveryFixture(t)
	fail := true
	m.writeOutcome = func(root, name string, v any) error {
		o := v.(outcome)
		if o.Phase == "terminal" && fail {
			return errors.New("fixture write failure")
		}
		return storeio.Save(root, name, v)
	}
	b, _ := json.Marshal(r)
	m.execute(context.Background(), "a", b)
	if _, e := os.Stat(file); !errors.Is(e, os.ErrNotExist) {
		t.Fatal("cleanup did not execute")
	}
	os.WriteFile(file, []byte("replacement"), 0600)
	fail = false
	m.flush(context.Background())
	if got, _ := os.ReadFile(file); string(got) != "replacement" {
		t.Fatal("work replayed")
	}
	h := m.host.(*recoveryHost)
	if len(h.seen) != 1 || h.seen[0].Details["outcome"] != "completed" {
		t.Fatal(h.seen)
	}
	m.flush(context.Background())
	if len(h.seen) != 1 {
		t.Fatal("publication repeated after receipt")
	}
}
func TestRestartRecoversPlanResultAndFailureOutcome(t *testing.T) {
	m, r, file := recoveryFixture(t)
	m.writeOutcome = func(root, name string, v any) error {
		if v.(outcome).Phase == "terminal" {
			return errors.New("fixture crash before completion")
		}
		return storeio.Save(root, name, v)
	}
	b, _ := json.Marshal(r)
	m.execute(context.Background(), "a", b)
	os.WriteFile(file, []byte("replacement"), 0600)
	restarted := &Module{Root: m.Root, host: &recoveryHost{}}
	restarted.flush(context.Background())
	h := restarted.host.(*recoveryHost)
	if len(h.seen) != 1 || h.seen[0].Details["outcome"] != "completed" {
		t.Fatal(h.seen)
	}
	if got, _ := os.ReadFile(file); string(got) != "replacement" {
		t.Fatal("replayed deletion")
	}
	r.RequestID = "failed"
	r.ID = "missing"
	b, _ = json.Marshal(r)
	restarted.execute(context.Background(), "a", b)
	restarted.flush(context.Background())
	if len(h.seen) != 2 || h.seen[1].Severity != "error" {
		t.Fatal("failure outcome missing", h.seen)
	}
}

func TestInterruptedCleanupWithoutReceiptIsUncertainAndNeverReplayed(t *testing.T) {
	m, r, file := recoveryFixture(t)
	key := storeio.Hash([]byte("a\x00" + r.RequestID))
	o := outcome{Phase: "executing", Input: r, Event: events.OperationalEvent{ServerID: "a", ActionID: key, EventKey: "complete", Type: "cleanup", Severity: "info", At: time.Now()}}
	if e := m.saveOutcome(key, o); e != nil {
		t.Fatal(e)
	}
	m.flush(context.Background())
	h := m.host.(*recoveryHost)
	if len(h.seen) != 1 || !strings.Contains(h.seen[0].Details["error"], "uncertain") {
		t.Fatal(h.seen)
	}
	if _, e := os.Stat(file); e != nil {
		t.Fatal("uncertain work replayed")
	}
}
func TestSecretRequestNeverBecomesOutcomeInput(t *testing.T) {
	m, r, _ := recoveryFixture(t)
	m.Secrets = []string{"private-token"}
	r.RequestID = "private-token"
	b, _ := json.Marshal(r)
	if _, e := m.execute(context.Background(), "a", b); e == nil {
		t.Fatal("secret identifier accepted")
	}
	files, _ := os.ReadDir(filepath.Join(m.Root, "outcomes"))
	if len(files) != 0 {
		t.Fatal("secret request persisted")
	}
}
