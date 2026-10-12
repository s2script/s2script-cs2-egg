// SPDX-License-Identifier: GPL-3.0-or-later
package workshop

import (
	"context"
	"errors"
	"github.com/s2script/s2script-cs2-egg/internal/artifact"
	node "github.com/s2script/s2script-cs2-egg/internal/nodetypes"
	"github.com/s2script/s2script-cs2-egg/internal/protocol"
	"github.com/s2script/s2script-cs2-egg/internal/runtimelease"
	"github.com/s2script/s2script-cs2-egg/internal/storeio"
	"github.com/s2script/s2script-cs2-egg/internal/volumehelper"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func startStore(t *testing.T, s *Store) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	if e := s.Start(ctx); e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { cancel(); s.jobs.Close() })
}
func prepareFixture(t *testing.T, s *Store, srv node.Server) (Result, error) {
	t.Helper()
	release, e := runtimelease.Acquire(srv.Volume)
	if e != nil {
		t.Fatal(e)
	}
	defer release()
	unlock, e := artifact.Acquire(srv.Volume)
	if e != nil {
		t.Fatal(e)
	}
	defer unlock()
	return PrepareLocked(context.Background(), LocalOptions{Root: srv.Volume, Items: []string{"123"}, Mode: "copy", Wait: 2 * time.Second}, func(ctx context.Context, r protocol.WorkshopRequest) (protocol.WorkshopResult, error) {
		if u, e := runtimelease.Acquire(srv.Volume); e == nil {
			u()
			t.Error("boot lost runtime lease")
		}
		return s.Handle(ctx, srv.ID, r)
	})
}
func TestTwoServersKeepPrivateBookkeepingAndReferencedGenerations(t *testing.T) {
	s, a := fixture(t)
	b := node.Server{ID: "consumer", Volume: t.TempDir()}
	s.o.Servers = append(s.o.Servers, b)
	g, e := s.Import(context.Background(), a.ID, "123", "55", sampleVPK())
	if e != nil {
		t.Fatal(e)
	}
	startStore(t, s)
	for _, srv := range []node.Server{a, b} {
		if _, e = prepareFixture(t, s, srv); e != nil {
			t.Fatal(e)
		}
		got, e := os.ReadFile(filepath.Join(srv.Volume, ContentPath, "123", "123_000.vpk"))
		if e != nil || string(got) != "data" {
			t.Fatal("missing delivered chunk", e)
		}
	}
	x, _ := os.Stat(filepath.Join(a.Volume, ACFPath))
	y, _ := os.Stat(filepath.Join(b.Volume, ACFPath))
	if os.SameFile(x, y) || x.Mode().Perm() != 0600 || y.Mode().Perm() != 0600 {
		t.Fatal("private ACF shared or mode widened")
	}
	s.o.Details = func(context.Context, []string) (map[string]Detail, error) {
		return map[string]Detail{"123": {ID: "123", Revision: "56", AppID: 730}}, nil
	}
	g2, e := s.Import(context.Background(), a.ID, "123", "56", sampleVPK())
	if e != nil || g.Key() == g2.Key() {
		t.Fatal("revision generation not distinct", e)
	}
	if _, e = s.Prune(context.Background()); e != nil {
		t.Fatal(e)
	}
	if _, e = s.Generation("123", "55"); e != nil {
		t.Fatal("referenced old generation pruned", e)
	}
	if _, e = prepareFixture(t, s, b); e != nil {
		t.Fatal(e)
	}
}
func TestMissingChunkIsRejectedByVPKValidation(t *testing.T) {
	s, a := fixture(t)
	f := sampleVPK()
	delete(f, "123_000.vpk")
	_, e := s.Import(context.Background(), a.ID, "123", "55", f)
	if e == nil || !strings.Contains(e.Error(), "chunk") {
		t.Fatal("missing chunk not caught by structural validation", e)
	}
}
func TestDuplicateACFAndUntrustedImporterRejected(t *testing.T) {
	if _, e := ParseACF([]byte(`"AppWorkshop" { "appid" "730" "appid" "730" }`)); e == nil {
		t.Fatal("duplicate accepted")
	}
	s, _ := fixture(t)
	if _, e := s.Begin(context.Background(), "follower", "123", "55", "request"); e == nil {
		t.Fatal("untrusted elected")
	}
	if _, e := s.Import(context.Background(), "follower", "123", "55", sampleVPK()); e == nil {
		t.Fatal("untrusted published")
	}
}
func TestRepeatedBootDoesNotUploadSealedGeneration(t *testing.T) {
	s, a := fixture(t)
	startStore(t, s)
	if _, e := s.Import(context.Background(), a.ID, "123", "55", sampleVPK()); e != nil {
		t.Fatal(e)
	}
	for range 3 {
		if _, e := prepareFixture(t, s, a); e != nil {
			t.Fatal(e)
		}
	}
}
func TestPrivateHelperRefusesHeldLease(t *testing.T) {
	s, a := fixture(t)
	startStore(t, s)
	socket := filepath.Join(t.TempDir(), "helper.sock")
	listener, e := net.Listen("unix", socket)
	if e != nil {
		t.Fatal(e)
	}
	defer listener.Close()
	a.Helper = &node.PrivateHelper{Socket: socket, UID: uint32(os.Getuid()), GID: os.Getgid()}
	done := make(chan error, 1)
	go func() {
		c, e := listener.Accept()
		if e != nil {
			done <- e
			return
		}
		defer c.Close()
		m, e := volumehelper.Read(c)
		if e == nil && m.Type == "begin-workshop" {
			e = ServePrivate(context.Background(), c, LocalOptions{Root: a.Volume, Items: []string{"123"}})
		}
		if e != nil {
			_ = volumehelper.Write(c, "result", struct{ Error string }{e.Error()})
		}
		done <- nil
	}()
	release, e := runtimelease.Acquire(a.Volume)
	if e != nil {
		t.Fatal(e)
	}
	defer release()
	if _, e = s.Sync(context.Background(), a); e == nil || !strings.Contains(e.Error(), "lease") {
		t.Fatal("held lease not refused", e)
	}
	if e = <-done; e != nil {
		t.Fatal(e)
	}
}
func TestInterruptedUploadNeverPublishedAndCannotChangeSeal(t *testing.T) {
	s, a := fixture(t)
	startStore(t, s)
	files := sampleVPK()
	var wf []protocol.WorkshopFile
	for _, name := range []string{"123_000.vpk", "123_dir.vpk"} {
		wf = append(wf, protocol.WorkshopFile{Name: name, Size: int64(len(files[name])), SHA256: storeio.Hash(files[name])})
	}
	r, e := await(context.Background(), func(c context.Context, r protocol.WorkshopRequest) (protocol.WorkshopResult, error) {
		return s.Handle(c, a.ID, r)
	}, protocol.WorkshopRequest{Operation: "begin", RequestID: "begin", ItemID: "123", Revision: "55", Files: wf})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.Generation("123", "55"); !errors.Is(e, os.ErrNotExist) {
		t.Fatal("partial published")
	}
	if e = s.chunk(context.Background(), a.ID, protocol.WorkshopRequest{Token: r.Token, File: "123_000.vpk", Data: files["123_000.vpk"]}); e != nil {
		t.Fatal(e)
	}
	if _, e = s.seal(context.Background(), a.ID, protocol.WorkshopRequest{Token: r.Token, ItemID: "123", Revision: "55"}); e == nil {
		t.Fatal("incomplete sealed")
	}
	if _, e = s.Generation("123", "55"); !errors.Is(e, os.ErrNotExist) {
		t.Fatal("failed seal published")
	}
}
func TestStoppedHelperCopiesBothChunks(t *testing.T) {
	s, a := fixture(t)
	if _, e := s.Import(context.Background(), a.ID, "123", "55", sampleVPK()); e != nil {
		t.Fatal(e)
	}
	startStore(t, s)
	socket := filepath.Join(t.TempDir(), "helper.sock")
	listener, e := net.Listen("unix", socket)
	if e != nil {
		t.Fatal(e)
	}
	defer listener.Close()
	a.Helper = &node.PrivateHelper{Socket: socket, UID: uint32(os.Getuid()), GID: os.Getgid()}
	done := make(chan error, 1)
	go func() {
		c, e := listener.Accept()
		if e != nil {
			done <- e
			return
		}
		defer c.Close()
		m, e := volumehelper.Read(c)
		if e == nil && m.Type == "begin-workshop" {
			e = ServePrivate(context.Background(), c, LocalOptions{Root: a.Volume, Items: []string{"123"}, Mode: "copy"})
		}
		if e != nil {
			_ = volumehelper.Write(c, "result", struct{ Error string }{e.Error()})
		}
		done <- e
	}()
	r, e := s.Sync(context.Background(), a)
	if e != nil {
		t.Fatal(e)
	}
	if len(r.Linked) != 1 {
		t.Fatal("no helper attachment")
	}
	if e = <-done; e != nil {
		t.Fatal(e)
	}
	if _, e = os.Stat(filepath.Join(a.Volume, ContentPath, "123", "123_000.vpk")); e != nil {
		t.Fatal(e)
	}
}
func TestMissingPublisherSignatureIsNotAssumed(t *testing.T) {
	s, a := fixture(t)
	files := sampleVPK()
	dir := files["123_dir.vpk"]
	files["123.vpk"] = dir
	delete(files, "123_dir.vpk")
	if _, e := s.Import(context.Background(), a.ID, "123", "55", files); e != nil {
		t.Fatal("non-dir VPK name rejected", e)
	}
}
func TestRecoveryCannotClaimMissingTarget(t *testing.T) {
	root := t.TempDir()
	g := Generation{AppID: 730, ItemID: "123", Revision: "55", Files: []File{{Name: "123.vpk", Size: 1, SHA256: storeio.Hash([]byte("x"))}}}
	g.Digest = manifestDigest(g)
	p := pending{Generation: g, Stage: "egg/.workshop-stage-" + storeio.ID(), Backup: "egg/.workshop-old-" + storeio.ID(), ACF: []byte(`"AppWorkshop" {"appid" "730"}`), ACFMode: 0600}
	if e := storeio.Save(root, "egg/workshop-pending.json", p); e != nil {
		t.Fatal(e)
	}
	if e := RecoverLocked(root); e == nil {
		t.Fatal("missing target accepted as recovered")
	}
}
func TestVPKRejectsMissingZeroLengthReferencedChunk(t *testing.T) {
	s, a := fixture(t)
	files := sampleVPK()
	data := files["123_dir.vpk"]
	for i := 0; i < 4; i++ {
		data[12+8+12+i] = 0
	}
	delete(files, "123_000.vpk")
	if _, e := s.Import(context.Background(), a.ID, "123", "55", files); e == nil {
		t.Fatal("missing referenced archive accepted")
	}
}
func TestReferenceFenceRetainsRenewal(t *testing.T) {
	s, a := fixture(t)
	g, e := s.Import(context.Background(), a.ID, "123", "55", sampleVPK())
	if e != nil {
		t.Fatal(e)
	}
	v, e := s.load()
	if e != nil {
		t.Fatal(e)
	}
	old := v.References[a.ID+":"+g.Key()]
	if _, e = s.Reserve(context.Background(), a.ID, "123", "55"); e != nil {
		t.Fatal(e)
	}
	latest, _ := s.load()
	if latest.References[a.ID+":"+g.Key()] == old {
		t.Fatal("reservation did not renew fence")
	}
}
func TestEmptyTrustListCannotElect(t *testing.T) {
	s, a := fixture(t)
	s.o.TrustedImporters = nil
	r, e := s.selectItem(context.Background(), a.ID, protocol.WorkshopRequest{ItemID: "123", RequestID: "request"})
	if e != nil {
		t.Fatal(e)
	}
	if r.State != "pending" || r.Error != "no_trusted_importer" {
		t.Fatal(r)
	}
}
func TestSelectedFollowerWaitIsBoundedAndActionable(t *testing.T) {
	s, a := fixture(t)
	s.o.TrustedImporters = nil
	startStore(t, s)
	start := time.Now()
	_, e := PrepareLocked(context.Background(), LocalOptions{Root: a.Volume, Items: []string{"123"}, Wait: 20 * time.Millisecond}, func(ctx context.Context, r protocol.WorkshopRequest) (protocol.WorkshopResult, error) {
		return s.Handle(ctx, a.ID, r)
	})
	if e == nil || !strings.Contains(e.Error(), "workshop pending item 123") {
		t.Fatal("pending was not actionable", e)
	}
	if time.Since(start) > time.Second {
		t.Fatal("startup wait was not bounded")
	}
}

