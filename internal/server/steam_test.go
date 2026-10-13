// SPDX-License-Identifier: GPL-3.0-or-later
package server

import (
	"bufio"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/s2script/s2script-cs2-egg/internal/config"
)

func TestStartupStreamsSteamProgressBeforeCommandFinishes(t *testing.T) {
	for _, phase := range []string{"bootstrap", "update"} {
		t.Run(phase, func(t *testing.T) {
			base, seed := steamFixture(t)
			if err := os.Rename(seed, filepath.Join(base.Root, "steamcmd")); err != nil {
				t.Fatal(err)
			}
			if phase == "update" {
				os.MkdirAll(filepath.Join(base.Root, "steamcmd/linux64"), 0700)
				os.WriteFile(filepath.Join(base.Root, "steamcmd/linux64/steamclient.so"), []byte("sdk64"), 0600)
			}
			cfg, err := config.LoadServer(base.Root, func(k string) (string, bool) {
				if k == "STEAM_ACC" {
					return "private-progress-token", true
				}
				return "", false
			})
			if err != nil {
				t.Fatal(err)
			}
			script := "#!/bin/sh\nprintf 'Downloading 25%% private-progress-token\\n'\nwhile [ ! -f \"$HOME/continue\" ]; do sleep 0.01; done\nprintf 'Steam connection failed\\n'\nexit 8\n"
			if err := os.WriteFile(filepath.Join(base.Root, "steamcmd/steamcmd.sh"), []byte(script), 0755); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			reader, writer := io.Pipe()
			defer reader.Close()
			lines := make(chan string, 64)
			go func() {
				defer close(lines)
				scanner := bufio.NewScanner(reader)
				for scanner.Scan() {
					lines <- scanner.Text()
				}
			}()
			done := make(chan error, 1)
			go func() {
				_, err := Run(ctx, cfg, nil, nil, writer, writer, Options{})
				writer.Close()
				done <- err
			}()
			defer func() { cancel(); <-done }()
			progress := false
			deadline := time.NewTimer(time.Second)
			defer deadline.Stop()
			for !progress {
				select {
				case line, ok := <-lines:
					if !ok {
						t.Fatal("startup exited before showing progress")
					}
					if strings.Contains(line, "private-progress-token") {
						t.Fatal("progress leaked token")
					}
					progress = strings.Contains(line, "Downloading 25% [REDACTED]")
				case <-deadline.C:
					t.Fatal("SteamCMD output is hidden while startup is in progress")
				}
			}
			if err := os.WriteFile(filepath.Join(base.Root, "continue"), nil, 0600); err != nil {
				t.Fatal(err)
			}
			output := ""
			for line := range lines {
				output += line + "\n"
			}
			if !strings.Contains(output, "Steam connection failed") {
				t.Fatalf("Steam failure details hidden: %q", output)
			}
		})
	}
}

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
	build, e := prepareSteam(context.Background(), cfg, seed, nil)
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
	if _, e = prepareSteam(context.Background(), cfg, seed, nil); e != nil {
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
	if _, e := prepareSteam(context.Background(), cfg, seed, nil); e == nil {
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
	if _, e := prepareSteam(context.Background(), cfg, seed, nil); e == nil || !strings.Contains(e.Error(), "SDK64") {
		t.Fatalf("missing SDK not actionable: %v", e)
	}
}
