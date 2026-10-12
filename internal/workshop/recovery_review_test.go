// SPDX-License-Identifier: GPL-3.0-or-later
package workshop

import (
	"context"
	"errors"
	"fmt"
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
)

func seedCompleteItem(t *testing.T, root string) {
	t.Helper()
	var size int64
	for name, b := range sampleVPK() {
		if e := storeio.Write(root, filepath.Join(ContentPath, "123", name), b, 0600); e != nil {
			t.Fatal(e)
		}
		size += int64(len(b))
	}
	doc, e := ParseACF([]byte(`"AppWorkshop" { "appid" "730" }`))
	if e != nil {
		t.Fatal(e)
	}
	doc.SetItem("123", "55", size, 0)
	if e = storeio.Write(root, ACFPath, doc.Bytes(), 0600); e != nil {
		t.Fatal(e)
	}
}
func TestRestartAfterSuccessfulSeal(t *testing.T) {
	s, a := fixture(t)
	startStore(t, s)
	seedCompleteItem(t, a.Volume)
	sealHandle := ""
	interrupted := false
	call := func(ctx context.Context, r protocol.WorkshopRequest) (protocol.WorkshopResult, error) {
		out, e := s.Handle(ctx, a.ID, r)
		if r.Operation == "seal" {
			sealHandle = out.Handle
		}
		if e == nil && out.Generation != nil && (r.Operation == "seal" || r.Operation == "status" && r.Handle == sealHandle) {
			interrupted = true
			return out, errors.New("simulated process loss after seal publication")
		}
		return out, e
	}
	release, e := runtimelease.Acquire(a.Volume)
	if e != nil {
		t.Fatal(e)
	}
	unlock, e := artifact.Acquire(a.Volume)
	if e != nil {
		release()
		t.Fatal(e)
	}
	_, e = PrepareLocked(context.Background(), LocalOptions{Root: a.Volume, Items: []string{"123"}, Mode: "copy"}, call)
	unlock()
	release()
	if e == nil || !interrupted {
		t.Fatal("did not reach seal crash boundary", e)
	}
	if _, e = s.Generation("123", "55"); e != nil {
		t.Fatal("seal was not published", e)
	}
	// Restart the daemon pool as well as local preparation: recovery must use
	// persisted begin/seal results rather than only in-memory completion.
	s.jobs.Close()
	restarted, e := New(s.o)
	if e != nil {
		t.Fatal(e)
	}
	startStore(t, restarted)
	if _, e = prepareFixture(t, restarted, a); e != nil {
		t.Fatalf("restart cannot resume successfully sealed import: %v", e)
	}
}
func TestHardlinkStartupRequiresRevisionIdentity(t *testing.T) {
	s, a := fixture(t)
	g, e := s.Import(context.Background(), a.ID, "123", "55", sampleVPK())
	if e != nil {
		t.Fatal(e)
	}
	seedCompleteItem(t, a.Volume)
	if e = storeio.Save(a.Volume, "egg/workshop-state.json", localState{Version: 1, Items: map[string]Generation{"123": g}, Requests: map[string]string{}}); e != nil {
		t.Fatal(e)
	}
	g.Revision = "56"
	doc, _ := ParseACF([]byte(`"AppWorkshop" { "appid" "730" }`))
	e = attachLocked(context.Background(), LocalOptions{Root: a.Volume, Mode: "hardlink"}, doc, g, nil)
	if e == nil {
		t.Fatal("new revision incorrectly accepted without host hardlink installation; ACF and files remain revision 55")
	}
}

