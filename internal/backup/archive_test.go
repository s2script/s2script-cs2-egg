// SPDX-License-Identifier: GPL-3.0-or-later
package backup

import (
	"context"
	"errors"
	"github.com/s2script/s2script-cs2-egg/internal/runtimelease"
	"github.com/s2script/s2script-cs2-egg/internal/storeio"
	"os"
	"path/filepath"
	"testing"
)

func fixture(t *testing.T) *Store {
	t.Helper()
	root := t.TempDir()
	volume := t.TempDir()
	os.Chmod(root, 0700)
	s, e := Open(volume, Policy{Root: root, RootID: "fixture-root", ServerID: "server-a", MinFreeBytes: 64 << 20})
	if e != nil {
		t.Fatal(e)
	}
	return s
}
func put(t *testing.T, s *Store, p, v string) {
	t.Helper()
	f := filepath.Join(s.volume, p)
	if e := os.MkdirAll(filepath.Dir(f), 0700); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(f, []byte(v), 0640); e != nil {
		t.Fatal(e)
	}
}
func TestBackupIncludesPrivateStateNotSharedGame(t *testing.T) {
	s := fixture(t)
	for _, p := range []string{"egg/server.json", "egg/plugins.json", "egg/plugins.lock.json", "game/csgo/cfg/server.cfg", "game/csgo/addons/s2script/data/db.sqlite", "game/csgo/custom/my.vpk"} {
		put(t, s, p, "private")
	}
	put(t, s, "game/csgo/pak01_dir.vpk", "bulk")
	put(t, s, "egg/.s2shared.json", "control")
	m, e := s.Create(context.Background(), Options{})
	if e != nil {
		t.Fatal(e)
	}
	if len(m.Files) != 6 {
		t.Fatalf("selected %+v", m.Files)
	}
	for _, f := range m.Files {
		if f.Path == "game/csgo/pak01_dir.vpk" || f.Path == "egg/.s2shared.json" {
			t.Fatal("included shared/control")
		}
	}
}
func TestConsistentBackupRequiresStoppedOrQuiescedServer(t *testing.T) {
	s := fixture(t)
	put(t, s, "egg/server.json", "private")
	release, e := runtimelease.Acquire(s.volume)
	if e != nil {
		t.Fatal(e)
	}
	defer release()
	if _, e = s.Create(context.Background(), Options{}); e == nil {
		t.Fatal("accepted running backup")
	}
	m, e := s.Create(context.Background(), Options{Mode: "live-best-effort"})
	if e != nil || m.Mode != "live-best-effort" {
		t.Fatalf("live mode: %+v %v", m, e)
	}
}
func TestLowDiskLeavesPreviousBackupIntact(t *testing.T) {
	s := fixture(t)
	put(t, s, "egg/server.json", "private")
	m, e := s.Create(context.Background(), Options{})
	if e != nil {
		t.Fatal(e)
	}
	s.free = func(string) (uint64, error) { return 0, nil }
	if _, e = s.Create(context.Background(), Options{}); e == nil {
		t.Fatal("ignored disk reserve")
	}
	if _, e = s.manifest(m.BackupID); e != nil {
		t.Fatal("previous backup lost", e)
	}
}
func TestRestoreRefusesRunningGame(t *testing.T) {
	s := fixture(t)
	put(t, s, "egg/server.json", "old")
	m, e := s.Create(context.Background(), Options{})
	if e != nil {
		t.Fatal(e)
	}
	p, e := s.Preview(context.Background(), m.BackupID)
	if e != nil {
		t.Fatal(e)
	}
	release, e := runtimelease.Acquire(s.volume)
	if e != nil {
		t.Fatal(e)
	}
	defer release()
	if e = s.Restore(context.Background(), p.ID, p.PlanDigest); e == nil {
		t.Fatal("restored running game")
	}
}
func TestRestoreTraversalRejected(t *testing.T) {
	s := fixture(t)
	for _, p := range []string{"../outside", "/etc/passwd", "game/csgo/../../evil", "egg/.artifact-txn/file", "egg/.s2shared.json", ".runtime-lease"} {
		if s.allowed(p) {
			t.Fatalf("allowed %q", p)
		}
	}
	if _, e := s.Preview(context.Background(), "../../outside"); e == nil {
		t.Fatal("accepted archive traversal")
	}
}
func TestRestoreKeepsUnrelatedFilesAndRecoveryCopy(t *testing.T) {
	s := fixture(t)
	put(t, s, "egg/server.json", "old")
	m, e := s.Create(context.Background(), Options{})
	if e != nil {
		t.Fatal(e)
	}
	put(t, s, "egg/server.json", "new")
	put(t, s, "game/csgo/cfg/unrelated.cfg", "keep")
	p, e := s.Preview(context.Background(), m.BackupID)
	if e != nil {
		t.Fatal(e)
	}
	if e = s.Restore(context.Background(), p.ID, p.PlanDigest); e != nil {
		t.Fatal(e)
	}
	for path, want := range map[string]string{"egg/server.json": "old", "game/csgo/cfg/unrelated.cfg": "keep"} {
		b, e := os.ReadFile(filepath.Join(s.volume, path))
		if e != nil || string(b) != want {
			t.Fatalf("%s = %q %v", path, b, e)
		}
	}
	i, _ := os.Stat(filepath.Join(s.volume, "egg/server.json"))
	if i.Mode().Perm() != 0640 {
		t.Fatal("permissions changed")
	}
	r, e := s.manifest("recovery-" + p.ID)
	if e != nil || len(r.Files) != 1 {
		t.Fatalf("recovery missing %+v %v", r, e)
	}
	if e = s.Restore(context.Background(), p.ID, p.PlanDigest); e == nil {
		t.Fatal("replayed consumed restore")
	}
}
func TestRestoreRejectsChangedPreviewAndConfirmation(t *testing.T) {
	s := fixture(t)
	put(t, s, "egg/server.json", "old")
	m, e := s.Create(context.Background(), Options{})
	if e != nil {
		t.Fatal(e)
	}
	p, e := s.Preview(context.Background(), m.BackupID)
	if e != nil {
		t.Fatal(e)
	}
	if e = s.Restore(context.Background(), p.ID, "bad"); e == nil {
		t.Fatal("no digest confirmation")
	}
	put(t, s, "egg/server.json", "changed")
	if e = s.Restore(context.Background(), p.ID, p.PlanDigest); e == nil {
		t.Fatal("stale preview accepted")
	}
}
func TestSelectedSymlinkFailsWithoutReadingTarget(t *testing.T) {
	s := fixture(t)
	put(t, s, "egg/server.json", "old")
	os.Remove(filepath.Join(s.volume, "egg/server.json"))
	os.Symlink("/etc/passwd", filepath.Join(s.volume, "egg/server.json"))
	if _, e := s.Create(context.Background(), Options{}); e == nil {
		t.Fatal("followed private symlink")
	}
}

