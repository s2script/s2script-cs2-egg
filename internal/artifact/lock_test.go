// SPDX-License-Identifier: GPL-3.0-or-later
package artifact

import (
	"os"
	"os/exec"
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
