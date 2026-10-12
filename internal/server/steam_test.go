// SPDX-License-Identifier: GPL-3.0-or-later
package server

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/s2script/s2script-cs2-egg/internal/config"
)

func steamFixture(t *testing.T) (config.Server, string) {
	t.Helper()
	root := t.TempDir()
	seed := t.TempDir()
	os.MkdirAll(filepath.Join(seed, "linux32"), 0700)
	script := `#!/bin/sh
[ "$HOME" = "` + root + `" ] || exit 9
[ "$PWD" = "$HOME/steamcmd" ] || exit 8
printf '%s\n' "$*" >> "$HOME/steam-calls"
mkdir -p linux64 linux32
printf 'sdk64' > linux64/steamclient.so
printf 'sdk32' > linux32/steamclient.so
case "$*" in
 *+app_update*) mkdir -p "$HOME/steamapps"; printf '"appid" "730" "buildid" "123"' > "$HOME/steamapps/appmanifest_730.acf";;
esac
`
	os.WriteFile(filepath.Join(seed, "steamcmd.sh"), []byte(script), 0755)
	for _, p := range []string{"linux32/steamcmd", "linux32/steamerrorreporter", "linux32/libstdc++.so.6", "linux32/crashhandler.so"} {
		os.WriteFile(filepath.Join(seed, p), []byte("fixture"), 0755)
	}
	cfg := config.Server{Root: root, Steam: config.Steam{AppID: 730, Update: true, Validate: true}}
	return cfg, seed
}
func TestSteamBootstrapAndRetainedContract(t *testing.T) {
	cfg, seed := steamFixture(t)
	build, e := prepareSteam(context.Background(), cfg, seed)
	if e != nil || build != "123" {
		t.Fatalf("%q %v", build, e)
	}
	b, _ := os.ReadFile(filepath.Join(cfg.Root, "steam-calls"))
	if string(b) != "+quit\n+force_install_dir "+cfg.Root+" +login anonymous +app_update 730 validate +quit\n" {
		t.Fatalf("Steam argv: %s", b)
	}
	sdk, e := os.ReadFile(filepath.Join(cfg.Root, ".steam/sdk64/steamclient.so"))
	if e != nil || string(sdk) != "sdk64" {
		t.Fatal("SDK64 absent")
	}
	cfg.Steam.Update = false
	os.RemoveAll(seed)
	if _, e = prepareSteam(context.Background(), cfg, seed); e != nil {
		t.Fatal(e)
	}
	after, _ := os.ReadFile(filepath.Join(cfg.Root, "steam-calls"))
	if string(after) != string(b) {
		t.Fatal("retained startup reinitialized/networked")
	}
}
func TestSteamRefusesSDKPathEscape(t *testing.T) {
	cfg, seed := steamFixture(t)
	outside := t.TempDir()
	os.Symlink(outside, filepath.Join(cfg.Root, ".steam"))
	if _, e := prepareSteam(context.Background(), cfg, seed); e == nil {
		t.Fatal("SDK escaped root")
	}
	entries, _ := os.ReadDir(outside)
	if len(entries) != 0 {
		t.Fatal("wrote external SDK")
	}
}
func TestSteamMissingGeneratedSDKFails(t *testing.T) {
	cfg, seed := steamFixture(t)
	os.WriteFile(filepath.Join(seed, "steamcmd.sh"), []byte("#!/bin/sh\nexit 0\n"), 0755)
	if _, e := prepareSteam(context.Background(), cfg, seed); e == nil || !strings.Contains(e.Error(), "SDK64") {
		t.Fatalf("missing SDK not actionable: %v", e)
	}
}