func TestRealPluginPoliciesAndPersistedOverridesAreBackedUp(t *testing.T) {
	s := fixture(t)
	for _, p := range []string{"s2script-plugins.json", "s2script-plugins.lock.json", "game/csgo/pak_custom.vpk"} {
		put(t, s, p, "private")
	}
	put(t, s, ".s2shared.json", `{"overrides":["game/csgo/pak_custom.vpk"],"generation":{"id":"fixture","build_id":"123"}}`)
	m, e := s.Create(context.Background(), Options{})
	if e != nil {
		t.Fatal(e)
	}
	if len(m.Files) != 3 {
		t.Fatalf("lost policies or persisted overrides: %+v", m.Files)
	}
}
func TestLiveBackupRefusesPendingSharedTransaction(t *testing.T) {
	s := fixture(t)
	put(t, s, "egg/server.json", "config")
	put(t, s, ".s2shared-pending.json", `{}`)
	if _, e := s.Create(context.Background(), Options{Mode: "live-best-effort"}); e == nil {
		t.Fatal("accepted pending shared transaction")
	}
}
func TestRecoveryQuotaFailsBeforeMutationAndCanBeReleased(t *testing.T) {
	s := fixture(t)
	s.policy.Recoveries = 1
	put(t, s, "egg/server.json", "backup")
	m, e := s.Create(context.Background(), Options{})
	if e != nil {
		t.Fatal(e)
	}
	put(t, s, "egg/server.json", "first")
	p, e := s.Preview(context.Background(), m.BackupID)
	if e != nil {
		t.Fatal(e)
	}
	if e = s.Restore(context.Background(), p.ID, p.PlanDigest); e != nil {
		t.Fatal(e)
	}
	put(t, s, "egg/server.json", "second")
	p2, e := s.Preview(context.Background(), m.BackupID)
	if e != nil {
		t.Fatal(e)
	}
	if e = s.Restore(context.Background(), p2.ID, p2.PlanDigest); e == nil {
		t.Fatal("ignored pinned quota")
	}
	b, _ := os.ReadFile(filepath.Join(s.volume, "egg/server.json"))
	if string(b) != "second" {
		t.Fatal("mutated before recovery quota")
	}
	r, e := s.manifest("recovery-" + p.ID)
	if e != nil {
		t.Fatal(e)
	}
	if e = s.ReleaseRecovery(context.Background(), r.BackupID, "wrong"); e == nil {
		t.Fatal("released without confirmation")
	}
	if e = s.ReleaseRecovery(context.Background(), r.BackupID, r.ArchiveSHA256); e != nil {
		t.Fatal(e)
	}
	if e = s.Restore(context.Background(), p2.ID, p2.PlanDigest); e != nil {
		t.Fatal(e)
	}
}
func TestChangedArchiveAndParentAreRejected(t *testing.T) {
	t.Run("archive", func(t *testing.T) {
		s := fixture(t)
		put(t, s, "egg/server.json", "old")
		m, e := s.Create(context.Background(), Options{})
		if e != nil {
			t.Fatal(e)
		}
		p, e := s.Preview(context.Background(), m.BackupID)
		if e != nil {
			t.Fatal(e)
		}
		os.WriteFile(filepath.Join(s.policy.Root, "archives", m.BackupID, "archive.tar.gz"), []byte("corrupt"), 0600)
		if e = s.Restore(context.Background(), p.ID, p.PlanDigest); e == nil {
			t.Fatal("accepted replaced archive")
		}
	})
	t.Run("parent", func(t *testing.T) {
		s := fixture(t)
		put(t, s, "egg/server.json", "old")
		m, e := s.Create(context.Background(), Options{})
		if e != nil {
			t.Fatal(e)
		}
		p, e := s.Preview(context.Background(), m.BackupID)
		if e != nil {
			t.Fatal(e)
		}
		os.Rename(filepath.Join(s.volume, "egg"), filepath.Join(s.volume, "previous"))
		os.Symlink(filepath.Join(s.volume, "previous"), filepath.Join(s.volume, "egg"))
		if e = s.Restore(context.Background(), p.ID, p.PlanDigest); e == nil {
			t.Fatal("followed replaced parent")
		}
	})
}

