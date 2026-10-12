// SPDX-License-Identifier: GPL-3.0-or-later
package workshop

import (
	"context"
	"encoding/binary"
	node "github.com/s2script/s2script-cs2-egg/internal/nodetypes"
	"github.com/s2script/s2script-cs2-egg/internal/runtimelease"
	"os"
	"path/filepath"
	"testing"
)

func sampleVPK() map[string][]byte {
	tree := []byte("txt\x00 \x00a\x00")
	entry := make([]byte, 18)
	binary.LittleEndian.PutUint16(entry[6:], 0)
	binary.LittleEndian.PutUint32(entry[12:], 4)
	binary.LittleEndian.PutUint16(entry[16:], 0xffff)
	tree = append(tree, entry...)
	tree = append(tree, 0, 0, 0)
	header := make([]byte, 12)
	binary.LittleEndian.PutUint32(header, 0x55aa1234)
	binary.LittleEndian.PutUint32(header[4:], 1)
	binary.LittleEndian.PutUint32(header[8:], uint32(len(tree)))
	return map[string][]byte{"123_dir.vpk": append(header, tree...), "123_000.vpk": []byte("data")}
}
func fixture(t *testing.T) (*Store, node.Server) {
	t.Helper()
	srv := node.Server{ID: "trusted", Volume: t.TempDir()}
	s, e := New(Options{Root: t.TempDir(), TrustedImporters: []string{"trusted"}, Servers: []node.Server{srv}, Items: []string{"123"}, Details: func(context.Context, []string) (map[string]Detail, error) {
		return map[string]Detail{"123": {ID: "123", Revision: "55", AppID: 730, Size: 0}}, nil
	}})
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() {
		filepath.Walk(s.root, func(p string, i os.FileInfo, e error) error {
			if e == nil && i.IsDir() {
				os.Chmod(p, 0700)
			}
			return nil
		})
	})
	return s, srv
}
func TestWorkshopChunkedItemPublishedTogether(t *testing.T) {
	s, srv := fixture(t)
	g, e := s.Import(context.Background(), srv.ID, "123", "55", sampleVPK())
	if e != nil {
		t.Fatal(e)
	}
	if len(g.Files) != 2 {
		t.Fatal("missing chunk")
	}
	deleteFiles := sampleVPK()
	delete(deleteFiles, "123_000.vpk")
	if _, e = s.Import(context.Background(), srv.ID, "123", "56", deleteFiles); e == nil {
		t.Fatal("incomplete item published")
	}
}
func TestInterruptedWorkshopOwnerNotPublished(t *testing.T) {
	s, srv := fixture(t)
	token, e := s.Begin(context.Background(), srv.ID, "123", "55", "request")
	if e != nil {
		t.Fatal(e)
	}
	if token == "" {
		t.Fatal("no token")
	}
	if _, e = s.Generation("123", "55"); !os.IsNotExist(e) {
		t.Fatal("unfinished owner published", e)
	}
}
func TestWorkshopRunningServerRefused(t *testing.T) {
	s, srv := fixture(t)
	release, e := runtimelease.Acquire(srv.Volume)
	if e != nil {
		t.Fatal(e)
	}
	defer release()
	if _, e = s.Sync(context.Background(), srv); e == nil {
		t.Fatal("running server accepted")
	}
}
func TestLegacyUnversionedItemStaysLocal(t *testing.T) {
	s, srv := fixture(t)
	if _, e := s.Import(context.Background(), srv.ID, "123", "-1", sampleVPK()); e == nil {
		t.Fatal("legacy item imported")
	}
	if _, e := os.Stat(filepath.Join(s.root, "generations", "123", "-1")); !os.IsNotExist(e) {
		t.Fatal("legacy generation exists")
	}
}
func TestWorkshopMetadataPreservesOtherItems(t *testing.T) {
	raw := []byte(`"AppWorkshop" { "appid" "730" "custom" "keep" "WorkshopItemsInstalled" { "999" { "manifest" "88" } } "WorkshopItemDetails" { "999" { "manifest" "88" } } }`)
	d, e := ParseACF(raw)
	if e != nil {
		t.Fatal(e)
	}
	d.SetItem("123", "55", 99, 5)
	out, e := ParseACF(d.Bytes())
	if e != nil {
		t.Fatal(e)
	}
	if out.Root.Child("AppWorkshop").Value("custom") != "keep" || out.Root.Child("AppWorkshop").Child("WorkshopItemsInstalled").Child("999").Value("manifest") != "88" {
		t.Fatal("other metadata lost")
	}
}
