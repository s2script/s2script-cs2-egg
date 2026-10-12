// SPDX-License-Identifier: GPL-3.0-or-later
package shared

import (
	"context"
	"github.com/s2script/s2script-cs2-egg/internal/artifact"
	node "github.com/s2script/s2script-cs2-egg/internal/nodetypes"
	"github.com/s2script/s2script-cs2-egg/internal/runtimelease"
	"os"
	"path/filepath"
	"testing"
)

func TestStaleHelperReceiptCannotReleaseNewRunReferences(t *testing.T) {
	ctx := context.Background()
	s, build := testStore(t)
	g1, err := s.Build(ctx)
	if err != nil {
		t.Fatal(err)
	}
	srv := node.Server{ID: "one", Volume: t.TempDir()}
	admin, err := s.Reserve(ctx, srv, "admin", "admin")
	if err != nil {
		t.Fatal(err)
	}
	fence, err := s.captureReconciliation(srv.ID, admin.ID)
	if err != nil {
		t.Fatal(err)
	}
	oldLease, err := runtimelease.Acquire(srv.Volume)
	if err != nil {
		t.Fatal(err)
	}
	// Helper finished g1 and sent its receipt, then died/released the lease.
	if err := MaterializeLocked(ctx, srv.Volume, s.root, g1, admin.ID, "symlink", nil, ""); err != nil {
		t.Fatal(err)
	}
	receipt, err := ReadLocal(srv.Volume)
	if err != nil {
		t.Fatal(err)
	}
	oldLease()
	*build = "200"
	g2, err := s.Build(ctx)
	if err != nil {
		t.Fatal(err)
	}
	liveLease, err := runtimelease.Acquire(srv.Volume)
	if err != nil {
		t.Fatal(err)
	}
	defer liveLease()
	newer, err := s.Reserve(ctx, srv, "new-run", "new-request")
	if err != nil {
		t.Fatal(err)
	}
	if err := MaterializeLocked(ctx, srv.Volume, s.root, g2, newer.ID, "symlink", nil, ""); err != nil {
		t.Fatal(err)
	}
	if err := s.Commit(ctx, srv.ID, newer.ID); err != nil {
		t.Fatal(err)
	}
	// Delayed Attach continuation consumes the buffered old receipt.
	if err := s.Commit(ctx, srv.ID, admin.ID); err != nil {
		t.Fatal(err)
	}
	unlock, err := artifact.Acquire(s.root)
	if err != nil {
		t.Fatal(err)
	}
	err = s.reconcile(fence, receipt.Dependencies)
	unlock()
	if err != nil {
		t.Fatal(err)
	}
	*build = "300"
	if _, err := s.Build(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Prune(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(g2.Root); err != nil {
		t.Fatalf("generation used under held runtime lease was pruned after stale helper reconciliation: %v", err)
	}
}

func TestReconciliationRetainsChangedReference(t *testing.T) {
	ctx := context.Background()
	s, build := testStore(t)
	first, err := s.Build(ctx)
	if err != nil {
		t.Fatal(err)
	}
	srv := node.Server{ID: "one"}
	changed, err := s.Reserve(ctx, srv, "pending", "pending")
	if err != nil {
		t.Fatal(err)
	}
	*build = "200"
	next, err := s.Build(ctx)
	if err != nil {
		t.Fatal(err)
	}
	admin, err := s.reserve(ctx, srv.ID, "admin", "admin", next.ID)
	if err != nil {
		t.Fatal(err)
	}
	fence, err := s.captureReconciliation(srv.ID, admin.ID)
	if err != nil {
		t.Fatal(err)
	}
	// A pending existing identity changes after the pre-helper snapshot.
	if err := s.Commit(ctx, srv.ID, changed.ID); err != nil {
		t.Fatal(err)
	}
	unlock, err := artifact.Acquire(s.root)
	if err != nil {
		t.Fatal(err)
	}
	err = s.reconcile(fence, nil)
	unlock()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Prune(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(first.Root, "game/csgo/pak01.vpk")); err != nil {
		t.Fatal("changed existing reference was released", err)
	}
}

func TestReconciliationRetainsRenewedPins(t *testing.T) {
	for _, action := range []string{"admin-reuse-at-quota", "reserve-retry", "commit-retry"} {
		t.Run(action, func(t *testing.T) {
			ctx := context.Background()
			s, build := testStore(t)
			first, err := s.Build(ctx)
			if err != nil {
				t.Fatal(err)
			}
			srv := node.Server{ID: "one"}
			ref, err := s.Reserve(ctx, srv, "earlier", "earlier")
			if err != nil {
				t.Fatal(err)
			}
			if err := s.Commit(ctx, srv.ID, ref.ID); err != nil {
				t.Fatal(err)
			}
			*build = "200"
			next, err := s.Build(ctx)
			if err != nil {
				t.Fatal(err)
			}
			admin, err := s.reserve(ctx, srv.ID, "admin", "admin", next.ID)
			if err != nil {
				t.Fatal(err)
			}
			if action == "admin-reuse-at-quota" {
				refs, err := s.refs()
				if err != nil {
					t.Fatal(err)
				}
				for len(refs.Records) < 8192 {
					refs.Records = append(refs.Records, Reference{ID: randomID(), Server: srv.ID, Run: "old", Request: randomID(), Generation: next.ID, Committed: true})
				}
				if err := writeJSON(s.root, "references.json", refs, 0600); err != nil {
					t.Fatal(err)
				}
			}
			fence, err := s.captureReconciliation(srv.ID, admin.ID)
			if err != nil {
				t.Fatal(err)
			}
			switch action {
			case "admin-reuse-at-quota":
				_, err = s.reserve(ctx, srv.ID, "new-admin", "new-admin", first.ID)
			case "reserve-retry":
				_, err = s.Reserve(ctx, srv, "earlier", "earlier")
			case "commit-retry":
				err = s.Commit(ctx, srv.ID, ref.ID)
			}
			if err != nil {
				t.Fatal(err)
			}
			unlock, err := artifact.Acquire(s.root)
			if err != nil {
				t.Fatal(err)
			}
			err = s.reconcile(fence, nil)
			unlock()
			if err != nil {
				t.Fatal(err)
			}
			if _, err := s.Prune(ctx); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(first.Root); err != nil {
				t.Fatal("renewed old pin retired by stale snapshot", err)
			}
		})
	}
}
