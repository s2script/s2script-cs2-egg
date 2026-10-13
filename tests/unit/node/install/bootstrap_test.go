// SPDX-License-Identifier: GPL-3.0-or-later
package install_test

import (
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestBootstrapVerifiesBeforeExecuting(t *testing.T) {
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("bootstrap lookup fixtures require jq")
	}
	_, file, _, _ := runtime.Caller(0)
	script := filepath.Join(filepath.Dir(file), "../../../../scripts/install-node.sh")
	for _, bad := range []bool{false, true} {
		t.Run(fmt.Sprint("bad_checksum=", bad), func(t *testing.T) {
			dir := t.TempDir()
			payload := []byte("#!/bin/bash\nprintf '%s\\n' \"$@\" > \"$RESULT\"\n")
			checksum := fmt.Sprintf("%x  cs2node_linux_amd64\n", sha256.Sum256(payload))
			if bad {
				checksum = strings.Repeat("0", 64) + "  cs2node_linux_amd64\n"
			}
			files := map[string][]byte{
				"asset": payload, "checksums": []byte(checksum),
				"metadata":   []byte(`{"tag_name":"v0.2.0-alpha.1","draft":false,"prerelease":true,"assets":[{"name":"cs2node_linux_amd64"},{"name":"checksums.txt"}]}`),
				"curl":       []byte("#!/bin/bash\nset -eu\nurl= out=\nwhile (($#)); do case \"$1\" in -o) out=$2;shift 2;; https:*) url=$1;shift;; *) shift;; esac;done\nprintf '%s\\n' \"$url\" >> \"$FETCHES\"\ncase \"$url\" in */repos/s2script/s2script-cs2-egg/releases/tags/*) cp \"$FIXTURE/metadata\" \"$out\";; */cs2node_linux_amd64) cp \"$FIXTURE/asset\" \"$out\";; */checksums.txt) cp \"$FIXTURE/checksums\" \"$out\";; *) exit 2;; esac\n"),
				"sudo":       []byte("#!/bin/bash\nexec \"$@\"\n"),
				"dpkg-query": []byte("#!/bin/bash\nexit 1\n"),
				"apt-get":    []byte("#!/bin/bash\nprintf '%s\\n' \"$*\" >> \"$APT_CALLS\"\n"),
			}
			for name, data := range files {
				if err := os.WriteFile(filepath.Join(dir, name), data, 0700); err != nil {
					t.Fatal(err)
				}
			}
			result := filepath.Join(dir, "result")
			cmd := exec.Command("bash", script, "--yes")
			cmd.Env = append(os.Environ(), "PATH="+dir+":/usr/bin:/bin", "FIXTURE="+dir, "RESULT="+result, "FETCHES="+filepath.Join(dir, "fetches"), "APT_CALLS="+filepath.Join(dir, "apt-calls"), "S2NODE_RELEASE=v0.2.0-alpha.1", "S2NODE_CHANNEL=stable")
			output, err := cmd.CombinedOutput()
			got, readErr := os.ReadFile(result)
			if bad {
				if err == nil || !os.IsNotExist(readErr) {
					t.Fatalf("bad checksum executed: %v %s", err, output)
				}
				if _, err := os.Stat(filepath.Join(dir, "apt-calls")); !os.IsNotExist(err) {
					t.Fatal("unverified download changed host packages")
				}
				return
			}
			if err != nil {
				t.Fatalf("bootstrap: %v %s", err, output)
			}
			if string(got) != "install\n--channel\nbeta\n--yes\n" {
				t.Fatalf("args=%q error=%v", got, readErr)
			}
			apt, _ := os.ReadFile(filepath.Join(dir, "apt-calls"))
			if string(apt) != "update\ninstall -y --no-install-recommends lib32gcc-s1 lib32stdc++6\n" {
				t.Fatalf("host prereq install=%q", apt)
			}
			fetches, _ := os.ReadFile(filepath.Join(dir, "fetches"))
			if !strings.Contains(string(fetches), "s2script/s2script-cs2-egg/releases/download/v0.2.0-alpha.1/") {
				t.Fatalf("wrong release: %s", fetches)
			}
		})
	}
}