func TestMissingInventoryCannotRetireStoreLinks(t *testing.T) {
	s, a := fixture(t)
	g, e := s.Import(context.Background(), a.ID, "123", "55", sampleVPK())
	if e != nil {
		t.Fatal(e)
	}
	seedCompleteItem(t, a.Volume)
	for _, f := range g.Files {
		p := filepath.Join(a.Volume, ContentPath, "123", f.Name)
		if e = os.Remove(p); e != nil {
			t.Fatal(e)
		}
		if e = os.Symlink(filepath.Join(s.root, "generations", g.Key(), f.Name), p); e != nil {
			t.Fatal(e)
		}
	}
	// Inventory was lost and the item is no longer selected; source links still exist.
	s.o.Items = nil
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
		_, e = volumehelper.Read(c)
		if e == nil {
			e = ServePrivate(context.Background(), c, LocalOptions{Root: a.Volume, Mode: "copy"})
		}
		if e != nil {
			_ = volumehelper.Write(c, "result", struct{ Error string }{e.Error()})
		}
		done <- e
	}()
	_, syncErr := s.Sync(context.Background(), a)
	if syncErr != nil {
		t.Logf("safe sync refusal: %v", syncErr)
	}
	if e = <-done; e != nil && syncErr == nil {
		t.Fatal(e)
	}
	v, e := s.load()
	if e != nil {
		t.Fatal(e)
	}
	if v.Known[a.ID] {
		t.Fatal("missing inventory marked known despite surviving links")
	}
	if _, e = s.Reserve(context.Background(), a.ID, g.ItemID, g.Revision); e != nil {
		t.Fatal(e)
	}
	v, e = s.load()
	if e != nil {
		t.Fatal(e)
	}
	if v.Known[a.ID] {
		t.Fatal("a new pin cleared unknown inventory without reconstruction")
	}
	if _, ok := v.References[a.ID+":"+g.Key()]; !ok {
		t.Fatal("existing generation pin retired")
	}
	if _, e = s.Prune(context.Background()); e == nil {
		t.Fatal("unknown inventory did not block prune")
	}
	if _, e = os.Stat(filepath.Join(a.Volume, ContentPath, "123", g.Files[0].Name)); e != nil {
		t.Fatalf("missing inventory allowed live store references to be pruned: %v", e)
	}
}

func TestHardlinkStartupRejectsOrdinaryCopy(t *testing.T) {
	s, a := fixture(t)
	g, e := s.Import(context.Background(), a.ID, "123", "55", sampleVPK())
	if e != nil {
		t.Fatal(e)
	}
	seedCompleteItem(t, a.Volume)
	if e = storeio.Save(a.Volume, "egg/workshop-state.json", localState{Version: 1, Items: map[string]Generation{"123": g}, Requests: map[string]string{}}); e != nil {
		t.Fatal(e)
	}
	raw, e := storeio.Read(a.Volume, ACFPath, 4<<20)
	if e != nil {
		t.Fatal(e)
	}
	doc, e := ParseACF(raw)
	if e != nil {
		t.Fatal(e)
	}
	call := func(ctx context.Context, r protocol.WorkshopRequest) (protocol.WorkshopResult, error) {
		return s.Handle(ctx, a.ID, r)
	}
	if e = attachLocked(context.Background(), LocalOptions{Root: a.Volume, Mode: "hardlink"}, doc, g, call); e == nil {
		t.Fatal("private copy accepted as immutable shared hardlink")
	}
}

func TestSealedUploadReplayCannotChangePublishedBytes(t *testing.T) {
	s, a := fixture(t)
	startStore(t, s)
	files := sampleVPK()
	var manifest []protocol.WorkshopFile
	for _, name := range []string{"123_000.vpk", "123_dir.vpk"} {
		manifest = append(manifest, protocol.WorkshopFile{Name: name, Size: int64(len(files[name])), SHA256: storeio.Hash(files[name])})
	}
	call := func(c context.Context, r protocol.WorkshopRequest) (protocol.WorkshopResult, error) {
		return s.Handle(c, a.ID, r)
	}
	r, e := await(context.Background(), call, protocol.WorkshopRequest{Operation: "begin", RequestID: "sealed-replay", ItemID: "123", Revision: "55", Files: manifest})
	if e != nil {
		t.Fatal(e)
	}
	for _, f := range manifest {
		if e = s.chunk(context.Background(), a.ID, protocol.WorkshopRequest{Token: r.Token, File: f.Name, Data: files[f.Name]}); e != nil {
			t.Fatal(e)
		}
	}
	if _, e = s.seal(context.Background(), a.ID, protocol.WorkshopRequest{Token: r.Token, ItemID: "123", Revision: "55"}); e != nil {
		t.Fatal(e)
	}
	if e = s.chunk(context.Background(), a.ID, protocol.WorkshopRequest{Token: r.Token, File: "123_000.vpk", Data: []byte("evil")}); e == nil {
		t.Fatal("changed sealed bytes accepted")
	}
	if e = s.chunk(context.Background(), a.ID, protocol.WorkshopRequest{Token: r.Token, File: "123_000.vpk", Data: []byte("data")}); e != nil {
		t.Fatal("exact retry rejected", e)
	}
	s.o.TrustedImporters = nil
	if e = s.chunk(context.Background(), a.ID, protocol.WorkshopRequest{Token: r.Token, File: "123_000.vpk", Data: []byte("data")}); e == nil {
		t.Fatal("revoked importer resumed sealed upload")
	}
}