func TestInterruptedHelperRequestNeverReexecutesBackup(t *testing.T) {
	s := fixture(t)
	put(t, s, "egg/server.json", "private")
	r := Request{Operation: "create", ServerID: "server-a", RequestID: "interrupted"}
	key := storeio.Hash([]byte("server-a\x00interrupted"))
	if e := storeio.Save(s.policy.Root, "receipts/"+key+".json", operationRecord{Input: r, Reply: Reply{ID: key, State: "executing"}}); e != nil {
		t.Fatal(e)
	}
	result, e := s.Handle(context.Background(), r)
	if e != nil || result.State != "uncertain" {
		t.Fatalf("%+v %v", result, e)
	}
	list, e := s.List()
	if e != nil || len(list) != 0 {
		t.Fatal("interrupted operation replayed", list, e)
	}
}
func TestDuplicateRestoreRequestReturnsReceiptWithoutMutation(t *testing.T) {
	s := fixture(t)
	put(t, s, "egg/server.json", "original")
	m, e := s.Create(context.Background(), Options{})
	if e != nil {
		t.Fatal(e)
	}
	put(t, s, "egg/server.json", "changed")
	p, e := s.Preview(context.Background(), m.BackupID)
	if e != nil {
		t.Fatal(e)
	}
	r := Request{Operation: "restore", ServerID: "server-a", RequestID: "once", ID: p.ID, Confirm: p.PlanDigest}
	first, e := s.Handle(context.Background(), r)
	if e != nil || first.State != "complete" {
		t.Fatalf("%+v %v", first, e)
	}
	put(t, s, "egg/server.json", "after")
	second, e := s.Handle(context.Background(), r)
	if e != nil || second.CompletedAt != first.CompletedAt {
		t.Fatalf("unstable receipt %+v %v", second, e)
	}
	b, _ := os.ReadFile(filepath.Join(s.volume, "egg/server.json"))
	if string(b) != "after" {
		t.Fatal("duplicate restore overwrote later state")
	}
}
func TestLiveChangeAfterSelectionCannotSealBackup(t *testing.T) {
	s := fixture(t)
	put(t, s, "egg/server.json", "first")
	selected, e := s.selectFiles(Options{})
	if e != nil {
		t.Fatal(e)
	}
	put(t, s, "egg/server.json", "changed")
	if _, e = s.seal(context.Background(), "backup-changing", "live-best-effort", selected, nil); e == nil {
		t.Fatal("sealed stale selection")
	}
	if _, e = s.manifest("backup-changing"); !errors.Is(e, os.ErrNotExist) {
		t.Fatal("partial backup was published", e)
	}
}
func TestForeignVolumeCannotReuseCatalog(t *testing.T) {
	s := fixture(t)
	if _, e := Open(t.TempDir(), s.policy); e == nil {
		t.Fatal("catalog accepted another volume")
	}
}
