// SPDX-License-Identifier: GPL-3.0-or-later
package node

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/s2script/s2script-cs2-egg/internal/alerts"
	"github.com/s2script/s2script-cs2-egg/internal/events"
	"github.com/s2script/s2script-cs2-egg/internal/privateops"
	"github.com/s2script/s2script-cs2-egg/internal/protocol"
	"github.com/s2script/s2script-cs2-egg/internal/volumehelper"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type operationalSink struct{ receiptSink }

func (s *operationalSink) Accepts(events.Event) bool { return true }
func newDiskCore(t *testing.T, s events.Sink) *Core {
	t.Helper()
	c, e := New(fixture(t), s)
	if e != nil {
		t.Fatal(e)
	}
	if e = c.openState(); e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { c.lock.Close(); c.stateDir.Close() })
	return c
}
func TestHostEventsDoNotClaimRuntimeOwners(t *testing.T) {
	c := newDiskCore(t, &operationalSink{})
	r := events.OperationalEvent{ServerID: "a", ActionID: "backup-1", EventKey: "complete", Type: "backup", Severity: "info", At: time.Now().UTC(), Details: map[string]string{"outcome": "done"}}
	a, e := c.publishOperational(context.Background(), "backup", r)
	if e != nil {
		t.Fatal(e)
	}
	if a.Result != "acknowledged" || len(c.state.Owners) != 0 {
		t.Fatal(a, c.state.Owners)
	}
	b, e := c.publishOperational(context.Background(), "backup", r)
	if e != nil || a.EventID != b.EventID || b.Result != "duplicate" {
		t.Fatal(b, e)
	}
	r.Details["outcome"] = "changed"
	if _, e = c.publishOperational(context.Background(), "backup", r); e == nil {
		t.Fatal("conflict accepted")
	}
	forged := event("forged")
	forged.Origin = "host"
	if _, e = c.admit("a", forged); e == nil {
		t.Fatal("container forged host origin")
	}
}
func TestArchiveRetainsOwnersAndTombstones(t *testing.T) {
	s := &receiptSink{}
	c := newDiskCore(t, s)
	if _, e := c.claim("a", "run"); e != nil {
		t.Fatal(e)
	}
	v := event("one")
	if _, e := c.admit("a", v); e != nil {
		t.Fatal(e)
	}
	c.deliverPending(context.Background())
	if _, e := c.Archive(context.Background()); e != nil {
		t.Fatal(e)
	}
	if len(c.state.Owners) != 0 || len(c.state.Records) != 0 {
		t.Fatal("hot quota not freed")
	}
	c.delivery = nil
	if owner, e := c.claim("a", "run"); e != nil || owner != "node" {
		t.Fatal(owner, e)
	}
	c.delivery = s
	if _, e := c.admit("a", v); e != nil {
		t.Fatal(e)
	}
	if len(c.state.Records) != 0 {
		t.Fatal("replay enqueued")
	}
	if _, e := c.admit("a", event("late")); e != nil {
		t.Fatal("archival incorrectly finalized", e)
	}
	for _, seg := range c.state.Archives {
		os.Remove(filepath.Join(c.cfg.StateDir, seg.Name))
		break
	}
	if _, e := c.claim("a", "new"); e == nil {
		t.Fatal("missing archive permitted new sender")
	}
}
func TestObserverIndependentOfSelectionAndBounded(t *testing.T) {
	c := newDiskCore(t, nil)
	sub, e := c.Subscribe(1)
	if e != nil {
		t.Fatal(e)
	}
	defer sub.Close()
	c.claim("a", "run")
	v := event("console")
	v.Type = "server.console"
	if _, e = c.admit("a", v); e != nil {
		t.Fatal(e)
	}
	o := <-sub.Events
	if o.Source != "container_report" || o.Server.ID != "a" || o.Event.ServerID != "a" {
		t.Fatal(o)
	}
	c.admit("a", v)
	c.admit("a", v)
	if sub.Dropped() == 0 {
		t.Fatal("unbounded observer")
	}
	o = <-sub.Events
	if o.Replay != "window_duplicate" {
		t.Fatal(o.Replay)
	}
}

var _ alerts.DeliveryResult

