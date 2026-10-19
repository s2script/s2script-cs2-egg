// SPDX-License-Identifier: GPL-3.0-or-later

package passwords

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestRotationRestartRetriesFailureAndDeduplicatesSuccess(t *testing.T) {
	due := time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)
	m := &Module{store: &Store{cfg: Config{Mode: "unique"}, state: diskState{Records: map[string]Record{
		"server:one": {Password: "lantern-meadow-copper", NextAt: due},
	}}}}
	calls := 0
	restart := func(_ context.Context, server string) error {
		calls++
		if server != "one" {
			t.Fatal("restart targeted another server")
		}
		if calls == 1 {
			return errors.New("Wings unavailable")
		}
		return nil
	}
	ctx := context.Background()
	for _, request := range []struct{ server, nextAt string }{
		{"one", ""}, {"one", "2026-10-15T12:00:00Z"}, {"two", due.Format(time.RFC3339)},
	} {
		if restarted, err := m.restartSaved(ctx, request.server, request.nextAt, restart); restarted || err != nil {
			t.Fatal("stale or unassigned request accepted", err)
		}
	}
	if calls != 0 {
		t.Fatal("invalid request called Wings")
	}
	if restarted, err := m.restartSaved(ctx, "one", due.Format(time.RFC3339), restart); restarted || err == nil {
		t.Fatal("restart failure not retained")
	}
	if restarted, err := m.restartSaved(ctx, "one", due.Format(time.RFC3339), restart); !restarted || err != nil {
		t.Fatal("restart not retried", err)
	}
	for i := 0; i < 10; i++ {
		if restarted, err := m.restartSaved(ctx, "one", due.Format(time.RFC3339), restart); restarted || err != nil {
			t.Fatal("duplicate restart accepted", err)
		}
	}
	if calls != 2 {
		t.Fatal("duplicate delivery restarted the server again")
	}
	m.store.state.Records["server:one"] = Record{Password: "river-birch-copper", NextAt: due.AddDate(0, 1, 0)}
	if restarted, err := m.restartSaved(ctx, "one", "2026-12-15T12:00:00Z", restart); !restarted || err != nil {
		t.Fatal("next rotation did not restart", err)
	}
}

func TestSharedRotationRestartsEachParticipatingServer(t *testing.T) {
	due := time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)
	m := &Module{store: &Store{cfg: Config{Mode: "shared"}, state: diskState{Records: map[string]Record{
		"shared": {Password: "lantern-meadow-copper", NextAt: due},
	}}}}
	var servers []string
	for _, server := range []string{"one", "two", "one", "two"} {
		_, err := m.restartSaved(context.Background(), server, due.Format(time.RFC3339), func(_ context.Context, target string) error {
			servers = append(servers, target)
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if len(servers) != 2 || servers[0] != "one" || servers[1] != "two" {
		t.Fatal("shared rotation did not restart each server exactly once")
	}
}
