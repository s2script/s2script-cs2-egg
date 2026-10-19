// SPDX-License-Identifier: GPL-3.0-or-later

package passwords

import (
	"context"
	"time"

	"github.com/K4ryuu/CS2-Egg-Go/internal/node/core"
	"github.com/K4ryuu/CS2-Egg-Go/internal/node/vpksync"
	"github.com/K4ryuu/CS2-Egg-Go/internal/proto"
)

// Only the enrolled socket's server can request its own restart, after the egg
// has durably cached the rotated assignment. Unchanged polling needs no restart.
func (m *Module) restart(ctx context.Context, c *core.Core, server string, request *proto.PasswordRestart) {
	if !m.cfg.Enabled || !c.PasswordOptIn(server) {
		return
	}
	restarted, err := m.restartSaved(ctx, server, request.NextAt, func(ctx context.Context, server string) error {
		var cfg struct {
			WingsConfig string `json:"wings_config"`
		}
		if err := c.Config("vpksync", &cfg); err != nil {
			return err
		}
		w, err := vpksync.LoadWings(cfg.WingsConfig)
		if err != nil {
			return err
		}
		c.ExpectStop(server, core.StopRestart)
		return w.Restart(ctx, server)
	})
	if err != nil {
		// Never log config errors or response bodies that could carry credentials.
		m.Log.Warn("password rotation restart failed; will retry", "server", server)
	} else if restarted {
		m.Log.Info("restarted via Wings for password rotation", "server", server)
	}
}

func (m *Module) restartSaved(ctx context.Context, server, nextAt string, restart func(context.Context, string) error) (bool, error) {
	unlock := m.lockServer(server)
	defer unlock()
	m.store.mu.Lock()
	r, exists := m.store.state.Records[m.store.key(server)]
	m.store.mu.Unlock()
	if !exists || nextAt == "" || r.NextAt.Format(time.RFC3339) != nextAt {
		return false, nil // stale or unassigned; never regenerate on a restart request
	}
	if previous, ok := m.restarted.Load(server); ok && previous.(string) == r.Password {
		return false, nil
	}
	if err := restart(ctx, server); err != nil {
		return false, err // next delivery retries until Wings accepts the restart
	}
	m.restarted.Store(server, r.Password)
	return true, nil
}