func TestFinalizationRejectsNovelEventsAndKeepsReplay(t *testing.T) {
	c := newDiskCore(t, &receiptSink{})
	c.claim("a", "run")
	v := event("old")
	c.admit("a", v)
	f, e := c.beginFence("a", "run")
	if e != nil {
		t.Fatal(e)
	}
	if e = c.finishFence("a", "run", f); e != nil {
		t.Fatal(e)
	}
	if _, e = c.admit("a", v); e != nil {
		t.Fatal(e)
	}
	if _, e = c.admit("a", event("late")); e == nil {
		t.Fatal("novel finalized event accepted")
	}
	if _, e = c.Archive(context.Background()); e != nil {
		t.Fatal(e)
	}
	if _, e = c.admit("a", event("later")); e == nil {
		t.Fatal("archive resurrected finalized owner")
	}
}
func TestFinalizationFenceInvalidatedByClaimAndFilteredActivity(t *testing.T) {
	c := newDiskCore(t, nil)
	c.claim("a", "run")
	f, e := c.beginFence("a", "run")
	if e != nil {
		t.Fatal(e)
	}
	c.claim("a", "run")
	if e = c.finishFence("a", "run", f); e == nil {
		t.Fatal("new claim did not fence delayed receipt")
	}
	f, e = c.beginFence("a", "run")
	if e != nil {
		t.Fatal(e)
	}
	v := event("raw")
	v.Type = "server.console"
	c.admit("a", v)
	if e = c.finishFence("a", "run", f); e == nil {
		t.Fatal("filtered activity did not fence receipt")
	}
}

func TestMigrationCannotBypassLiveDaemonLock(t *testing.T) {
	c := newDiskCore(t, nil)
	legacy := c.state
	legacy.Version = 1
	b, _ := json.Marshal(legacy)
	os.WriteFile(filepath.Join(c.cfg.StateDir, "delivery.json"), b, 0600)
	other, e := New(c.cfg, nil)
	if e != nil {
		t.Fatal(e)
	}
	defer func() {
		if other.lock != nil {
			other.lock.Close()
		}
		if other.stateDir != nil {
			other.stateDir.Close()
		}
	}()
	if e = other.MigrateDelivery(); e == nil {
		t.Fatal("migration bypassed daemon lock")
	}
	got, _ := os.ReadFile(filepath.Join(c.cfg.StateDir, "delivery.json"))
	if string(got) != string(b) {
		t.Fatal("live state mutated")
	}
}

func TestArchiveCountsUnpublishedSegments(t *testing.T) {
	c := newDiskCore(t, nil)
	c.claim("a", "run")
	for i := 0; i < 64; i++ {
		name := fmt.Sprintf("delivery-archive-%032x.json", i)
		os.WriteFile(filepath.Join(c.cfg.StateDir, name), []byte("orphan"), 0600)
	}
	if _, e := c.Archive(context.Background()); e == nil {
		t.Fatal("unpublished segments bypassed disk quota")
	}
}
func TestExplicitMigrationPreservesLegacyDigestAndPrivateCopy(t *testing.T) {
	c := newDiskCore(t, &receiptSink{})
	c.claim("a", "run")
	v := event("before")
	c.admit("a", v)
	legacy := c.state
	legacy.Version = 1
	legacy.Meta = nil
	b, _ := json.Marshal(legacy)
	c.lock.Close()
	c.stateDir.Close()
	os.WriteFile(filepath.Join(c.cfg.StateDir, "delivery.json"), b, 0600)
	other, e := New(c.cfg, &receiptSink{})
	if e != nil {
		t.Fatal(e)
	}
	if e = other.MigrateDelivery(); e != nil {
		t.Fatal(e)
	}
	defer func() {
		if other.lock != nil {
			other.lock.Close()
		}
		if other.stateDir != nil {
			other.stateDir.Close()
		}
	}()
	copy, e := os.ReadFile(filepath.Join(c.cfg.StateDir, "delivery-before-v2.json"))
	if e != nil || string(copy) != string(b) {
		t.Fatal("legacy copy differs", e)
	}
	if old := other.state.Records[key("a", "run", "before")]; old.Digest != legacy.Records[key("a", "run", "before")].Digest {
		t.Fatal("legacy digest changed")
	}
}

