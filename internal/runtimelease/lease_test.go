// SPDX-License-Identifier: GPL-3.0-or-later
package runtimelease

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestLifetimeLeaseExcludesMutationAndStartup(t *testing.T) {
	root := t.TempDir()
	release, e := Acquire(root)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = Acquire(root); e == nil {
		t.Fatal("second lifetime owner entered")
	}
	if e = release(); e != nil {
		t.Fatal(e)
	}
	if e = release(); e != nil {
		t.Fatal(e)
	}
	next, e := Acquire(root)
	if e != nil {
		t.Fatal(e)
	}
	next()
}
func TestLeaseRejectsSymlinks(t *testing.T) {
	root := t.TempDir()
	external := filepath.Join(t.TempDir(), "outside")
	os.WriteFile(external, []byte("untouched"), 0600)
	os.Symlink(external, filepath.Join(root, ".runtime-lease"))
	if _, e := Acquire(root); e == nil {
		t.Fatal("followed lease symlink")
	}
	link := filepath.Join(t.TempDir(), "root")
	os.Symlink(root, link)
	if _, e := Acquire(link); e == nil {
		t.Fatal("followed root symlink")
	}
}

func TestLeaseCrossProcess(t *testing.T) {
	if root := os.Getenv("S2SCRIPT_TASK4_LEASE_ROOT"); root != "" {
		if _, e := Acquire(root); !errors.Is(e, ErrHeld) {
			os.Exit(2)
		}
		os.Exit(0)
	}
	root := t.TempDir()
	release, e := Acquire(root)
	if e != nil {
		t.Fatal(e)
	}
	defer release()
	cmd := exec.Command(os.Args[0], "-test.run=^TestLeaseCrossProcess$")
	cmd.Env = append(os.Environ(), "S2SCRIPT_TASK4_LEASE_ROOT="+root)
	if b, e := cmd.CombinedOutput(); e != nil {
		t.Fatalf("child lifetime owner entered: %v %s", e, b)
	}
}
