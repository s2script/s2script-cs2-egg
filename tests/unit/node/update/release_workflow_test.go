// SPDX-License-Identifier: GPL-3.0-or-later
package update_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestReleaseRebuildCannotRollChannelBackward(t *testing.T) {
	_, file, _, _ := runtime.Caller(0)
	workflow, err := os.ReadFile(filepath.Join(filepath.Dir(file), "../../../../.github/workflows/release.yml"))
	if err != nil {
		t.Fatal(err)
	}
	_, step, ok := strings.Cut(string(workflow), "- name: Advance channel only for its newest release")
	if !ok {
		t.Fatal("missing channel promotion step")
	}
	_, script, ok := strings.Cut(step, "run: |\n")
	if !ok {
		t.Fatal("missing channel promotion script")
	}
	for _, channel := range []string{"stable", "beta"} {
		for _, newest := range []string{"v0.2.0-alpha.1", "v0.2.0-alpha.2"} {
			t.Run(channel+"/"+newest, func(t *testing.T) {
				dir := t.TempDir()
				result := filepath.Join(dir, "promoted")
				for name, source := range map[string]string{"gh": "#!/bin/bash\nprintf '%s\\n' \"$NEWEST\"\n", "docker": "#!/bin/bash\nprintf '%s\\n' \"$*\" > \"$RESULT\"\n"} {
					if err := os.WriteFile(filepath.Join(dir, name), []byte(source), 0700); err != nil {
						t.Fatal(err)
					}
				}
				cmd := exec.Command("bash", "-c", script)
				cmd.Env = append(os.Environ(), "PATH="+dir+":/usr/bin:/bin", "NEWEST="+newest, "RESULT="+result, "TAG=v0.2.0-alpha.1", "CHANNEL="+channel, "GH_REPO=s2script/s2script-cs2-egg")
				if out, err := cmd.CombinedOutput(); err != nil {
					t.Fatalf("promotion: %v %s", err, out)
				}
				got, err := os.ReadFile(result)
				if newest != "v0.2.0-alpha.1" {
					if !os.IsNotExist(err) {
						t.Fatal("old release advanced channel")
					}
					return
				}
				if err != nil || !strings.Contains(string(got), "--tag ghcr.io/s2script/s2script-cs2-egg:"+channel+" ghcr.io/s2script/s2script-cs2-egg:v0.2.0-alpha.1") {
					t.Fatalf("promotion=%s err=%v", got, err)
				}
			})
		}
	}
}
