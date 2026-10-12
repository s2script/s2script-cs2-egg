// SPDX-License-Identifier: GPL-3.0-or-later
package shared

import (
	"context"
	"github.com/s2script/s2script-cs2-egg/internal/artifact"
	"github.com/s2script/s2script-cs2-egg/internal/node"
	"os"
	"testing"
)

func TestScheduledTargetSurvivesNewCurrent(t *testing.T) {
	s, build := testStore(t)
	ctx := context.Background()
	a, e := s.Build(ctx)
	if e != nil {
		t.Fatal(e)
	}
	srv := node.Server{ID: "srv"}
	if e = s.SelectTarget(ctx, srv.ID, a.ID); e != nil {
		t.Fatal(e)
	}
	*build = "200"
	if _, e = s.Build(ctx); e != nil {
		t.Fatal(e)
	}
	r, e := s.Reserve(ctx, srv, "new-run", "new-request")
	if e != nil || r.Generation != a.ID {
		t.Fatal(r, e)
	}
}
func TestUnchangedBuildDoesNotDownload(t *testing.T) {
	s, _ := testStore(t)
	ctx := context.Background()
	a, e := s.Build(ctx)
	if e != nil {
		t.Fatal(e)
	}
	g, changed, e := s.PollUpdate(ctx, func(context.Context, string) (string, error) { return a.BuildID, nil })
	if e != nil || changed || g.ID != a.ID {
		t.Fatal(g, changed, e)
	}
}

func TestPollSerializesAdministrativeBuild(t *testing.T) {
	s, _ := testStore(t)
	ctx := context.Background()
	g, e := s.Build(ctx)
	if e != nil {
		t.Fatal(e)
	}
	entered := make(chan struct{})
	release := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		_, _, e := s.PollUpdate(ctx, func(context.Context, string) (string, error) { close(entered); <-release; return g.BuildID, nil })
		done <- e
	}()
	<-entered
	if _, e = s.Build(ctx); e == nil {
		t.Error("administrative Build overlapped metadata polling")
	}
	close(release)
	if e = <-done; e != nil {
		t.Fatal(e)
	}
}

func TestWaitingJobPinSurvivesUnrelatedStoppedReconciliation(t *testing.T) {
	s, build := testStore(t)
	ctx := context.Background()
	target, err := s.Build(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.PinTarget(ctx, "srv", target.ID); err != nil {
		t.Fatal(err)
	}
	*build = "200"
	other, err := s.Build(ctx)
	if err != nil {
		t.Fatal(err)
	}
	ref, err := s.reserve(ctx, "srv", "admin", "other", other.ID)
	if err != nil {
		t.Fatal(err)
	}
	fence, err := s.captureReconciliation("srv", ref.ID)
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
	if _, err = s.Prune(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(target.Root); err != nil {
		t.Fatal("waiting job target pin was retired", err)
	}
}

func TestClearTargetFencesSelectionAndPinIndependently(t *testing.T) {
	s, build := testStore(t)
	ctx := context.Background()
	a, err := s.Build(ctx)
	if err != nil {
		t.Fatal(err)
	}
	*build = "200"
	b, err := s.Build(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, mixed := range []bool{false, true} {
		t.Run(map[bool]string{false: "new_selection_and_pin", true: "old_selection_new_pin"}[mixed], func(t *testing.T) {
			selected := b.ID
			if mixed {
				selected = a.ID
			}
			if err = s.SelectTarget(ctx, "srv", selected); err != nil {
				t.Fatal(err)
			}
			if err = s.PinTarget(ctx, "srv", b.ID); err != nil {
				t.Fatal(err)
			}
			for i := 0; i < 2; i++ {
				if err = s.ClearTarget(ctx, "srv", a.ID); err != nil {
					t.Fatal(err)
				}
			}
			targets, err := s.targets()
			if err != nil {
				t.Fatal(err)
			}
			pins, err := s.generationMap("update-pins.json")
			if err != nil {
				t.Fatal(err)
			}
			expected := b.ID
			if mixed {
				expected = ""
			}
			if targets["srv"] != expected || pins["srv"] != b.ID {
				t.Fatal("historical cleanup deleted newer ownership", targets, pins)
			}
			if err = s.ClearTarget(ctx, "srv", b.ID); err != nil {
				t.Fatal(err)
			}
			targets, err = s.targets()
			if err != nil {
				t.Fatal(err)
			}
			pins, err = s.generationMap("update-pins.json")
			if err != nil {
				t.Fatal(err)
			}
			if targets["srv"] != "" || pins["srv"] != "" {
				t.Fatal("owner cleanup failed", targets, pins)
			}
		})
	}
	if err = s.SelectTarget(ctx, "srv", ""); err == nil {
		t.Fatal("unfenced cleanup accepted")
	}
}