func TestFollowerPendingBudgetAndNextStartupRefresh(t *testing.T) {
	s, a := fixture(t)
	b := node.Server{ID: "consumer", Volume: t.TempDir()}
	s.o.Servers = append(s.o.Servers, b)
	startStore(t, s)
	release, e := runtimelease.Acquire(b.Volume)
	if e != nil {
		t.Fatal(e)
	}
	defer release()
	unlock, e := artifact.Acquire(b.Volume)
	if e != nil {
		t.Fatal(e)
	}
	defer unlock()
	call := func(ctx context.Context, r protocol.WorkshopRequest) (protocol.WorkshopResult, error) {
		return s.Handle(ctx, b.ID, r)
	}
	opts := LocalOptions{Root: b.Volume, Items: []string{"123"}, Mode: "copy", Wait: 200 * time.Millisecond}
	started := time.Now()
	if _, e = PrepareLocked(context.Background(), opts, call); e == nil || !strings.Contains(e.Error(), "pending item 123") {
		t.Fatalf("expected actionable pending: %v", e)
	}
	if elapsed := time.Since(started); elapsed > 700*time.Millisecond {
		t.Errorf("selection budget overrun: %s", elapsed)
	}
	if _, e = s.Import(context.Background(), a.ID, "123", "55", sampleVPK()); e != nil {
		t.Fatal(e)
	}
	started = time.Now()
	if _, e = PrepareLocked(context.Background(), opts, call); e != nil {
		t.Fatalf("next startup replayed stale selection: %v", e)
	}
	if elapsed := time.Since(started); elapsed > 700*time.Millisecond {
		t.Errorf("next startup stalled on stale selection: %s", elapsed)
	}
	if _, e = os.Stat(filepath.Join(b.Volume, ContentPath, "123", "123_000.vpk")); e != nil {
		t.Fatal(e)
	}
}
