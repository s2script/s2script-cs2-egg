// SPDX-License-Identifier: GPL-3.0-or-later
package shared

import (
	"context"
	"errors"
	"fmt"
	"github.com/s2script/s2script-cs2-egg/internal/artifact"
	node "github.com/s2script/s2script-cs2-egg/internal/nodetypes"
	"github.com/s2script/s2script-cs2-egg/internal/runtimelease"
	"net"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func helperFixture(t *testing.T, s *Store) (node.Server, func()) {
	t.Helper()
	volume := t.TempDir()
	put(t, volume, "egg/keep", "private")
	socket := filepath.Join(t.TempDir(), "helper.sock")
	listener, e := net.Listen("unix", socket)
	if e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- ServeHelper(ctx, listener, HelperConfig{Volume: volume, SharedRoot: s.root, NodeUID: uint32(os.Geteuid()), RuntimeUID: uint32(os.Geteuid()), AccessGID: os.Getgid()})
	}()
	s.options.Helpers = map[string]HelperEndpoint{"one": {Socket: socket, UID: uint32(os.Geteuid()), GID: os.Getgid()}}
	return node.Server{ID: "one", Volume: volume}, func() {
		cancel()
		listener.Close()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Error("helper failed to stop")
		}
	}
}
func TestAttachRefusesRunningGame(t *testing.T) {
	s, _ := testStore(t)
	g, e := s.Build(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	srv, close := helperFixture(t, s)
	defer close()
	u, e := runtimelease.Acquire(srv.Volume)
	if e != nil {
		t.Fatal(e)
	}
	defer u()
	if e = s.Attach(context.Background(), srv, g.ID, "copy"); e == nil {
		t.Fatal("attached despite live runtime lease")
	}
	if _, e = os.Stat(filepath.Join(srv.Volume, "game")); !errors.Is(e, os.ErrNotExist) {
		t.Fatal("running server changed")
	}
}
func TestHelperCopyPreservesPrivateFileAndRequiresProvisioning(t *testing.T) {
	s, _ := testStore(t)
	g, e := s.Build(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	if e = s.Attach(context.Background(), node.Server{ID: "absent", Volume: t.TempDir()}, g.ID, "copy"); e == nil {
		t.Fatal("unprovisioned helper accepted")
	}
	srv, close := helperFixture(t, s)
	defer close()
	put(t, srv.Volume, "game/csgo/cfg/server.cfg", "operator")
	if e = s.Attach(context.Background(), srv, g.ID, "copy"); e != nil {
		t.Fatal(e)
	}
	b, _ := os.ReadFile(filepath.Join(srv.Volume, "game/csgo/cfg/server.cfg"))
	if string(b) != "operator" {
		t.Fatal("private file replaced")
	}
}
func TestHelperRejectsWrongDaemonUID(t *testing.T) {
	s, _ := testStore(t)
	g, e := s.Build(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	srv, close := helperFixture(t, s)
	defer close()
	ep := s.options.Helpers[srv.ID]
	ep.UID++
	s.options.Helpers[srv.ID] = ep
	if e = s.Attach(context.Background(), srv, g.ID, "copy"); e == nil {
		t.Fatal("wrong helper identity accepted")
	}
}
func TestHardlinkSameRuntimeOwnerRejected(t *testing.T) {
	s, _ := testStore(t)
	g, e := s.Build(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	srv, close := helperFixture(t, s)
	defer close()
	if e = s.Attach(context.Background(), srv, g.ID, "hardlink"); e == nil {
		t.Fatal("runtime could chmod shared inode")
	}
}

func TestHardlinkCrossFilesystemFailsExplicitly(t *testing.T) {
	source := t.TempDir()
	dest, e := os.MkdirTemp("/dev/shm", "s2shared-exdev-")
	if e != nil {
		t.Skip("separate temporary filesystem unavailable:", e)
	}
	defer os.RemoveAll(dest)
	a, e := os.Stat(source)
	if e != nil {
		t.Fatal(e)
	}
	b, e := os.Stat(dest)
	if e != nil {
		t.Fatal(e)
	}
	if a.Sys().(*syscall.Stat_t).Dev == b.Sys().(*syscall.Stat_t).Dev {
		t.Skip("temporary paths are on one filesystem")
	}
	put(t, source, "source", "unchanged")
	src, e := artifact.OpenDirectory(source)
	if e != nil {
		t.Fatal(e)
	}
	defer src.Close()
	dst, e := artifact.OpenDirectory(dest)
	if e != nil {
		t.Fatal(e)
	}
	defer dst.Close()
	if e = linkImmutable(src, "source", dst, 0); !errors.Is(e, syscall.EXDEV) {
		t.Fatalf("real link did not report EXDEV: %v", e)
	}
	v, _ := os.ReadFile(filepath.Join(source, "source"))
	if string(v) != "unchanged" {
		t.Fatal("failed link modified source")
	}
}

func TestStoppedReconciliationCanRecoverFullReferenceQuota(t *testing.T) {
	s, _ := testStore(t)
	g, e := s.Build(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	srv, close := helperFixture(t, s)
	defer close()
	r := references{Version: 1}
	for i := 0; i < 8192; i++ {
		r.Records = append(r.Records, Reference{ID: fmt.Sprintf("%032x", i+1), Server: srv.ID, Run: fmt.Sprint(i), Request: fmt.Sprint(i), Generation: g.ID, Committed: true})
	}
	if e = writeJSON(s.root, "references.json", r, 0600); e != nil {
		t.Fatal(e)
	}
	if e = s.Attach(context.Background(), srv, g.ID, "copy"); e != nil {
		t.Fatal("cannot safely reconcile exhausted references", e)
	}
	after, e := s.refs()
	if e != nil || len(after.Records) != 1 {
		t.Fatalf("stopped reconciliation did not compact references: %d %v", len(after.Records), e)
	}
}

func TestOverrideSymlinkKeepsPreviousGenerationReferenced(t *testing.T) {
	s, b := testStore(t)
	old, e := s.Build(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	srv, close := helperFixture(t, s)
	defer close()
	ref, e := s.Reserve(context.Background(), srv, "old-run", "old-request")
	if e != nil {
		t.Fatal(e)
	}
	if e = MaterializeLocked(context.Background(), srv.Volume, s.root, old, ref.ID, "symlink", nil, ""); e != nil {
		t.Fatal(e)
	}
	if e = s.Commit(context.Background(), srv.ID, ref.ID); e != nil {
		t.Fatal(e)
	} // Preserve an explicit operator symlink outside the next generation's inventory.
	if e = os.Symlink(filepath.Join(old.Root, "game/csgo/pak01.vpk"), filepath.Join(srv.Volume, "game/csgo/operator.vpk")); e != nil {
		t.Fatal(e)
	}
	*b = "200"
	next, e := s.Build(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	if e = s.Attach(context.Background(), srv, next.ID, "copy"); e != nil {
		t.Fatal(e)
	}
	if _, e = s.Prune(context.Background()); e != nil {
		t.Fatal(e)
	}
	v, e := os.ReadFile(filepath.Join(srv.Volume, "game/csgo/operator.vpk"))
	if e != nil || string(v) != "vpk-100" {
		t.Fatal("preserved operator link lost its generation", e)
	}
}

func TestAttachDelayedReceiptPreservesNewRun(t *testing.T) {
	for _, sendReceipt := range []bool{true, false} {
		t.Run(fmt.Sprint("receipt=", sendReceipt), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			s, build := testStore(t)
			first, err := s.Build(ctx)
			if err != nil {
				t.Fatal(err)
			}
			srv := node.Server{ID: "one", Volume: t.TempDir()}
			socket := filepath.Join(t.TempDir(), "helper.sock")
			listener, err := net.Listen("unix", socket)
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()
			s.options.Helpers = map[string]HelperEndpoint{srv.ID: {Socket: socket, UID: uint32(os.Geteuid()), GID: os.Getgid()}}
			ready := make(chan struct{})
			resume := make(chan struct{})
			helperDone := make(chan error, 1)
			go func() {
				helperDone <- func() error {
					conn, err := listener.Accept()
					if err != nil {
						return err
					}
					defer conn.Close()
					stop := context.AfterFunc(ctx, func() { conn.Close() })
					defer stop()
					var req helperRequest
					if err = helperRead(conn, "begin", &req); err != nil {
						return err
					}
					lease, err := runtimelease.Acquire(srv.Volume)
					if err != nil {
						return err
					}
					unlock, err := artifact.Acquire(srv.Volume)
					if err != nil {
						lease()
						return err
					}
					materializeErr := func() error {
						if err := helperWrite(conn, "result", helperReply{}); err != nil {
							return err
						}
						if err := helperRead(conn, "materialize", &struct{}{}); err != nil {
							return err
						}
						return MaterializeLocked(ctx, srv.Volume, s.root, first, req.Reservation, "symlink", nil, "")
					}()
					unlock()
					lease() // Helper death/timeout releases locks while receipt is in flight.
					if materializeErr != nil {
						return materializeErr
					}
					receipt, err := ReadLocal(srv.Volume)
					if err != nil {
						return err
					}
					close(ready)
					select {
					case <-resume:
					case <-ctx.Done():
						return ctx.Err()
					}
					if !sendReceipt {
						return nil
					}
					if err = helperWrite(conn, "result", helperReply{Generation: first.ID, Dependencies: receipt.Dependencies}); err != nil {
						return err
					}
					return helperRead(conn, "release", &struct{}{})
				}()
			}()
			attached := make(chan error, 1)
			go func() { attached <- s.Attach(ctx, srv, first.ID, "symlink") }()
			select {
			case <-ready:
			case err := <-helperDone:
				t.Fatal("helper failed before materialization", err)
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			*build = "200"
			next, err := s.Build(ctx)
			if err != nil {
				t.Fatal(err)
			}
			lease, err := runtimelease.Acquire(srv.Volume)
			if err != nil {
				t.Fatal(err)
			}
			defer lease()
			unlock, err := artifact.Acquire(srv.Volume)
			if err != nil {
				t.Fatal(err)
			}
			newer, err := s.Reserve(ctx, srv, "new-run", "new-request")
			if err != nil {
				unlock()
				t.Fatal(err)
			}
			err = MaterializeLocked(ctx, srv.Volume, s.root, next, newer.ID, "symlink", nil, "")
			unlock()
			if err != nil {
				t.Fatal(err)
			}
			if err := s.Commit(ctx, srv.ID, newer.ID); err != nil {
				t.Fatal(err)
			}
			close(resume)
			select {
			case err := <-attached:
				if (err == nil) != sendReceipt {
					t.Fatal("unexpected Attach result", err)
				}
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			select {
			case err := <-helperDone:
				if err != nil {
					t.Fatal(err)
				}
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			*build = "300"
			if _, err := s.Build(ctx); err != nil {
				t.Fatal(err)
			}
			if _, err := s.Prune(ctx); err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(filepath.Join(next.Root, "game/csgo/pak01.vpk"))
			if err != nil || string(data) != "vpk-200" {
				t.Fatal("Attach released newer live generation after stale/uncertain receipt", err)
			}
			if !sendReceipt {
				if _, err := os.Stat(first.Root); err != nil {
					t.Fatal("uncertain Attach released old reservation", err)
				}
			}
		})
	}
}

func TestAttachPreservesStartupOverridePolicy(t *testing.T) {
	ctx := context.Background()
	s, build := testStore(t)
	first, err := s.Build(ctx)
	if err != nil {
		t.Fatal(err)
	}
	srv, close := helperFixture(t, s)
	defer close()
	rel := "game/csgo/pak01.vpk"
	put(t, srv.Volume, rel, "operator content")
	before, err := os.Stat(filepath.Join(srv.Volume, rel))
	if err != nil {
		t.Fatal(err)
	}
	if err := MaterializeLocked(ctx, srv.Volume, s.root, first, "local", "copy", []string{rel}, ""); err != nil {
		t.Fatal(err)
	}
	*build = "200"
	next, err := s.Build(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Attach(ctx, srv, next.ID, "copy"); err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(filepath.Join(srv.Volume, rel))
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(srv.Volume, rel))
	if err != nil || string(data) != "operator content" || !os.SameFile(before, after) || after.Mode() != before.Mode() {
		t.Fatal("helper lost startup operator policy", string(data), err)
	}
}
