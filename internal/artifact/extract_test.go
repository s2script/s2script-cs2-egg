// SPDX-License-Identifier: GPL-3.0-or-later
package artifact

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func zipFixture(t *testing.T, name string, mode os.FileMode) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "test.zip")
	f, e := os.Create(p)
	if e != nil {
		t.Fatal(e)
	}
	z := zip.NewWriter(f)
	h := &zip.FileHeader{Name: name}
	h.SetMode(mode)
	w, e := z.CreateHeader(h)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = w.Write([]byte("payload")); e != nil {
		t.Fatal(e)
	}
	if e = z.Close(); e != nil {
		t.Fatal(e)
	}
	if e = f.Close(); e != nil {
		t.Fatal(e)
	}
	return p
}
func TestExtractRejectsTraversalAndSymlinkEscape(t *testing.T) {
	outside := t.TempDir()
	for _, name := range []string{"../escape", "/absolute", "nested/../../escape", "link/escape", "C:/escape", "a\\b"} {
		t.Run(name, func(t *testing.T) {
			stage := t.TempDir()
			if e := os.Symlink(outside, filepath.Join(stage, "link")); e != nil {
				t.Fatal(e)
			}
			if Extract(zipFixture(t, name, 0600), stage, 100) == nil {
				t.Fatal("unsafe name accepted")
			}
		})
	}
	if _, e := os.Stat(filepath.Join(outside, "escape")); !os.IsNotExist(e) {
		t.Fatal("wrote outside stage")
	}
	if Extract(zipFixture(t, "sym", os.ModeSymlink|0777), t.TempDir(), 100) == nil {
		t.Fatal("symlink member accepted")
	}
}
func TestExtractFormatsAndBound(t *testing.T) {
	stage := t.TempDir()
	if e := Extract(zipFixture(t, "bin/plugin.so", 0755), stage, 7); e != nil {
		t.Fatal(e)
	}
	wantFile(t, filepath.Join(stage, "bin/plugin.so"), "payload")
	if Extract(zipFixture(t, "big", 0600), t.TempDir(), 6) == nil {
		t.Fatal("oversize accepted")
	}
	p := filepath.Join(t.TempDir(), "test.tar.gz")
	f, e := os.Create(p)
	if e != nil {
		t.Fatal(e)
	}
	g := gzip.NewWriter(f)
	tw := tar.NewWriter(g)
	if e = tw.WriteHeader(&tar.Header{Name: "plugin", Mode: 0755, Size: 3}); e != nil {
		t.Fatal(e)
	}
	if _, e = tw.Write([]byte("tar")); e != nil {
		t.Fatal(e)
	}
	if e = tw.Close(); e != nil {
		t.Fatal(e)
	}
	if e = g.Close(); e != nil {
		t.Fatal(e)
	}
	if e = f.Close(); e != nil {
		t.Fatal(e)
	}
	stage = t.TempDir()
	if e = Extract(p, stage, 3); e != nil {
		t.Fatal(e)
	}
	wantFile(t, filepath.Join(stage, "plugin"), "tar")
}

func TestExtractAllowsConventionalDotPrefix(t *testing.T) {
	stage := t.TempDir()
	if e := Extract(zipFixture(t, "./addons/plugin.so", 0755), stage, 100); e != nil {
		t.Fatal(e)
	}
	wantFile(t, filepath.Join(stage, "addons/plugin.so"), "payload")
}
func TestExtractRejectsTarLinkAndAggregateSize(t *testing.T) {
	p := filepath.Join(t.TempDir(), "links.tar.gz")
	f, e := os.Create(p)
	if e != nil {
		t.Fatal(e)
	}
	g := gzip.NewWriter(f)
	tw := tar.NewWriter(g)
	if e = tw.WriteHeader(&tar.Header{Name: "link", Typeflag: tar.TypeLink, Linkname: "../../escape"}); e != nil {
		t.Fatal(e)
	}
	if e = tw.Close(); e != nil {
		t.Fatal(e)
	}
	if e = g.Close(); e != nil {
		t.Fatal(e)
	}
	if e = f.Close(); e != nil {
		t.Fatal(e)
	}
	if Extract(p, t.TempDir(), 100) == nil {
		t.Fatal("tar hardlink accepted")
	}
	p = filepath.Join(t.TempDir(), "aggregate.zip")
	f, e = os.Create(p)
	if e != nil {
		t.Fatal(e)
	}
	z := zip.NewWriter(f)
	for _, n := range []string{"a", "b"} {
		w, e := z.Create(n)
		if e != nil {
			t.Fatal(e)
		}
		if _, e = w.Write([]byte("four")); e != nil {
			t.Fatal(e)
		}
	}
	if e = z.Close(); e != nil {
		t.Fatal(e)
	}
	if e = f.Close(); e != nil {
		t.Fatal(e)
	}
	if Extract(p, t.TempDir(), 7) == nil {
		t.Fatal("aggregate expanded limit ignored")
	}
}

// Removing the decompressed-stream bound would let Next consume all repeated
// GNU metadata records while the visible-entry and file-content counts stay one.
func TestExtractBoundsHiddenTarMetadataAndPadding(t *testing.T) {
	var record bytes.Buffer
	tw := tar.NewWriter(&record)
	name := strings.Repeat("a", 200)
	if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0600, Size: 1, Format: tar.FormatGNU}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write([]byte("x")); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	data := record.Bytes()
	if len(data) < 1536 || data[156] != tar.TypeGNULongName {
		t.Fatal("fixture lacks GNU long-name record")
	}
	for _, kind := range []string{"hidden metadata", "trailing padding"} {
		t.Run(kind, func(t *testing.T) {
			p := filepath.Join(t.TempDir(), "metadata.tar.gz")
			f, err := os.Create(p)
			if err != nil {
				t.Fatal(err)
			}
			gz := gzip.NewWriter(f)
			if kind == "hidden metadata" {
				// Each hidden header/body pair occupies 1024 expanded bytes. Seventeen
				// thousand pairs exceed the explicit 16 MiB format allowance.
				for i := 0; i < 17000; i++ {
					if _, err = gz.Write(data[:1024]); err != nil {
						t.Fatal(err)
					}
				}
				_, err = gz.Write(data[1024:])
			} else {
				if _, err = gz.Write(data); err != nil {
					t.Fatal(err)
				}
				block := make([]byte, 1024)
				for i := 0; i < 17000; i++ {
					if _, err = gz.Write(block); err != nil {
						t.Fatal(err)
					}
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			if err = gz.Close(); err != nil {
				t.Fatal(err)
			}
			if err = f.Close(); err != nil {
				t.Fatal(err)
			}
			if err = Extract(p, t.TempDir(), 1); err == nil {
				t.Fatal("unbounded tar metadata/padding accepted")
			}
		})
	}
}
