// SPDX-License-Identifier: GPL-3.0-or-later
package backup

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestOversizedRetentionRejectsWithoutDeletingPreviousBackups(t *testing.T) {
	s := fixture(t)
	put(t, s, "egg/server.json", "previous recoverable state")
	old, e := s.Create(context.Background(), Options{})
	if e != nil {
		t.Fatal(e)
	}
	s.policy.Retention.Bytes = 1
	if e = s.policy.Validate(); e != nil {
		t.Fatal(e)
	}
	put(t, s, "egg/server.json", "new private state")
	if _, e = s.Create(context.Background(), Options{}); e == nil {
		t.Fatal("backup larger than retention budget reported success")
	}
	if _, e = s.manifest(old.BackupID); e != nil {
		t.Fatal("previous backup was removed", e)
	}
	if _, e = s.verify(context.Background(), old, ""); e != nil {
		t.Fatal("previous backup is not recoverable", e)
	}
	entries, e := os.ReadDir(filepath.Join(s.policy.Root, "archives"))
	if e != nil || len(entries) != 1 {
		t.Fatal("failed backup was published", entries, e)
	}
}
