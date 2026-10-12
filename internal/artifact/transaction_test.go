// SPDX-License-Identifier: GPL-3.0-or-later
package artifact

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestApplyPreservesCreateOnlyFiles(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(t.TempDir(), "new")
	put(t, source, "new")
	put(t, filepath.Join(root, "config"), "operator")
	put(t, filepath.Join(root, "remove"), "old")
	if e := Apply(context.Background(), root, []Change{{Path: "config", Source: source, CreateOnly: true}, {Path: "bin/plugin", Source: source}, {Path: "remove", Remove: true}}); e != nil {
		t.Fatal(e)
	}
	wantFile(t, filepath.Join(root, "config"), "operator")
	wantFile(t, filepath.Join(root, "bin/plugin"), "new")
	if _, e := os.Stat(filepath.Join(root, "remove")); !os.IsNotExist(e) {
		t.Fatal("remove failed")
	}
}
func TestApplyRejectsUnsafePathsBeforeMutation(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	source := filepath.Join(t.TempDir(), "new")
	put(t, source, "new")
	put(t, filepath.Join(root, "old"), "old")
	if e := os.Symlink(outside, filepath.Join(root, "link")); e != nil {
		t.Fatal(e)
	}
	for _, p := range []string{"../escape", "/absolute", "link/escape", ".artifact-txn/evil", "old/child"} {
		if Apply(context.Background(), root, []Change{{Path: "old", Source: source}, {Path: p, Source: source}}) == nil {
			t.Fatalf("accepted %s", p)
		}
		wantFile(t, filepath.Join(root, "old"), "old")
	}
}
func TestRecoverInterruptedCommit(t *testing.T) {
	// Durable write-ahead fixture: one replacement applied, one removal not yet applied,
	// and a new file installed. Recovery must restore a complete old generation and be repeatable.
	root := t.TempDir()
	dir := filepath.Join(root, ".artifact-txn")
	if e := os.MkdirAll(filepath.Join(dir, "old"), 0700); e != nil {
		t.Fatal(e)
	}
	put(t, filepath.Join(root, "a"), "new a")
	put(t, filepath.Join(root, "b"), "old b")
	put(t, filepath.Join(root, "c"), "new c")
	put(t, filepath.Join(dir, "old/0"), "old a")
	put(t, filepath.Join(dir, "old/1"), "old b")
	data := map[string]any{"version": 1, "entries": []map[string]any{{"path": "a", "had_old": true}, {"path": "b", "had_old": true}, {"path": "c", "had_old": false}}}
	b, e := json.Marshal(data)
	if e != nil {
		t.Fatal(e)
	}
	put(t, filepath.Join(dir, "journal.json"), string(b))
	for i := 0; i < 2; i++ {
		if e := Recover(root); e != nil {
			t.Fatal(e)
		}
		wantFile(t, filepath.Join(root, "a"), "old a")
		wantFile(t, filepath.Join(root, "b"), "old b")
		if _, e := os.Stat(filepath.Join(root, "c")); !os.IsNotExist(e) {
			t.Fatal("new file survived rollback")
		}
	}
}
func TestRecoverRejectsEscapingJournal(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, ".artifact-txn")
	if e := os.Mkdir(dir, 0700); e != nil {
		t.Fatal(e)
	}
	put(t, filepath.Join(dir, "journal.json"), `{"version":1,"entries":[{"path":"../escape","had_old":false}]}`)
	if Recover(root) == nil {
		t.Fatal("unsafe journal accepted")
	}
}

