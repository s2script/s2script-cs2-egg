// SPDX-License-Identifier: GPL-3.0-or-later

package boot

import (
	"context"
	"errors"
	"time"

	"github.com/K4ryuu/CS2-Egg-Go/internal/egg/nodeclient"
	"github.com/K4ryuu/CS2-Egg-Go/internal/egg/passwords"
	"github.com/K4ryuu/CS2-Egg-Go/internal/logx"
	"github.com/K4ryuu/CS2-Egg-Go/internal/proto"
)

// preparePassword runs after other boot-time node requests and before starting
// the engine, so its managed cfg already protects the initial map.
func preparePassword(ctx context.Context, env Env, node *nodeclient.Client, log *logx.Console) (*passwords.Manager, error) {
	enabled := env.flag("ENABLE_ROTATING_PASSWORD")
	manager, err := passwords.Open(env.Root, enabled)
	if err != nil {
		return nil, err
	}
	if !enabled {
		return manager, nil
	}
	if node != nil && node.HasModule("passwords") {
		// A just-started daemon may still be provisioning its dictionary.
		deadline := time.Now().Add(25 * time.Second)
		for ctx.Err() == nil {
			reply, err := node.Request(&proto.PasswordQuery{}, 5*time.Second, func(m proto.Message) bool { _, ok := m.(*proto.PasswordUpdate); return ok })
			if err != nil {
				break
			}
			if err = manager.Accept(*reply.(*proto.PasswordUpdate)); err == nil {
				break
			}
			if manager.Ready() || time.Now().After(deadline) {
				break
			}
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(time.Second):
			}
		}
	}
	if !manager.Ready() {
		return nil, errors.New("rotating password requested but no assignment is available; enable the node passwords module or opt this server out")
	}
	log.Log(logx.Info, "Node-managed join password loaded")
	return manager, nil
}
