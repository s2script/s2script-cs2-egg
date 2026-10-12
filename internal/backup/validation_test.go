// SPDX-License-Identifier: GPL-3.0-or-later
package backup

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"github.com/s2script/s2script-cs2-egg/internal/storeio"
	"os"
	"path/filepath"
	"testing"
)

func TestRestoreRejectsMalformedMembersBeforeMutation(t *testing.T) {
	for _, bad := range []string{"traversal", "symlink", "duplicate", "extra", "trailing-stream"} {
		t.Run(bad, func(t *testing.T) {
			s := fixture(t)
			put(t, s, "egg/server.json", "old")
			m, e := s.Create(context.Background(), Options{})
			if e != nil {
				t.Fatal(e)
			}
			var b bytes.Buffer
			gz := gzip.NewWriter(&b)
			tw := tar.NewWriter(gz)
			name := "egg/server.json"
			kind := byte(tar.TypeReg)
			size := int64(3)
			link := ""
			if bad == "traversal" {
				name = "../outside"
			}
			if bad == "symlink" {
				kind = tar.TypeSymlink
				size = 0
				link = "/etc/passwd"
			}
			if e = tw.WriteHeader(&tar.Header{Name: name, Typeflag: kind, Size: size, Linkname: link, Mode: 0600}); e != nil {
				t.Fatal(e)
			}
			if size > 0 {
				tw.Write([]byte("old"))
			}
			if bad == "duplicate" || bad == "extra" {
				if bad == "extra" {
					name = "egg/extra"
				}
				tw.WriteHeader(&tar.Header{Name: name, Typeflag: tar.TypeReg, Size: 3, Mode: 0600})
				tw.Write([]byte("bad"))
			}
			tw.Close()
			gz.Close()
			if bad == "trailing-stream" {
				g := gzip.NewWriter(&b)
				g.Write([]byte("hidden"))
				g.Close()
			}
			m.ArchiveSHA256 = storeio.Hash(b.Bytes())
			m.ArchiveBytes = int64(b.Len())
			root := filepath.Join(s.policy.Root, "archives", m.BackupID)
			if e = os.WriteFile(filepath.Join(root, "archive.tar.gz"), b.Bytes(), 0600); e != nil {
				t.Fatal(e)
			}
			if e = storeio.Save(root, "manifest.json", m); e != nil {
				t.Fatal(e)
			}
			if _, e = s.Preview(context.Background(), m.BackupID); e == nil {
				t.Fatal("malformed archive accepted")
			}
			value, _ := os.ReadFile(filepath.Join(s.volume, "egg/server.json"))
			if string(value) != "old" {
				t.Fatal("preview mutated target")
			}
		})
	}
}
func TestOnlyScopedOrphanStagesAreReclaimed(t *testing.T) {
	s := fixture(t)
	put(t, s, "egg/server.json", "private")
	stage, e := s.newStage(".stage-")
	if e != nil {
		t.Fatal(e)
	}
	os.WriteFile(filepath.Join(stage, "archive.tar.gz"), []byte("partial"), 0600)
	if _, e = s.Create(context.Background(), Options{}); e != nil {
		t.Fatal(e)
	}
	if _, e = os.Stat(stage); !os.IsNotExist(e) {
		t.Fatal("orphan stage retained", e)
	}
	unknown := filepath.Join(s.policy.Root, ".stage-unknown")
	os.Mkdir(unknown, 0700)
	os.WriteFile(filepath.Join(unknown, "important"), []byte("keep"), 0600)
	if _, e = s.Create(context.Background(), Options{}); e == nil {
		t.Fatal("unknown stage should require inspection")
	}
	if b, e := os.ReadFile(filepath.Join(unknown, "important")); e != nil || string(b) != "keep" {
		t.Fatal("unowned staging content removed")
	}
}