func TestBootstrapFollowsReleaseChannels(t *testing.T) {
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("bootstrap lookup fixtures require jq")
	}
	_, file, _, _ := runtime.Caller(0)
	script := filepath.Join(filepath.Dir(file), "../../../../scripts/install-node.sh")
	stable := `{"tag_name":"v7.4.0","draft":false,"prerelease":false,"assets":[{"name":"cs2node_linux_amd64"},{"name":"checksums.txt"}]}`
	beta := `{"tag_name":"v7.5.0-rc.1","draft":false,"prerelease":true,"assets":[{"name":"cs2node_linux_amd64"},{"name":"checksums.txt"}]}`
	for _, tc := range []struct {
		name, channel, metadata, tag, nodeChannel string
		wantOK                                    bool
	}{
		{"latest stable", "stable", stable, "v7.4.0", "stable", true},
		{"beta skips drafts", "beta", `[{"tag_name":"v99.0.0","draft":true},` + beta + `,` + stable + `]`, "v7.5.0-rc.1", "beta", true},
		{"beta includes stable", "beta", `[` + stable + `]`, "v7.4.0", "beta", true},
		{"unfinished release", "stable", `{"tag_name":"v7.4.0","draft":false,"prerelease":false,"assets":[]}`, "", "", false},
		{"no published beta", "beta", `[]`, "", "", false},
		{"unsafe release tag", "stable", strings.ReplaceAll(stable, "v7.4.0", "../../other"), "", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			result := filepath.Join(dir, "result")
			payload := []byte("#!/bin/bash\nprintf '%s\\n' \"$@\" > \"$RESULT\"\n")
			files := map[string][]byte{
				"asset": payload, "checksums": []byte(fmt.Sprintf("%x  cs2node_linux_amd64\n", sha256.Sum256(payload))), "metadata": []byte(tc.metadata),
				"curl": []byte(`#!/bin/bash
set -eu
url= out=
while (($#)); do case "$1" in -o) out=$2;shift 2;; https:*) url=$1;shift;; *) shift;; esac;done
printf '%s\n' "$url" >> "$FETCHES"
case "$url" in
 */repos/s2script/s2script-cs2-egg/releases*) cp "$FIXTURE/metadata" "$out";;
 */cs2node_linux_amd64) cp "$FIXTURE/asset" "$out";;
 */checksums.txt) cp "$FIXTURE/checksums" "$out";;
 *) exit 2;;
esac
`),
				"sudo": []byte("#!/bin/bash\nexec \"$@\"\n"), "dpkg-query": []byte("#!/bin/bash\nprintf installed\n"), "apt-get": []byte("#!/bin/bash\nexit 99\n"),
			}
			for name, data := range files {
				if err := os.WriteFile(filepath.Join(dir, name), data, 0700); err != nil {
					t.Fatal(err)
				}
			}
			cmd := exec.Command("bash", script, "--yes")
			cmd.Env = append(os.Environ(), "PATH="+dir+":/usr/bin:/bin", "FIXTURE="+dir, "RESULT="+result, "FETCHES="+filepath.Join(dir, "fetches"), "S2NODE_RELEASE=", "S2NODE_CHANNEL="+tc.channel)
			output, err := cmd.CombinedOutput()
			got, readErr := os.ReadFile(result)
			if !tc.wantOK {
				if err == nil || !os.IsNotExist(readErr) {
					t.Fatalf("invalid metadata executed: %v %s", err, output)
				}
				return
			}
			if err != nil {
				t.Fatalf("lookup: %v %s", err, output)
			}
			if string(got) != "install\n--channel\n"+tc.nodeChannel+"\n--yes\n" {
				t.Fatalf("installer arguments=%q", got)
			}
			fetches, _ := os.ReadFile(filepath.Join(dir, "fetches"))
			if !strings.Contains(string(fetches), "/releases/download/"+tc.tag+"/cs2node_linux_amd64") || !strings.Contains(string(fetches), "/releases/download/"+tc.tag+"/checksums.txt") {
				t.Fatalf("unmatched assets: %s", fetches)
			}
		})
	}
}