func TestApplyCancellationAndMissingSourcePreserveGeneration(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(t.TempDir(), "new")
	put(t, source, "new")
	put(t, filepath.Join(root, "a"), "old a")
	put(t, filepath.Join(root, "b"), "old b")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if Apply(ctx, root, []Change{{Path: "a", Source: source}}) == nil {
		t.Fatal("cancelled apply succeeded")
	}
	if Apply(context.Background(), root, []Change{{Path: "a", Source: source}, {Path: "b", Source: source + "missing"}}) == nil {
		t.Fatal("missing source accepted")
	}
	wantFile(t, filepath.Join(root, "a"), "old a")
	wantFile(t, filepath.Join(root, "b"), "old b")
}
func TestRecoverMissingBackupDoesNotPartiallyRollback(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, ".artifact-txn")
	if e := os.MkdirAll(filepath.Join(dir, "old"), 0700); e != nil {
		t.Fatal(e)
	}
	put(t, filepath.Join(root, "a"), "new a")
	put(t, filepath.Join(root, "b"), "new b")
	put(t, filepath.Join(dir, "old/0"), "old a")
	put(t, filepath.Join(dir, "journal.json"), `{"version":1,"entries":[{"path":"a","had_old":true},{"path":"b","had_old":true}]}`)
	if Recover(root) == nil {
		t.Fatal("missing rollback backup accepted")
	}
	wantFile(t, filepath.Join(root, "a"), "new a")
	wantFile(t, filepath.Join(root, "b"), "new b")
}
func TestRecoverCommittedGeneration(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, ".artifact-txn")
	if e := os.Mkdir(dir, 0700); e != nil {
		t.Fatal(e)
	}
	put(t, filepath.Join(root, "a"), "new a")
	put(t, filepath.Join(dir, "committed"), "")
	if e := Recover(root); e != nil {
		t.Fatal(e)
	}
	wantFile(t, filepath.Join(root, "a"), "new a")
	if _, e := os.Stat(dir); !os.IsNotExist(e) {
		t.Fatal("committed transaction not cleaned")
	}
}
func TestApplyParentSymlinkRaceNeverEscapes(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	source := filepath.Join(t.TempDir(), "source")
	put(t, source, "new")
	parent := filepath.Join(root, "race")
	if e := os.Mkdir(parent, 0755); e != nil {
		t.Fatal(e)
	}
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-stop:
				return
			default:
			}
			if os.Rename(parent, parent+"-saved") == nil {
				_ = os.Symlink(outside, parent)
				_ = os.Remove(parent)
				_ = os.Rename(parent+"-saved", parent)
			}
		}
	}()
	for i := 0; i < 100; i++ {
		_ = Apply(context.Background(), root, []Change{{Path: "race/escape", Source: source}})
	}
	close(stop)
	<-done
	if _, e := os.Stat(filepath.Join(outside, "escape")); !os.IsNotExist(e) {
		t.Fatal("symlink race escaped root")
	}
}

// Writer acceptance of a journal that recovery cannot read must fail before the
// write-ahead publication boundary which allows managed-file mutations.
func TestWriteJournalRejectsOversizeBeforePublication(t *testing.T) {
	rootPath := t.TempDir()
	put(t, filepath.Join(rootPath, "live"), "old generation")
	if err := os.Mkdir(filepath.Join(rootPath, transactionDir), 0700); err != nil {
		t.Fatal(err)
	}
	root, err := openDir(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	// Every path has ordinary relative components within Linux path/component
	// bounds; the aggregate serialized journal, rather than any entry, is too big.
	prefix := strings.Repeat(strings.Repeat("a", 200)+"/", 15)
	j := journal{Version: 1}
	for i := 0; i < 6000; i++ {
		j.Entries = append(j.Entries, journalEntry{Path: prefix + strconv.Itoa(i), HadOld: false})
	}
	if err := writeJournal(root, j); err == nil {
		t.Fatal("writer published a journal larger than recovery can read")
	}
	for _, name := range []string{"journal.json", "journal.tmp"} {
		if _, err := os.Stat(filepath.Join(rootPath, transactionDir, name)); !os.IsNotExist(err) {
			t.Fatalf("oversized journal left %s: %v", name, err)
		}
	}
	if err := Recover(rootPath); err != nil {
		t.Fatalf("oversized journal rejection left unrecoverable state: %v", err)
	}
	wantFile(t, filepath.Join(rootPath, "live"), "old generation")
}
