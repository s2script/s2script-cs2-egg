// SPDX-License-Identifier: GPL-3.0-or-later
package artifact

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestPrivateReplacementPreservesModeAndCreatesPrivateParents(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(t.TempDir(), "source")
	os.WriteFile(src, []byte("replacement"), 0600)
	os.WriteFile(filepath.Join(root, "old"), []byte("old"), 0640)
	release, e := Acquire(root)
	if e != nil {
		t.Fatal(e)
	}
	defer release()
	if e = Apply(context.Background(), root, []Change{{Path: "old", Source: src, PreserveMetadata: true}, {Path: "private/nested/new", Source: src, PreserveMetadata: true}}); e != nil {
		t.Fatal(e)
	}
	for p, want := range map[string]os.FileMode{"old": 0640, "private": 0700, "private/nested": 0700, "private/nested/new": 0600} {
		i, e := os.Stat(filepath.Join(root, p))
		if e != nil || i.Mode().Perm() != want {
			t.Fatalf("%s mode %v error %v", p, i, e)
		}
	}
}

func TestInterruptedPrivateRestoreRollsBackMetadataAndEmptyDirectories(t *testing.T) {
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, ".artifact-txn/old"), 0700)
	os.WriteFile(filepath.Join(root, ".artifact-txn/old/0"), []byte("original"), 0640)
	os.WriteFile(filepath.Join(root, "old"), []byte("replacement"), 0600)
	os.MkdirAll(filepath.Join(root, "private/nested"), 0700)
	os.WriteFile(filepath.Join(root, "private/nested/new"), []byte("new"), 0600)
	d, e := openDir(root)
	if e != nil {
		t.Fatal(e)
	}
	defer d.Close()
	j := journal{Version: 1, Entries: []journalEntry{{Path: "old", HadOld: true, PreserveMetadata: true}, {Path: "private/nested/new", PreserveMetadata: true}}, CreatedDirs: []string{"private", "private/nested"}}
	if e = writeJournal(d, j); e != nil {
		t.Fatal(e)
	}
	if e = Recover(root); e != nil {
		t.Fatal(e)
	}
	b, e := os.ReadFile(filepath.Join(root, "old"))
	i, _ := os.Stat(filepath.Join(root, "old"))
	if e != nil || string(b) != "original" || i.Mode().Perm() != 0640 {
		t.Fatal("rollback lost data/mode", e)
	}
	if _, e = os.Stat(filepath.Join(root, "private")); !os.IsNotExist(e) {
		t.Fatal("empty private parents left after rollback", e)
	}
}

func TestPrivateRestoreRetainsReadOnlyMode(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(t.TempDir(), "source")
	os.WriteFile(src, []byte("new"), 0600)
	os.WriteFile(filepath.Join(root, "config"), []byte("old"), 0400)
	unlock, e := Acquire(root)
	if e != nil {
		t.Fatal(e)
	}
	defer unlock()
	if e = Apply(context.Background(), root, []Change{{Path: "config", Source: src, PreserveMetadata: true}}); e != nil {
		t.Fatal(e)
	}
	b, e := os.ReadFile(filepath.Join(root, "config"))
	i, _ := os.Stat(filepath.Join(root, "config"))
	if e != nil || string(b) != "new" || i.Mode().Perm() != 0400 {
		t.Fatal("read-only file restore failed", e)
	}
}
