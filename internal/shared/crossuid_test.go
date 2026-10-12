// SPDX-License-Identifier: GPL-3.0-or-later
package shared

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"github.com/s2script/s2script-cs2-egg/internal/artifact"
	node "github.com/s2script/s2script-cs2-egg/internal/nodetypes"
	"github.com/s2script/s2script-cs2-egg/internal/runtimelease"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

const fixtureDaemonUID = 65532
const fixtureRuntimeUID = 65533

// This test is explicitly opt-in on ephemeral CI. It never alters accounts,
// mounts, sysctls, services or any path outside its freshly allocated root.
func TestCrossUIDHelperFixture(t *testing.T) {
	if os.Getenv("S2SHARED_PRIVILEGED_TEST") != "1" || os.Geteuid() != 0 {
		t.Skip("requires explicit ephemeral-CI root fixture; ordinary tests do not prove cross-UID acceptance")
	}
	base, e := os.MkdirTemp("/tmp", "s2shared-crossuid-")
	if e != nil {
		t.Fatal(e)
	}
	if e = validateFixtureRoot(base); e != nil {
		t.Fatal(e)
	}
	defer func() {
		if e := validateFixtureRoot(base); e != nil {
			t.Error(e)
			return
		}
		filepath.WalkDir(base, func(p string, d os.DirEntry, e error) error {
			if e == nil && d.IsDir() {
				os.Chmod(p, 0755)
			}
			return nil
		})
		os.RemoveAll(base)
	}()
	if e = os.Chmod(base, 0755); e != nil {
		t.Fatal(e)
	}
	for _, v := range []struct {
		name     string
		uid, gid int
		mode     os.FileMode
	}{{"store", fixtureDaemonUID, fixtureDaemonUID, 0755}, {"volume", fixtureRuntimeUID, fixtureRuntimeUID, 0750}, {"volume/egg", fixtureRuntimeUID, fixtureRuntimeUID, 0770}, {"socket", fixtureRuntimeUID, fixtureRuntimeUID, 0770}} {
		p := filepath.Join(base, v.name)
		if e = os.Mkdir(p, v.mode); e != nil {
			t.Fatal(e)
		}
		if e = os.Chown(p, v.uid, v.gid); e != nil {
			t.Fatal(e)
		}
		if e = os.Chmod(p, v.mode); e != nil {
			t.Fatal(e)
		}
	}
	executable, e := os.Executable()
	if e != nil {
		t.Fatal(e)
	}
	in, e := os.Open(executable)
	if e != nil {
		t.Fatal(e)
	}
	out, e := os.OpenFile(filepath.Join(base, "fixture.test"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0755)
	if e != nil {
		t.Fatal(e)
	}
	_, e = io.Copy(out, in)
	e = errors.Join(e, in.Close(), out.Close())
	if e != nil {
		t.Fatal(e)
	}
	command := func(role string, uid uint32) *exec.Cmd {
		c := exec.Command(filepath.Join(base, "fixture.test"), "-test.run=^TestCrossUIDFixtureChild$", "-test.v")
		c.Env = append(os.Environ(), "S2SHARED_FIXTURE_ROOT="+base, "S2SHARED_FIXTURE_CHILD="+role)
		c.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: uid, Gid: uid, Groups: []uint32{fixtureRuntimeUID}}}
		return c
	}
	helper := command("helper", fixtureRuntimeUID)
	var helperLog bytes.Buffer
	helper.Stdout = &helperLog
	helper.Stderr = &helperLog
	if e = helper.Start(); e != nil {
		t.Fatal(e)
	}
	defer func() {
		helper.Process.Kill()
		helper.Wait()
		if t.Failed() {
			t.Log(helperLog.String())
		}
	}()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, e = os.Stat(filepath.Join(base, "socket/helper.sock")); e == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("helper did not start")
		}
		time.Sleep(10 * time.Millisecond)
	}
	for _, role := range []struct {
		name string
		uid  uint32
	}{{"attach", fixtureDaemonUID}, {"runtime-probe", fixtureRuntimeUID}} {
		c := command(role.name, role.uid)
		b, e := c.CombinedOutput()
		if e != nil {
			t.Fatalf("%s: %v\n%s", role.name, e, b)
		}
		t.Logf("%s subprocess: %s", role.name, b)
	}
	for _, name := range []string{".runtime-lease", ".artifact-lock", "game/csgo/cfg/server.cfg"} {
		info, e := os.Stat(filepath.Join(base, "volume", name))
		if e != nil || info.Mode().Perm() != 0600 || info.Sys().(*syscall.Stat_t).Uid != fixtureRuntimeUID {
			t.Fatalf("private identity/mode changed: %s %v", name, e)
		}
	}
	info, e := os.Stat(filepath.Join(base, "volume/game/csgo/pak01.vpk"))
	if e != nil || info.Sys().(*syscall.Stat_t).Uid != fixtureDaemonUID || info.Mode().Perm() != 0444 {
		t.Fatal("hardlink ownership/mode contract broken", e)
	}
}
func validateFixtureRoot(base string) error {
	if filepath.Dir(base) != "/tmp" || !strings.HasPrefix(filepath.Base(base), "s2shared-crossuid-") {
		return errors.New("unexpected fixture root")
	}
	info, e := os.Lstat(base)
	if e != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Sys().(*syscall.Stat_t).Uid != 0 {
		return errors.New("fixture root is not an isolated root-owned directory")
	}
	real, e := filepath.EvalSymlinks(base)
	if e != nil || real != base {
		return errors.New("fixture root containment failed")
	}
	return nil
}
func TestCrossUIDFixtureChild(t *testing.T) {
	role := os.Getenv("S2SHARED_FIXTURE_CHILD")
	if role == "" {
		t.Skip("subprocess only")
	}
	base := os.Getenv("S2SHARED_FIXTURE_ROOT")
	if os.Getenv("S2SHARED_PRIVILEGED_TEST") != "1" {
		t.Fatal("fixture not explicitly enabled")
	}
	if e := validateFixtureRoot(base); e != nil {
		t.Fatal(e)
	}
	volume := filepath.Join(base, "volume")
	store := filepath.Join(base, "store")
	socket := filepath.Join(base, "socket/helper.sock")
	switch role {
	case "helper":
		if os.Geteuid() != fixtureRuntimeUID {
			t.Fatal("wrong helper UID")
		}
		put(t, volume, "game/csgo/cfg/server.cfg", "private operator configuration")
		e := RunHelper(context.Background(), HelperConfig{Volume: volume, SharedRoot: store, Socket: socket, NodeUID: fixtureDaemonUID, RuntimeUID: fixtureRuntimeUID, AccessGID: fixtureRuntimeUID})
		t.Fatal(e)
	case "attach":
		if os.Geteuid() != fixtureDaemonUID {
			t.Fatal("wrong daemon UID")
		}
		s, e := New(Options{Root: store, ReserveBytes: 1, Helpers: map[string]HelperEndpoint{"one": {Socket: socket, UID: fixtureRuntimeUID, GID: fixtureRuntimeUID}}, Update: func(ctx context.Context, root string) (string, error) { fixture(t, root, "100"); return "100", nil }})
		if e != nil {
			t.Fatal(e)
		}
		g, e := s.Build(context.Background())
		if e != nil {
			t.Fatal(e)
		}
		if e = s.Attach(context.Background(), node.Server{ID: "one", Volume: volume}, g.ID, "copy"); e != nil {
			t.Fatal(e)
		}
		if e = s.Attach(context.Background(), node.Server{ID: "one", Volume: volume}, g.ID, "hardlink"); e != nil {
			t.Fatal(e)
		}
		if u, e := runtimelease.Acquire(volume); e == nil {
			u()
			t.Fatal("daemon unexpectedly opened private runtime lease")
		}
		a, _ := os.Stat(filepath.Join(g.Root, "game/csgo/pak01.vpk"))
		b, _ := os.Stat(filepath.Join(volume, "game/csgo/pak01.vpk"))
		if !os.SameFile(a, b) {
			t.Fatal("helper did not install actual hardlink")
		}
		if e = writeJSON(store, "fixture-generation.json", g, 0644); e != nil {
			t.Fatal(e)
		}
	case "runtime-probe":
		if os.Geteuid() != fixtureRuntimeUID {
			t.Fatal("wrong probe UID")
		}
		p := filepath.Join(volume, "game/csgo/pak01.vpk")
		if e := os.WriteFile(p, []byte("mutated"), 0600); e == nil {
			t.Fatal("runtime wrote immutable source through hardlink")
		}
		if e := os.Chmod(p, 0644); e == nil {
			t.Fatal("runtime chmodded immutable source through hardlink")
		}
		var g Generation
		b, e := os.ReadFile(filepath.Join(store, "fixture-generation.json"))
		if e != nil || json.Unmarshal(b, &g) != nil {
			t.Fatal("missing generation receipt", e)
		}
		release, e := runtimelease.Acquire(volume)
		if e != nil {
			t.Fatal(e)
		}
		defer release()
		unlock, e := artifact.Acquire(volume)
		if e != nil {
			t.Fatal(e)
		}
		defer unlock()
		if e = artifact.Recover(volume); e != nil {
			t.Fatal(e)
		}
		if e = MaterializeLocked(context.Background(), volume, store, g, "existing", "hardlink", nil, ""); e != nil {
			t.Fatal("local verification of host hardlinks failed", e)
		}
		bytes, e := os.ReadFile(filepath.Join(store, "generations", g.ID, "game/csgo/pak01.vpk"))
		if e != nil || string(bytes) != "vpk-100" {
			t.Fatal("source changed", e)
		}
	default:
		t.Fatal("unknown fixture role")
	}
}
