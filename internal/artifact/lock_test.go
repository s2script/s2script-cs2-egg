// SPDX-License-Identifier: GPL-3.0-or-later
package artifact

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestAcquireCrossProcess(t *testing.T) {
	if root := os.Getenv("ARTIFACT_LOCK_TEST_ROOT"); root != "" {
		release, e := Acquire(root)
		if e == nil {
			release()
			os.Exit(2)
		}
		os.Exit(0)
	}
	root := t.TempDir()
	release, e := Acquire(root)
	if e != nil {
		t.Fatal(e)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestAcquireCrossProcess$")
	cmd.Env = append(os.Environ(), "ARTIFACT_LOCK_TEST_ROOT="+root)
	if out, e := cmd.CombinedOutput(); e != nil {
		t.Fatalf("competing process acquired lock: %v %s", e, out)
	}
	if e = release(); e != nil {
		t.Fatal(e)
	}
	release, e = Acquire(root)
	if e != nil {
		t.Fatal(e)
	}
	if e = release(); e != nil {
		t.Fatal(e)
	}
}

func TestHasPendingRejectsUnsafeAndDetectsEmptyTransaction(t *testing.T) {
	root := t.TempDir()
	pending, e := HasPending(root)
	if e != nil || pending {
		t.Fatalf("%v %v", pending, e)
	}
	if e = os.Mkdir(filepath.Join(root, transactionDir), 0700); e != nil {
		t.Fatal(e)
	}
	if pending, e = HasPending(root); e != nil || !pending {
		t.Fatalf("%v %v", pending, e)
	}
	os.Remove(filepath.Join(root, transactionDir))
	os.Symlink(t.TempDir(), filepath.Join(root, transactionDir))
	if _, e = HasPending(root); e == nil {
		t.Fatal("followed pending transaction symlink")
	}
}
