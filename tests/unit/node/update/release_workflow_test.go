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
	script := releaseStep(t, "Advance channel only for its newest release")
	for _, tc := range []struct {
		name, channel, tag, stable, published string
		aliases                               []string
	}{
		{"new stable advances both", "stable", "v7.4.0", "v7.4.0", "v7.4.0", []string{"stable", "beta"}},
		{"new beta", "beta", "v7.5.0-rc.1", "v7.4.0", "v7.5.0-rc.1", []string{"beta"}},
		{"old stable rebuild", "stable", "v7.3.0", "v7.4.0", "v7.5.0-rc.1", nil},
		{"old beta rebuild", "beta", "v7.5.0-rc.1", "v7.4.0", "v7.5.0-rc.2", nil},
		{"stable leaves newer beta", "stable", "v7.4.0", "v7.4.0", "v7.5.0-rc.1", []string{"stable"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			result := filepath.Join(dir, "promoted")
			for name, source := range map[string]string{
				"gh": `#!/bin/bash
if [[ "$2" == */releases/latest ]]; then printf '%s\n' "$STABLE_HEAD"; else printf '%s\n' "$PUBLISHED_HEAD"; fi
`,
				"docker": `#!/bin/bash
printf '%s\n' "$*" >> "$RESULT"
`,
			} {
				if err := os.WriteFile(filepath.Join(dir, name), []byte(source), 0700); err != nil {
					t.Fatal(err)
				}
			}
			cmd := exec.Command("bash", "-c", script)
			cmd.Env = append(os.Environ(), "PATH="+dir+":/usr/bin:/bin", "STABLE_HEAD="+tc.stable, "PUBLISHED_HEAD="+tc.published, "RESULT="+result, "TAG="+tc.tag, "CHANNEL="+tc.channel, "GH_REPO=s2script/s2script-cs2-egg")
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("promotion: %v %s", err, out)
			}
			got, err := os.ReadFile(result)
			if len(tc.aliases) == 0 {
				if !os.IsNotExist(err) {
					t.Fatalf("old release changed channels: %s", got)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if len(strings.Split(strings.TrimSpace(string(got)), "\n")) != len(tc.aliases) {
				t.Fatalf("unexpected aliases: %s", got)
			}
			for _, alias := range tc.aliases {
				if !strings.Contains(string(got), "--tag ghcr.io/s2script/s2script-cs2-egg:"+alias+" ghcr.io/s2script/s2script-cs2-egg:"+tc.tag) {
					t.Fatalf("missing %s promotion: %s", alias, got)
				}
			}
		})
	}
}

func releaseStep(t *testing.T, name string) string {
	t.Helper()
	_, file, _, _ := runtime.Caller(0)
	data, err := os.ReadFile(filepath.Join(filepath.Dir(file), "../../../../.github/workflows/release.yml"))
	if err != nil {
		t.Fatal(err)
	}
	_, after, ok := strings.Cut(string(data), "- name: "+name+"\n")
	if !ok {
		t.Fatalf("missing step %s", name)
	}
	_, after, ok = strings.Cut(after, "        run: |\n")
	if !ok {
		t.Fatalf("missing shell for %s", name)
	}
	var lines []string
	for _, line := range strings.Split(after, "\n") {
		if !strings.HasPrefix(line, "          ") {
			break
		}
		lines = append(lines, strings.TrimPrefix(line, "          "))
	}
	return strings.Join(lines, "\n")
}

func TestReleaseTagOverridesLocalVersion(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "VERSION"), []byte("0.0.0-dev\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for name, source := range map[string]string{"git": "#!/bin/bash\nprintf '%040d\\n' 1\n", "make": "#!/bin/bash\nprintf '%s\\n' \"$*\" >> \"$RESULT\"\n", "go": "#!/bin/bash\nprintf '%s\\n' \"$FAKE_GOROOT\"\n", "cp": "#!/bin/bash\nexit 0\n", "sha256sum": "#!/bin/bash\nprintf 'hash  cs2node_linux_amd64\\n'\n"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(source), 0700); err != nil {
			t.Fatal(err)
		}
	}
	output := filepath.Join(dir, "output")
	result := filepath.Join(dir, "result")
	env := append(os.Environ(), "PATH="+dir+":/usr/bin:/bin", "VERSION=7.4.0", "CHANNEL=stable", "COMMIT="+strings.Repeat("a", 40), "GITHUB_OUTPUT="+output, "RESULT="+result, "FAKE_GOROOT="+dir)
	for _, step := range []string{"Validate checked-out source", "Build matching node assets"} {
		cmd := exec.Command("bash", "-e", "-c", releaseStep(t, step))
		cmd.Dir = dir
		cmd.Env = env
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%s: %v %s", step, err, out)
		}
	}
	got, err := os.ReadFile(result)
	if err != nil || !strings.Contains(string(got), "VERSION=7.4.0") || !strings.Contains(string(got), "CHANNEL=stable") {
		t.Fatalf("build did not explicitly receive release version/channel: %q %v", got, err)
	}
}

func TestReleaseHelperNeedsOnlyAnExplicitTag(t *testing.T) {
	_, file, _, _ := runtime.Caller(0)
	script := filepath.Join(filepath.Dir(file), "../../../../scripts/release.sh")
	for _, tc := range []struct {
		tag        string
		prerelease bool
	}{{"v7.4.0", false}, {"v7.5.0-rc.1", true}} {
		args := []string{script, tc.tag, "--dry-run"}
		if tc.prerelease {
			args = append(args, "--prerelease")
		}
		cmd := exec.Command("bash", args...)
		out, err := cmd.CombinedOutput()
		if err != nil || !strings.Contains(string(out), tc.tag) || !strings.Contains(string(out), "--generate-notes") {
			t.Fatalf("explicit-tag release: %v %s", err, out)
		}
	}
}
