// SPDX-License-Identifier: GPL-3.0-or-later
package shared

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	node "github.com/s2script/s2script-cs2-egg/internal/nodetypes"
	"github.com/s2script/s2script-cs2-egg/internal/server"
)

func put(t *testing.T, root, name, value string) {
	t.Helper()
	if e := os.MkdirAll(filepath.Dir(filepath.Join(root, name)), 0755); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(filepath.Join(root, name), []byte(value), 0600); e != nil {
		t.Fatal(e)
	}
}
func fixture(t *testing.T, root, build string) {
	t.Helper()
	put(t, root, "steamapps/appmanifest_730.acf", `"appid" "730" "buildid" "`+build+`"`)
	put(t, root, "game/bin/linuxsteamrt64/cs2", "binary")
	if e := os.Chmod(filepath.Join(root, "game/bin/linuxsteamrt64/cs2"), 0755); e != nil {
		t.Fatal(e)
	}
	put(t, root, ".steam/sdk64/steamclient.so", "sdk64")
	put(t, root, "game/csgo/gameinfo.gi", "gameinfo")
	put(t, root, "game/csgo/pak01.vpk", "vpk-"+build)
	put(t, root, "game/csgo/cfg/server.cfg", "default")
	put(t, root, "game/bin/linuxsteamrt64/steamapps/bookkeeping", "private")
}
func testStore(t *testing.T) (*Store, *string) {
	t.Helper()
	build := "100"
	s, e := New(Options{Root: t.TempDir(), ReserveBytes: 1, Update: func(ctx context.Context, root string) (string, error) { fixture(t, root, build); return build, nil }})
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() {
		filepath.Walk(s.root, func(p string, i os.FileInfo, e error) error {
			if e == nil && i.IsDir() {
				os.Chmod(p, 0755)
			}
			return nil
		})
	})
	return s, &build
}
func TestBuildNeverMutatesPublishedGeneration(t *testing.T) {
	s, b := testStore(t)
	a, e := s.Build(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	old, e := os.Stat(filepath.Join(a.Root, "game/csgo/pak01.vpk"))
	if e != nil {
		t.Fatal(e)
	}
	*b = "200"
	n, e := s.Build(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	got, _ := os.ReadFile(filepath.Join(a.Root, "game/csgo/pak01.vpk"))
	fresh, _ := os.Stat(filepath.Join(n.Root, "game/csgo/pak01.vpk"))
	if string(got) != "vpk-100" || os.SameFile(old, fresh) {
		t.Fatal("new generation mutated/reused old inode")
	}
	if old.Mode().Perm()&0222 != 0 {
		t.Fatal("published file writable")
	}
}
func TestPruneRetainsLiveGeneration(t *testing.T) {
	s, b := testStore(t)
	a, e := s.Build(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	r, e := s.Reserve(context.Background(), node.Server{ID: "one"}, "run-a", "request-a")
	if e != nil {
		t.Fatal(e)
	}
	*b = "200"
	if _, e = s.Build(context.Background()); e != nil {
		t.Fatal(e)
	}
	restarted, e := New(s.options)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = restarted.Prune(context.Background()); e != nil {
		t.Fatal(e)
	}
	if _, e = os.Stat(a.Root); e != nil {
		t.Fatal("pending reference lost on restart", e)
	}
	if e = restarted.Commit(context.Background(), "one", r.ID); e != nil {
		t.Fatal(e)
	}
	if _, e = restarted.Prune(context.Background()); e != nil {
		t.Fatal(e)
	}
	if _, e = os.Stat(a.Root); e != nil {
		t.Fatal("committed reference pruned")
	}
}
func TestInterruptedBuildLeavesPreviousPublished(t *testing.T) {
	s, _ := testStore(t)
	a, e := s.Build(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	s.options.Update = func(ctx context.Context, root string) (string, error) {
		fixture(t, root, "999")
		return "", errors.New("interrupted")
	}
	if _, e = s.Build(context.Background()); e == nil {
		t.Fatal("failed staging published")
	}
	g, e := s.Current()
	if e != nil || g.ID != a.ID {
		t.Fatal("previous publication lost", e)
	}
}
func TestReferenceFailureDoesNotAcknowledge(t *testing.T) {
	s, _ := testStore(t)
	if _, e := s.Build(context.Background()); e != nil {
		t.Fatal(e)
	}
	if e := os.Mkdir(filepath.Join(s.root, "references.json"), 0700); e != nil {
		t.Fatal(e)
	}
	if _, e := s.Reserve(context.Background(), node.Server{ID: "one"}, "run", "id"); e == nil {
		t.Fatal("reservation acknowledged without durable reference")
	}
}

func TestIncompleteGenerationCannotPublish(t *testing.T) {
	s, _ := testStore(t)
	s.options.Update = func(ctx context.Context, root string) (string, error) {
		fixture(t, root, "100")
		os.Remove(filepath.Join(root, "game/csgo/pak01.vpk"))
		return "100", nil
	}
	if _, e := s.Build(context.Background()); e == nil {
		t.Fatal("incomplete generation published")
	}
	if _, e := s.Current(); e == nil {
		t.Fatal("incomplete generation became current")
	}
}
func TestDiskPreflightPreventsSteam(t *testing.T) {
	s, _ := testStore(t)
	s.options.ReserveBytes = ^uint64(0)
	called := false
	s.options.Update = func(context.Context, string) (string, error) { called = true; return "100", nil }
	if _, e := s.Build(context.Background()); e == nil || called {
		t.Fatal("Steam invoked without staging space")
	}
}

type liveFixture struct {
	servers []node.Server
	status  server.Status
}

func (l liveFixture) Servers() []node.Server      { return l.servers }
func (l liveFixture) Status(string) server.Status { return l.status }
func TestPruneRequiresRegisteredServerReconciliation(t *testing.T) {
	s, b := testStore(t)
	old, e := s.Build(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	*b = "200"
	if _, e = s.Build(context.Background()); e != nil {
		t.Fatal(e)
	}
	s.options.State = liveFixture{servers: []node.Server{{ID: "unreconciled"}}, status: server.Status{State: "lost_contact"}}
	if _, e = s.Prune(context.Background()); e == nil {
		t.Fatal("pruned before unknown registered server had durable references")
	}
	if _, e = os.Stat(old.Root); e != nil {
		t.Fatal("unreconciled server lost possible generation")
	}
}

func TestBuildRequiresSDKAndExecutable(t *testing.T) {
	for _, bad := range []string{"sdk", "executable"} {
		t.Run(bad, func(t *testing.T) {
			s, _ := testStore(t)
			s.options.Update = func(ctx context.Context, root string) (string, error) {
				fixture(t, root, "100")
				if bad == "sdk" {
					os.Remove(filepath.Join(root, ".steam/sdk64/steamclient.so"))
				} else {
					os.Chmod(filepath.Join(root, "game/bin/linuxsteamrt64/cs2"), 0600)
				}
				return "100", nil
			}
			if _, e := s.Build(context.Background()); e == nil {
				t.Fatal("incomplete runtime generation published")
			}
		})
	}
}

func TestBuildUsesSteamAdapterAndSeedsPrivateSDK(t *testing.T) {
	steamRoot := t.TempDir()
	put(t, steamRoot, "linux64/steamclient.so", "sdk64-from-dedicated-steamcmd")
	script := `#!/bin/sh
set -eu
[ "$1" = +force_install_dir ]
[ "$3" = +login ]
[ "$4" = anonymous ]
[ "$5" = +app_update ]
[ "$6" = 730 ]
[ "$7" = validate ]
[ "$8" = +quit ]
root=$2
mkdir -p "$root/steamapps" "$root/game/bin/linuxsteamrt64" "$root/game/csgo" "$root/.steam"
printf '"appid" "730" "buildid" "123"' > "$root/steamapps/appmanifest_730.acf"
printf binary > "$root/game/bin/linuxsteamrt64/cs2"
chmod 755 "$root/game/bin/linuxsteamrt64/cs2"
printf gameinfo > "$root/game/csgo/gameinfo.gi"
printf vpk > "$root/game/csgo/pak01.vpk"
ln -s /nonexistent-host-steam-bookkeeping "$root/.steam/root"
`
	put(t, steamRoot, "steamcmd.sh", script)
	if e := os.Chmod(filepath.Join(steamRoot, "steamcmd.sh"), 0755); e != nil {
		t.Fatal(e)
	}
	s, e := New(Options{Root: t.TempDir(), SteamCMD: filepath.Join(steamRoot, "steamcmd.sh"), ReserveBytes: 1})
	if e != nil {
		t.Fatal(e)
	}
	defer removeTree(s.root)
	g, e := s.Build(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	sdk, e := os.ReadFile(filepath.Join(g.Root, ".steam/sdk64/steamclient.so"))
	if e != nil || string(sdk) != "sdk64-from-dedicated-steamcmd" {
		t.Fatal("SDK not independently seeded", e)
	}
	if _, e = os.Lstat(filepath.Join(g.Root, ".steam/root")); !os.IsNotExist(e) {
		t.Fatal("host HOME bookkeeping link published")
	}
}
func TestInterruptedPublicationKeepsPreviousPointer(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses fixture directory permissions")
	}
	s, _ := testStore(t)
	old, e := s.Build(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	s.options.Update = func(ctx context.Context, root string) (string, error) {
		fixture(t, root, "200")
		return "200", os.Chmod(s.root, 0555)
	}
	defer os.Chmod(s.root, 0755)
	if _, e = s.Build(context.Background()); e == nil {
		t.Fatal("publication ignored current-pointer write failure")
	}
	if e = os.Chmod(s.root, 0755); e != nil {
		t.Fatal(e)
	}
	g, e := s.Current()
	if e != nil || g.ID != old.ID {
		t.Fatal("failed publication replaced previous pointer", e)
	}
}
