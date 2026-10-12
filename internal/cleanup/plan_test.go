// SPDX-License-Identifier: GPL-3.0-or-later
package cleanup

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCleanupRejectsEscapingSymlinkAndChangedCandidate(t *testing.T) {
	root := t.TempDir()
	os.Chmod(root, 0700)
	if e := Initialize(root, "logs"); e != nil {
		t.Fatal(e)
	}
	os.WriteFile(filepath.Join(root, "one.log"), []byte("old"), 0600)
	if e := Seal(root, "one.log"); e != nil {
		t.Fatal(e)
	}
	os.Symlink("/etc/passwd", filepath.Join(root, "escape.log"))
	c, e := Plan(root, []Rule{{Pattern: "*.log", MaxRetained: 0}}, time.Now())
	if e != nil {
		t.Fatal(e)
	}
	if len(c) != 1 {
		t.Fatal(c)
	}
	os.Remove(filepath.Join(root, "one.log"))
	os.WriteFile(filepath.Join(root, "one.log"), []byte("newer"), 0600)
	r, e := Apply(context.Background(), root, c)
	if e != nil {
		t.Fatal(e)
	}
	if len(r.Removed) != 0 {
		t.Fatal("changed candidate deleted")
	}
	if _, e = os.Stat(filepath.Join(root, "one.log")); e != nil {
		t.Fatal(e)
	}
	for _, p := range []string{"cfg/*", "data/*", "generations/*", "../*"} {
		if _, e = Plan(root, []Rule{{Pattern: p}}, time.Now()); e == nil {
			t.Fatal("unsafe rule accepted", p)
		}
	}
}
