// SPDX-License-Identifier: GPL-3.0-or-later
package shared

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/s2script/s2script-cs2-egg/internal/artifact"
	"github.com/s2script/s2script-cs2-egg/internal/backup"
	node "github.com/s2script/s2script-cs2-egg/internal/nodetypes"
	"github.com/s2script/s2script-cs2-egg/internal/privateops"
	"github.com/s2script/s2script-cs2-egg/internal/protocol"
	"github.com/s2script/s2script-cs2-egg/internal/runtimelease"
	"github.com/s2script/s2script-cs2-egg/internal/workshop"
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
	}{{"backup", fixtureRuntimeUID, fixtureRuntimeUID, 0700}, {"ops", fixtureRuntimeUID, fixtureRuntimeUID, 0700}, {"workshop", fixtureDaemonUID, fixtureDaemonUID, 0755}, {"store", fixtureDaemonUID, fixtureDaemonUID, 0755}, {"volume", fixtureRuntimeUID, fixtureRuntimeUID, 0750}, {"volume/egg", fixtureRuntimeUID, fixtureRuntimeUID, 0770}, {"socket", fixtureRuntimeUID, fixtureRuntimeUID, 0770}} {
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
	for _, name := range []string{".runtime-lease", ".artifact-lock", "game/csgo/cfg/server.cfg", workshop.ACFPath} {
		info, e := os.Stat(filepath.Join(base, "volume", name))
		if e != nil || info.Mode().Perm() != 0600 || info.Sys().(*syscall.Stat_t).Uid != fixtureRuntimeUID {
			t.Fatalf("private identity/mode changed: %s %v", name, e)
		}
	}
	// An independent operations-only helper proves this route needs no shared
	// store and crosses the same actual UID boundary while preserving 0600 files.
	opsHelper := command("operations-helper", fixtureRuntimeUID)
	var opsLog bytes.Buffer
	opsHelper.Stdout = &opsLog
	opsHelper.Stderr = &opsLog
	if e = opsHelper.Start(); e != nil {
		t.Fatal(e)
	}
	defer func() {
		opsHelper.Process.Kill()
		opsHelper.Wait()
		if t.Failed() {
			t.Log(opsLog.String())
		}
	}()
	waitFile := func(name string) {
		t.Helper()
		until := time.Now().Add(5 * time.Second)
		for {
			if _, e := os.Stat(filepath.Join(base, name)); e == nil {
				return
			}
			if time.Now().After(until) {
				t.Fatalf("fixture did not create %s", name)
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	waitFile("socket/ops.sock")
	holder := command("operations-lease", fixtureRuntimeUID)
	var holdLog bytes.Buffer
	holder.Stdout = &holdLog
	holder.Stderr = &holdLog
	if e = holder.Start(); e != nil {
		t.Fatal(e)
	}
	defer func() {
		if holder.ProcessState == nil {
			holder.Process.Kill()
			holder.Wait()
		}
	}()
	waitFile("socket/lease-ready")
	for _, role := range []struct {
		name string
		uid  uint32
	}{{"operations-live", fixtureDaemonUID}, {"backup-live", fixtureDaemonUID}} {
		b, e := command(role.name, role.uid).CombinedOutput()
		if e != nil {
			t.Fatalf("%s: %v\n%s", role.name, e, b)
		}
		t.Logf("%s subprocess: %s", role.name, b)
	}
	if e = os.WriteFile(filepath.Join(base, "socket/lease-stop"), nil, 0644); e != nil {
		t.Fatal(e)
	}
	if e = holder.Wait(); e != nil {
		t.Fatalf("lease fixture: %v %s", e, holdLog.String())
	}
	for _, role := range []struct {
		name string
		uid  uint32
	}{{"operations-wrong-peer", fixtureRuntimeUID}, {"backup-wrong-peer", fixtureRuntimeUID}, {"backup-stopped", fixtureDaemonUID}} {
		b, e := command(role.name, role.uid).CombinedOutput()
		if e != nil {
			t.Fatalf("%s: %v\n%s", role.name, e, b)
		}
		t.Logf("%s stopped distinct-UID subprocess: %s", role.name, b)
	}
	b, e := command("operations-stopped", fixtureDaemonUID).CombinedOutput()
	if e != nil {
		t.Fatalf("operations-stopped: %v\n%s", e, b)
	}
	t.Logf("operations-only distinct-UID subprocess: %s", b)
	entries, e := os.ReadDir(filepath.Join(base, "ops/dumps"))
	if e != nil {
		t.Fatal(e)
	}
	dumps := 0
	for _, f := range entries {
		if strings.HasSuffix(f.Name(), ".dmp") {
			info, e := f.Info()
			if e != nil || info.Mode().Perm() != 0600 || info.Sys().(*syscall.Stat_t).Uid != fixtureRuntimeUID {
				t.Fatal("private retained dump identity changed", e)
			}
			dumps++
		}
	}
	if dumps != 1 {
		t.Fatal("missing private dump", dumps)
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
	workshopRoot := filepath.Join(base, "workshop")
	socket := filepath.Join(base, "socket/helper.sock")
	switch role {
	case "operations-helper":
		if os.Geteuid() != fixtureRuntimeUID {
			t.Fatal("wrong operations helper UID")
		}
		put(t, volume, "dumps/private.dmp", "sensitive fixture binary\x00")
		e := RunHelper(context.Background(), HelperConfig{Volume: volume, Socket: filepath.Join(base, "socket/ops.sock"), NodeUID: fixtureDaemonUID, RuntimeUID: fixtureRuntimeUID, AccessGID: fixtureRuntimeUID, Backup: &backup.Policy{Root: filepath.Join(base, "backup"), RootID: "fixture", ServerID: "one", MinFreeBytes: 64 << 20}, Operations: &privateops.Policy{Enabled: true, Root: filepath.Join(base, "ops"), ServerID: "one", DumpSource: "dumps", Dumps: true}})
		t.Fatal(e)
	case "backup-live", "backup-stopped", "backup-wrong-peer":
		srv := node.Server{ID: "one", Volume: volume, Helper: &node.PrivateHelper{Socket: filepath.Join(base, "socket/ops.sock"), UID: fixtureRuntimeUID, GID: fixtureRuntimeUID}}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		m, e := backup.Create(ctx, srv, backup.Options{})
		if role != "backup-stopped" {
			if e == nil {
				t.Fatal("live or unauthorized backup accepted")
			}
			return
		}
		if e != nil {
			t.Fatal(e)
		}
		if _, e = os.ReadFile(filepath.Join(volume, "game/csgo/cfg/server.cfg")); e == nil {
			t.Fatal("node read private backup input directly")
		}
		if _, e = os.ReadFile(filepath.Join(base, "backup", "archives", m.BackupID, "archive.tar.gz")); e == nil {
			t.Fatal("node read private archive directly")
		}
		p, e := backup.PreviewRestore(ctx, srv, m.BackupID)
		if e != nil {
			t.Fatal(e)
		}
		if e = backup.Restore(ctx, srv, p); e != nil {
			t.Fatal(e)
		}
		i, e := os.Stat(filepath.Join(volume, "game/csgo/cfg/server.cfg"))
		if e != nil || i.Mode().Perm() != 0600 || i.Sys().(*syscall.Stat_t).Uid != fixtureRuntimeUID || i.Sys().(*syscall.Stat_t).Gid != fixtureRuntimeUID {
			t.Fatal("backup restore changed private metadata", e)
		}
	case "operations-lease":
		release, e := runtimelease.Acquire(volume)
		if e != nil {
			t.Fatal(e)
		}
		defer release()
		if e = os.WriteFile(filepath.Join(base, "socket/lease-ready"), nil, 0644); e != nil {
			t.Fatal(e)
		}
		until := time.Now().Add(15 * time.Second)
		for {
			if _, e = os.Stat(filepath.Join(base, "socket/lease-stop")); e == nil {
				return
			}
			if time.Now().After(until) {
				t.Fatal("lease stop timeout")
			}
			time.Sleep(10 * time.Millisecond)
		}
	case "operations-live", "operations-stopped", "operations-wrong-peer":
		expected := fixtureDaemonUID
		if role == "operations-wrong-peer" {
			expected = fixtureRuntimeUID
		}
		if os.Geteuid() != expected {
			t.Fatal("wrong operation client UID")
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		r, e := privateops.Call(ctx, filepath.Join(base, "socket/ops.sock"), fixtureRuntimeUID, privateops.Request{Operation: "dump-retain", RequestID: "crossuid-fixed", ID: "private.dmp"})
		if role != "operations-stopped" {
			if e == nil {
				t.Fatal("live or wrong-peer operation accepted")
			}
			return
		}
		if e != nil || r.ID == "" || !r.Sensitive {
			t.Fatal("stopped private operation failed", r, e)
		}
		if _, e = os.ReadFile(filepath.Join(volume, "dumps/private.dmp")); e == nil {
			t.Fatal("node UID read private source directly")
		}
		read, e := privateops.Call(ctx, filepath.Join(base, "socket/ops.sock"), fixtureRuntimeUID, privateops.Request{Operation: "read", Namespace: "dumps", ID: r.ID, Limit: 65536})
		if e != nil || string(read.Data) != "sensitive fixture binary\x00" || !read.Sensitive {
			t.Fatal("private export failed", read, e)
		}
	case "helper":
		if os.Geteuid() != fixtureRuntimeUID {
			t.Fatal("wrong helper UID")
		}
		put(t, volume, "game/csgo/cfg/server.cfg", "private operator configuration")
		files := crossUIDWorkshopFiles()
		var size int
		for n, b := range files {
			put(t, volume, workshop.ContentPath+"/123/"+n, string(b))
			size += len(b)
		}
		put(t, volume, workshop.ACFPath, fmt.Sprintf(`"AppWorkshop" { "appid" "730" "WorkshopItemsInstalled" {"123" {"manifest" "55" "size" "%d"}} "WorkshopItemDetails" {"123" {"manifest" "55" "latest_manifest" "55"}} }`, size))
		e := RunHelper(context.Background(), HelperConfig{Volume: volume, SharedRoot: store, WorkshopRoot: workshopRoot, WorkshopMode: "hardlink", WorkshopItems: []string{"123"}, Socket: socket, NodeUID: fixtureDaemonUID, RuntimeUID: fixtureRuntimeUID, AccessGID: fixtureRuntimeUID})
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
		srv := node.Server{ID: "one", Volume: volume, Helper: &node.PrivateHelper{Socket: socket, UID: fixtureRuntimeUID, GID: fixtureRuntimeUID}}
		ws, e := workshop.New(workshop.Options{Root: workshopRoot, Servers: []node.Server{srv}, TrustedImporters: []string{"one"}, Items: []string{"123"}, Mode: "hardlink", Details: func(context.Context, []string) (map[string]workshop.Detail, error) {
			return map[string]workshop.Detail{"123": {ID: "123", Revision: "55", AppID: 730}}, nil
		}})
		if e != nil {
			t.Fatal(e)
		}
		if _, e = os.ReadFile(filepath.Join(volume, workshop.ACFPath)); e == nil {
			t.Fatal("daemon read private ACF")
		}
		if e = ws.Start(context.Background()); e != nil {
			t.Fatal(e)
		}
		defer (&workshop.Module{Store: ws}).Close(context.Background())
		result, e := ws.Sync(context.Background(), srv)
		if e != nil {
			t.Fatal(e)
		}
		if len(result.Imported) != 1 || len(result.Linked) != 1 {
			t.Fatal("workshop private import/attach missing", result)
		}
		for name := range crossUIDWorkshopFiles() {
			src, e := os.Stat(filepath.Join(workshopRoot, "generations/123/55", name))
			if e != nil {
				t.Fatal(e)
			}
			dst, e := os.Stat(filepath.Join(volume, workshop.ContentPath, "123", name))
			if e != nil || !os.SameFile(src, dst) || dst.Sys().(*syscall.Stat_t).Uid != fixtureDaemonUID || dst.Mode().Perm() != 0444 {
				t.Fatal("workshop hardlink ownership/content contract", e)
			}
		}
		wg, e := ws.Generation("123", "55")
		if e != nil {
			t.Fatal(e)
		}
		links, e := ws.Handle(context.Background(), srv.ID, protocol.WorkshopRequest{Operation: "link-info", ItemID: wg.ItemID, Revision: wg.Revision, Digest: wg.Digest})
		if e != nil {
			t.Fatal(e)
		}
		// Transfer the real daemon-derived inode metadata to the subsequent runtime
		// probe. Socket authentication is covered by the live Sync session above;
		// this probe isolates boot verification of those actual distinct-UID inodes.
		workshopData, e := json.Marshal(struct {
			Generation workshop.Generation
			Source     protocol.WorkshopResult
		}{wg, links})
		if e != nil {
			t.Fatal(e)
		}
		if e = os.WriteFile(filepath.Join(workshopRoot, "fixture-workshop.json"), workshopData, 0644); e != nil {
			t.Fatal(e)
		}
		if e = writeJSON(store, "fixture-generation.json", g, 0644); e != nil {
			t.Fatal(e)
		}
	case "runtime-probe":
		if os.Geteuid() != fixtureRuntimeUID {
			t.Fatal("wrong probe UID")
		}
		for name := range crossUIDWorkshopFiles() {
			p := filepath.Join(volume, workshop.ContentPath, "123", name)
			if e := os.Chmod(p, 0644); e == nil {
				t.Fatal("runtime chmodded workshop source")
			}
			if e := os.WriteFile(p, []byte("bad"), 0600); e == nil {
				t.Fatal("runtime mutated workshop source")
			}
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
		var workshopReceipt struct {
			Generation protocol.WorkshopGeneration
			Source     protocol.WorkshopResult
		}
		b, e = os.ReadFile(filepath.Join(workshopRoot, "fixture-workshop.json"))
		if e != nil || json.Unmarshal(b, &workshopReceipt) != nil {
			t.Fatal("missing workshop source identity", e)
		}
		result, e := workshop.PrepareLocked(context.Background(), workshop.LocalOptions{Root: volume, Items: []string{"123"}, Mode: "hardlink"}, func(ctx context.Context, r protocol.WorkshopRequest) (protocol.WorkshopResult, error) {
			switch r.Operation {
			case "link-info":
				return workshopReceipt.Source, nil
			case "select", "commit":
				return protocol.WorkshopResult{State: "ready", Generation: &workshopReceipt.Generation}, nil
			case "finish":
				return protocol.WorkshopResult{State: "finished"}, nil
			default:
				return protocol.WorkshopResult{}, fmt.Errorf("unexpected boot operation %s", r.Operation)
			}
		})
		if e != nil || len(result.Linked) != 1 || result.BytesSaved == 0 {
			t.Fatal("boot verification of actual workshop hardlinks failed", result, e)
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

func crossUIDWorkshopFiles() map[string][]byte {
	tree := []byte("txt\x00 \x00a\x00")
	entry := make([]byte, 18)
	binary.LittleEndian.PutUint32(entry[12:], 4)
	binary.LittleEndian.PutUint16(entry[16:], 0xffff)
	tree = append(tree, entry...)
	tree = append(tree, 0, 0, 0)
	header := make([]byte, 12)
	binary.LittleEndian.PutUint32(header, 0x55aa1234)
	binary.LittleEndian.PutUint32(header[4:], 1)
	binary.LittleEndian.PutUint32(header[8:], uint32(len(tree)))
	return map[string][]byte{"123_dir.vpk": append(header, tree...), "123_000.vpk": []byte("data")}
}