func TestLinkInfoRequiresReservedExactGenerationAndOwner(t *testing.T) {
	s, a := fixture(t)
	g, e := s.Import(context.Background(), a.ID, "123", "55", sampleVPK())
	if e != nil {
		t.Fatal(e)
	}
	b := node.Server{ID: "consumer", Volume: t.TempDir()}
	s.o.Servers = append(s.o.Servers, b)
	req := protocol.WorkshopRequest{Operation: "link-info", ItemID: "123", Revision: "55", Digest: g.Digest}
	if _, e = s.Handle(context.Background(), b.ID, req); e == nil {
		t.Fatal("unreserved server obtained source identity")
	}
	if _, e = s.Handle(context.Background(), "unknown", req); e == nil {
		t.Fatal("unknown server obtained source identity")
	}
	if _, e = s.Handle(context.Background(), a.ID, req); e != nil {
		t.Fatalf("reserved source identity rejected: %v", e)
	}
	bad := req
	bad.Digest = strings.Repeat("0", 64)
	if _, e = s.Handle(context.Background(), a.ID, bad); e == nil {
		t.Fatal("wrong generation digest accepted")
	}
	p := filepath.Join(s.root, "generations", g.Key(), g.Files[0].Name)
	if e = os.Chmod(p, 0644); e != nil {
		t.Fatal(e)
	}
	if _, e = s.Handle(context.Background(), a.ID, req); e == nil {
		t.Fatal("mutable source accepted")
	}
}

func TestLinkInfoRejectsOversizedMetadata(t *testing.T) {
	s, a := fixture(t)
	files := sampleVPK()
	for i := 0; i < MaxFiles-2; i++ {
		files[fmt.Sprintf("%03d", i)+strings.Repeat("\x01", 250)] = nil
	}
	g, e := s.Import(context.Background(), a.ID, "123", "55", files)
	if e != nil {
		t.Fatal(e)
	}
	_, e = s.Handle(context.Background(), a.ID, protocol.WorkshopRequest{Operation: "link-info", ItemID: g.ItemID, Revision: g.Revision, Digest: g.Digest})
	if e == nil || !strings.Contains(e.Error(), "frame budget") {
		t.Fatalf("oversized link response not rejected: %v", e)
	}
}

func TestInventoryReconstructionRejectsUntrackedLinks(t *testing.T) {
	s, a := fixture(t)
	g, e := s.Import(context.Background(), a.ID, "123", "55", sampleVPK())
	if e != nil {
		t.Fatal(e)
	}
	local, e := loadLocal(a.Volume)
	if e != nil {
		t.Fatal(e)
	}
	opts := LocalOptions{Root: a.Volume}
	if !inventoryKnown(context.Background(), opts, local, nil) {
		t.Fatal("empty content cannot be reconstructed")
	}
	seedCompleteItem(t, a.Volume)
	if !inventoryKnown(context.Background(), opts, local, nil) {
		t.Fatal("private single-link local item incorrectly treated as shared")
	}
	p := filepath.Join(a.Volume, ContentPath, "123", g.Files[0].Name)
	if e = os.Remove(p); e != nil {
		t.Fatal(e)
	}
	if e = os.Link(filepath.Join(s.root, "generations", g.Key(), g.Files[0].Name), p); e != nil {
		t.Fatal(e)
	}
	if inventoryKnown(context.Background(), opts, local, nil) {
		t.Fatal("untracked hardlink allowed pin retirement")
	}
	if e = os.Remove(p); e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(p, sampleVPK()[g.Files[0].Name], 0600); e != nil {
		t.Fatal(e)
	}
	if !inventoryKnown(context.Background(), opts, local, nil) {
		t.Fatal("verified private reconstruction remained unknown")
	}
}
