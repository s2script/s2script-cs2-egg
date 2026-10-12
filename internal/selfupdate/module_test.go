// SPDX-License-Identifier: GPL-3.0-or-later
package selfupdate

import (
	"context"
	"errors"
	"github.com/s2script/s2script-cs2-egg/internal/config"
	"github.com/s2script/s2script-cs2-egg/internal/events"
	node "github.com/s2script/s2script-cs2-egg/internal/nodetypes"
	"github.com/s2script/s2script-cs2-egg/internal/server"
	"github.com/s2script/s2script-cs2-egg/internal/storeio"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type updateHost struct{}

func (updateHost) Servers() []node.Server               { return nil }
func (updateHost) ModuleHealth() map[string]node.Health { return nil }
func (updateHost) Status(string) server.Status          { return server.Status{} }
func (updateHost) Publish(context.Context, events.Event) error {
	return errors.New("runtime event forbidden")
}
func (updateHost) PublishOperational(context.Context, events.OperationalEvent) (events.Admission, error) {
	return events.Admission{}, errors.New("offline fixture")
}
func (updateHost) Subscribe(int) (*node.Subscription, error) { return nil, errors.New("unused") }
func (updateHost) Dispatch(context.Context, string, string, any) (any, error) {
	return nil, errors.New("unexpected action")
}
func TestUpdateJobNeverReplaysAfterInterruption(t *testing.T) {
	manager := fixtureManager(t, "binary")
	root := t.TempDir()
	id := strings.Repeat("a", 64)
	if err := storeio.Save(root, id+".json", Record{ID: id, Operation: "activate", State: "running"}); err != nil {
		t.Fatal(err)
	}
	m := &Module{Root: root, Config: config.ProjectUpdates{Enabled: true}, Manager: manager}
	if err := m.Start(context.Background(), updateHost{}); err != nil {
		t.Fatal(err)
	}
	defer m.Close(context.Background())
	m.mu.Lock()
	r := *m.records[id]
	queued := len(m.queue)
	m.mu.Unlock()
	if r.State != "uncertain" || queued != 0 || r.Event.ActionID != id {
		t.Fatal("interrupted mutation replayed", r, queued)
	}
}
func TestPostReadinessActivationOutcomeIsCapturedOnce(t *testing.T) {
	manager, j := trialFixture(t)
	m := &Module{Root: t.TempDir(), Manager: manager, records: map[string]*Record{}}
	if err := m.captureOutcome(); err != nil {
		t.Fatal(err)
	}
	if len(m.records) != 0 {
		t.Fatal("pending treated as activation success")
	}
	j.Current = j.Candidate
	j.CurrentSHA256 = j.CandidateSHA256
	j.Candidate = ""
	j.CandidateSHA256 = ""
	j.Phase = "good"
	j.Outcome = "activated"
	j.At = time.Now().UTC()
	if err := manager.save(j); err != nil {
		t.Fatal(err)
	}
	if err := m.captureOutcome(); err != nil {
		t.Fatal(err)
	}
	for _, r := range m.records {
		if !r.Event.At.Equal(j.At) || r.Event.ActionID != j.Transaction {
			t.Fatal("outcome lost durable identity", r)
		}
		before, err := storeio.Read(m.Root, filepath.Base(r.ID+".json"), 1<<20)
		if err != nil {
			t.Fatal(err)
		}
		m.captureOutcome()
		after, _ := storeio.Read(m.Root, r.ID+".json", 1<<20)
		if string(before) != string(after) {
			t.Fatal("outcome rebuilt instead of frozen")
		}
	}
	if len(m.records) != 1 {
		t.Fatal("activation outcome missing or duplicated")
	}
}
