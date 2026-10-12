// SPDX-License-Identifier: GPL-3.0-or-later
package shared

import (
	"context"
	"github.com/s2script/s2script-cs2-egg/internal/runtimelease"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestPrivateFilesPreserved(t *testing.T) {
	s, _ := testStore(t)
	g, e := s.Build(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	root := t.TempDir()
	names := []string{"game/csgo/gameinfo.gi", "game/csgo/cfg/server.cfg", "game/csgo/addons/plugin.so", "game/csgo/data/state", "steamapps/appmanifest_730.acf", "game/bin/linuxsteamrt64/steamapps/bookkeeping", "game/csgo/pak01.vpk"}
	before := map[string]os.FileInfo{}
	for _, p := range names {
		put(t, root, p, "operator")
		before[p], _ = os.Stat(filepath.Join(root, p))
	}
	e = MaterializeLocked(context.Background(), root, s.root, g, "reservation", "copy", []string{"game/csgo/pak01.vpk"}, "")
	if e != nil {
		t.Fatal(e)
	}
	for _, p := range names {
		b, _ := os.ReadFile(filepath.Join(root, p))
		a, _ := os.Stat(filepath.Join(root, p))
		if string(b) != "operator" || !os.SameFile(before[p], a) || a.Mode().Perm() != 0600 {
			t.Fatalf("private file changed: %s", p)
		}
	}
}
func TestInterruptedMaterializationResumes(t *testing.T) {
	s, b := testStore(t)
	a, e := s.Build(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	root := t.TempDir()
	if e = MaterializeLocked(context.Background(), root, s.root, a, "r1", "symlink", nil, ""); e != nil {
		t.Fatal(e)
	}
	*b = "200"
	g, e := s.Build(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if e = MaterializeLocked(ctx, root, s.root, g, "r2", "symlink", nil, ""); e == nil {
		t.Fatal("cancel ignored")
	}
	if e = MaterializeLocked(context.Background(), root, s.root, g, "r2", "symlink", nil, ""); e != nil {
		t.Fatal(e)
	}
	v, _ := os.ReadFile(filepath.Join(root, "game/csgo/pak01.vpk"))
	if string(v) != "vpk-200" {
		t.Fatal("did not resume")
	}
}
func TestStandaloneMigrationMakesDistinctWritableInode(t *testing.T) {
	s, _ := testStore(t)
	g, e := s.Build(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	root := t.TempDir()
	if e = MaterializeLocked(context.Background(), root, s.root, g, "r", "symlink", nil, ""); e != nil {
		t.Fatal(e)
	}
	if e = StandaloneLocked(context.Background(), root); e != nil {
		t.Fatal(e)
	}
	p := filepath.Join(root, "game/csgo/pak01.vpk")
	a, _ := os.Stat(p)
	b, _ := os.Stat(filepath.Join(g.Root, "game/csgo/pak01.vpk"))
	if os.SameFile(a, b) || a.Mode().Perm()&0200 == 0 {
		t.Fatal("migration kept linked/read-only inode")
	}
	if e = os.WriteFile(p, []byte("updated standalone"), 0600); e != nil {
		t.Fatal(e)
	}
	old, _ := os.ReadFile(filepath.Join(g.Root, "game/csgo/pak01.vpk"))
	if string(old) != "vpk-100" {
		t.Fatal("migration mutated source")
	}
}
func TestMaterializationRejectsSymlinkParent(t *testing.T) {
	s, _ := testStore(t)
	g, e := s.Build(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	root := t.TempDir()
	outside := t.TempDir()
	if e = os.Symlink(outside, filepath.Join(root, "game")); e != nil {
		t.Fatal(e)
	}
	if e = MaterializeLocked(context.Background(), root, s.root, g, "r", "copy", nil, ""); e == nil {
		t.Fatal("followed private symlink parent")
	}
	entries, _ := os.ReadDir(outside)
	if len(entries) != 0 {
		t.Fatal("wrote outside root")
	}
}
func TestHardlinkUpdateDoesNotMutateOldInode(t *testing.T) {
	s, b := testStore(t)
	g, e := s.Build(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	dest := filepath.Join(t.TempDir(), "old.vpk")
	if e = os.Link(filepath.Join(g.Root, "game/csgo/pak01.vpk"), dest); e != nil {
		t.Fatal(e)
	}
	*b = "200"
	if _, e = s.Build(context.Background()); e != nil {
		t.Fatal(e)
	}
	old, _ := os.ReadFile(dest)
	info, _ := os.Stat(dest)
	if string(old) != "vpk-100" || info.Sys().(*syscall.Stat_t).Nlink != 2 {
		t.Fatal("build changed old shared inode")
	}
}

func TestObservedOverrideSurvivesMultipleGenerations(t *testing.T) {
	s, b := testStore(t)
	g, e := s.Build(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	root := t.TempDir()
	if e = MaterializeLocked(context.Background(), root, s.root, g, "r", "copy", nil, ""); e != nil {
		t.Fatal(e)
	}
	put(t, root, "game/bin/linuxsteamrt64/cs2", "operator override")
	for _, build := range []string{"200", "300"} {
		*b = build
		g, e = s.Build(context.Background())
		if e != nil {
			t.Fatal(e)
		}
		if e = MaterializeLocked(context.Background(), root, s.root, g, "r", "copy", nil, ""); e != nil {
			t.Fatal(e)
		}
	}
	v, _ := os.ReadFile(filepath.Join(root, "game/bin/linuxsteamrt64/cs2"))
	if string(v) != "operator override" {
		t.Fatal("observed override forgotten on next generation")
	}
}
func TestStandaloneClonesExplicitOverrideHardlinks(t *testing.T) {
	s, _ := testStore(t)
	g, e := s.Build(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	root := t.TempDir()
	put(t, root, "game/csgo/placeholder", "x")
	source := filepath.Join(g.Root, "game/csgo/pak01.vpk")
	target := filepath.Join(root, "game/csgo/pak01.vpk")
	if e = os.Link(source, target); e != nil {
		t.Fatal(e)
	}
	if e = MaterializeLocked(context.Background(), root, s.root, g, "r", "copy", []string{"game/csgo/pak01.vpk"}, ""); e != nil {
		t.Fatal(e)
	}
	if e = StandaloneLocked(context.Background(), root); e != nil {
		t.Fatal(e)
	}
	a, _ := os.Stat(source)
	b, _ := os.Stat(target)
	if os.SameFile(a, b) {
		t.Fatal("standalone migration left override attached to shared inode")
	}
}

type interruptedContext struct {
	context.Context
	remaining int
}

func (c *interruptedContext) Err() error {
	c.remaining--
	if c.remaining < 0 {
		return context.Canceled
	}
	return nil
}
func TestInterruptedAfterFirstReplacementResumes(t *testing.T) {
	s, b := testStore(t)
	a, e := s.Build(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	root := t.TempDir()
	if e = MaterializeLocked(context.Background(), root, s.root, a, "a", "copy", nil, ""); e != nil {
		t.Fatal(e)
	}
	*b = "200"
	n, e := s.Build(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	if e = MaterializeLocked(&interruptedContext{Context: context.Background(), remaining: 2}, root, s.root, n, "n", "copy", nil, ""); e == nil {
		t.Fatal("interrupted update completed")
	}
	if _, e = ReadPending(root); e != nil {
		t.Fatal("interrupted state not durable", e)
	}
	if e = MaterializeLocked(context.Background(), root, s.root, n, "n", "copy", nil, ""); e != nil {
		t.Fatal(e)
	}
	v, _ := os.ReadFile(filepath.Join(root, "game/csgo/pak01.vpk"))
	if string(v) != "vpk-200" {
		t.Fatal("partial update did not converge")
	}
}

func TestCopyModeReplacesExistingHardlink(t *testing.T) {
	s, _ := testStore(t)
	g, e := s.Build(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	root := t.TempDir()
	put(t, root, "game/csgo/placeholder", "x")
	source := filepath.Join(g.Root, "game/csgo/pak01.vpk")
	target := filepath.Join(root, "game/csgo/pak01.vpk")
	if e = os.Link(source, target); e != nil {
		t.Fatal(e)
	}
	if e = MaterializeLocked(context.Background(), root, s.root, g, "r", "copy", nil, ""); e != nil {
		t.Fatal(e)
	}
	a, _ := os.Stat(source)
	b, _ := os.Stat(target)
	if os.SameFile(a, b) || b.Mode().Perm()&0200 == 0 {
		t.Fatal("copy mode kept immutable shared inode")
	}
}

func TestInterruptedPreparationPreservesExplicitPolicy(t *testing.T) {
	s, _ := testStore(t)
	g, err := s.Build(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	path := "game/csgo/pak01.vpk"
	put(t, root, path, "operator override")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := MaterializeLocked(ctx, root, s.root, g, "r", "copy", []string{path}, ""); err == nil {
		t.Fatal("cancel ignored")
	}
	if err := materializeLocked(context.Background(), root, s.root, g, "r", "copy", nil, "", MountRoot); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(root, path))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "operator override" {
		t.Fatalf("pending override policy lost: %q", data)
	}
}

func TestStandaloneDetachesAllPreservedSharedFiles(t *testing.T) {
	for _, rel := range []string{"game/bin/linuxsteamrt64/cs2", "game/csgo/gameinfo.gi", ".steam/sdk64/steamclient.so"} {
		for _, kind := range []string{"symlink", "hardlink"} {
			t.Run(rel+"/"+kind, func(t *testing.T) {
				s, _ := testStore(t)
				g, err := s.Build(context.Background())
				if err != nil {
					t.Fatal(err)
				}
				root := t.TempDir()
				source, target := filepath.Join(g.Root, rel), filepath.Join(root, rel)
				if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
					t.Fatal(err)
				}
				link := os.Link
				if kind == "symlink" {
					link = os.Symlink
				}
				if err := link(source, target); err != nil {
					t.Fatal(err)
				}
				if err := MaterializeLocked(context.Background(), root, s.root, g, "r", "copy", []string{rel}, ""); err != nil {
					t.Fatal(err)
				}
				before, err := os.Stat(source)
				if err != nil {
					t.Fatal(err)
				}
				if err := StandaloneLocked(context.Background(), root); err != nil {
					t.Fatal(err)
				}
				after, err := os.Lstat(target)
				if err != nil {
					t.Fatal(err)
				}
				if !after.Mode().IsRegular() || os.SameFile(before, after) || after.Mode().Perm()&0200 == 0 || before.Mode().Perm()&0111 != after.Mode().Perm()&0111 {
					t.Fatalf("shared file not detached with executable/write mode preserved: %v", after.Mode())
				}
				if _, err := ReadLocal(root); !os.IsNotExist(err) {
					t.Fatal("successful migration retained marker", err)
				}
				if err := os.WriteFile(target, []byte("standalone mutation"), after.Mode().Perm()); err != nil {
					t.Fatal(err)
				}
				sourceInfo, err := os.Stat(source)
				if err != nil || sourceInfo.Size() != before.Size() || sourceInfo.Mode() != before.Mode() {
					t.Fatal("source inode changed", err)
				}
			})
		}
	}
}

func TestStandaloneRejectsUnrecognizedSymlinksWithoutReadingTargets(t *testing.T) {
	for _, name := range []string{"operator.vpk", "game/bin/override", "private-directory", "generation-directory"} {
		t.Run(name, func(t *testing.T) {
			s, _ := testStore(t)
			g, err := s.Build(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			root := t.TempDir()
			if err := MaterializeLocked(context.Background(), root, s.root, g, "r", "copy", nil, ""); err != nil {
				t.Fatal(err)
			}
			outside := t.TempDir()
			put(t, outside, "secret", "external bytes")
			destination := filepath.Join(outside, "secret")
			if name == "private-directory" {
				destination = outside
			}
			if name == "generation-directory" {
				destination = filepath.Join(g.Root, "game")
			}
			path := filepath.Join(root, name)
			if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(destination, path); err != nil {
				t.Fatal(err)
			}
			if err := StandaloneLocked(context.Background(), root); err == nil {
				t.Fatal("migration accepted arbitrary external link")
			}
			if _, err := ReadLocal(root); err != nil {
				t.Fatal("unsafe migration removed marker", err)
			}
			target, err := os.Readlink(path)
			if err != nil || target != destination {
				t.Fatal("unsafe link was followed/copied", target, err)
			}
		})
	}
}

func TestExplicitLocalOverrideSurvivesHelperMaterialization(t *testing.T) {
	s, build := testStore(t)
	old, err := s.Build(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	path := "game/csgo/pak01.vpk"
	put(t, root, path, "operator override")
	if err := MaterializeLocked(context.Background(), root, s.root, old, "local", "copy", []string{path}, ""); err != nil {
		t.Fatal(err)
	}
	*build = "200"
	next, err := s.Build(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := materializeLocked(context.Background(), root, s.root, next, "host", "copy", nil, "", MountRoot); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(root, path))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "operator override" {
		t.Fatalf("explicit local override overwritten by host attachment: %q", data)
	}
}

func TestStandaloneDetachesPreservedSharedBinary(t *testing.T) {
	s, _ := testStore(t)
	gen, err := s.Build(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	rel := "game/bin/linuxsteamrt64/cs2"
	target := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(gen.Root, rel), target); err != nil {
		t.Fatal(err)
	}
	if err := MaterializeLocked(context.Background(), root, s.root, gen, "local", "symlink", []string{rel}, ""); err != nil {
		t.Fatal(err)
	}
	if err := StandaloneLocked(context.Background(), root); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, LocalState)); !os.IsNotExist(err) {
		t.Fatal("migration did not remove marker", err)
	}
	info, err := os.Lstat(target)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		t.Fatal("standalone marker removed while explicit override still links published generation")
	}
}

func TestStandaloneNeverReplacesHeldLockInode(t *testing.T) {
	s, _ := testStore(t)
	g, err := s.Build(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	if err := MaterializeLocked(context.Background(), root, s.root, g, "r", "copy", nil, ""); err != nil {
		t.Fatal(err)
	}
	lease, err := runtimelease.Acquire(root)
	if err != nil {
		t.Fatal(err)
	}
	defer lease()
	lockPath := filepath.Join(root, ".runtime-lease")
	if err := os.Link(lockPath, filepath.Join(root, "lease-alias")); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(lockPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := StandaloneLocked(context.Background(), root); err == nil {
		t.Fatal("migration replaced held lock inode")
	}
	after, err := os.Stat(lockPath)
	if err != nil || !os.SameFile(before, after) {
		t.Fatal("runtime lock changed", err)
	}
	if _, err := ReadLocal(root); err != nil {
		t.Fatal("unsafe lock migration removed marker", err)
	}
}

func TestStandaloneInterruptedDetachmentRetainsMarkerAndResumes(t *testing.T) {
	s, _ := testStore(t)
	g, err := s.Build(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	rel := "game/bin/linuxsteamrt64/cs2"
	if err := os.MkdirAll(filepath.Join(root, filepath.Dir(rel)), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(g.Root, rel), filepath.Join(root, rel)); err != nil {
		t.Fatal(err)
	}
	if err := MaterializeLocked(context.Background(), root, s.root, g, "r", "symlink", []string{rel}, ""); err != nil {
		t.Fatal(err)
	}
	if err := StandaloneLocked(&interruptedContext{Context: context.Background(), remaining: 1}, root); err == nil {
		t.Fatal("interruption ignored")
	}
	if _, err := ReadLocal(root); err != nil {
		t.Fatal("interrupted migration removed safety marker", err)
	}
	if err := StandaloneLocked(context.Background(), root); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{rel, "game/csgo/pak01.vpk"} {
		a, err := os.Lstat(filepath.Join(root, path))
		if err != nil {
			t.Fatal(err)
		}
		b, err := os.Stat(filepath.Join(g.Root, path))
		if err != nil {
			t.Fatal(err)
		}
		if !a.Mode().IsRegular() || os.SameFile(a, b) || a.Mode().Perm()&0200 == 0 {
			t.Fatal("resumed migration left shared link", path)
		}
	}
}