func TestObserverIngressAndStatusRedaction(t *testing.T) {
	c := newDiskCore(t, &operationalSink{})
	c.cfg.Webhook.URL = "https://example.invalid/hooks/private-token"
	c.claim("a", "run")
	sub, e := c.Subscribe(8)
	if e != nil {
		t.Fatal(e)
	}
	defer sub.Close()
	v := event("local")
	v.ServerID = "a"
	if e = c.Publish(context.Background(), v); e != nil {
		t.Fatal(e)
	}
	if got := (<-sub.Events).Source; got != "local_runtime" {
		t.Fatal(got)
	}
	s := c.statuses["a"]
	s.Map = "private-token"
	s.Versions = map[string]string{"cs2": "private-token"}
	c.statuses["a"] = s
	b, _ := json.Marshal(c.Status("a"))
	if strings.Contains(string(b), "private-token") {
		t.Fatal(string(b))
	}
}

func TestKnownSecretsInIdentifiersRejectedBeforeObservationOrPersistence(t *testing.T) {
	c := newDiskCore(t, &operationalSink{})
	c.cfg.Webhook.URL = "https://example.invalid/hooks/private-token"
	sub, _ := c.Subscribe(8)
	defer sub.Close()
	if _, e := c.claim("a", "private-token"); e == nil {
		t.Fatal("secret run accepted")
	}
	c.claim("a", "run")
	v := event("private-token")
	if _, e := c.admit("a", v); e == nil {
		t.Fatal("secret event identifier accepted")
	}
	r := events.OperationalEvent{ServerID: "a", ActionID: "private-token", EventKey: "done", Type: "backup", Severity: "info", At: time.Now()}
	if _, e := c.publishOperational(context.Background(), "backup", r); e == nil {
		t.Fatal("secret action accepted")
	}
	if len(sub.Events) != 0 {
		t.Fatal("secret identity observed")
	}
	b, _ := json.Marshal(c.state)
	if strings.Contains(string(b), "private-token") {
		t.Fatal("secret identity persisted")
	}
	s := c.statuses["a"]
	s.RunID = "private-token"
	s.ScheduledActionID = "private-token"
	c.statuses["a"] = s
	b, _ = json.Marshal(c.Status("a"))
	if strings.Contains(string(b), "private-token") {
		t.Fatal("historic secret status exposed")
	}
}

func TestHelperLossRetainsCommittedFinalizationFence(t *testing.T) {
	c := newDiskCore(t, &operationalSink{})
	c.claim("a", "run")
	base, e := os.MkdirTemp("", "s2fence-")
	if e != nil {
		t.Fatal(e)
	}
	defer os.RemoveAll(base)
	socket := filepath.Join(base, "s")
	l, e := net.Listen("unix", socket)
	if e != nil {
		t.Fatal(e)
	}
	defer l.Close()
	srv := c.servers["a"]
	srv.Helper = &PrivateHelper{Socket: socket, UID: uint32(os.Geteuid())}
	c.servers["a"] = srv
	done := make(chan struct{})
	go func() {
		defer close(done)
		conn, e := l.Accept()
		if e != nil {
			return
		}
		defer conn.Close()
		msg, e := protocol.Read(conn)
		if e != nil {
			return
		}
		var r privateops.Request
		protocol.Decode(msg.Payload, &r)
		w := volumehelper.Session{Conn: conn, Family: "operations"}
		w.Write("result", privateops.Reply{Nonce: r.Nonce})
		w.Read("check", &struct{}{})
		w.Write("held", privateops.Reply{Nonce: r.Nonce})
		w.Read("check", &struct{}{}) /* lose helper after commit, before held reply */
	}()
	err := c.FinalizeRun(context.Background(), "a", "run")
	<-done
	if !errors.Is(err, privateops.ErrStoppedUncertain) {
		t.Fatal(err)
	}
	owner, meta, _, _, e := c.lookup(key("a", "run"), "")
	if e != nil || !meta.Finalized || owner != "node" {
		t.Fatal("uncertainty rolled back fence/owner", owner, meta, e)
	}
	if _, e = c.admit("a", event("novel")); e == nil {
		t.Fatal("uncertain finalization reopened sender")
	}
}
