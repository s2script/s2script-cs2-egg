// SPDX-License-Identifier: GPL-3.0-or-later
package node

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/s2script/s2script-cs2-egg/internal/events"
	"github.com/s2script/s2script-cs2-egg/internal/nodetypes"
	"github.com/s2script/s2script-cs2-egg/internal/protocol"
	"github.com/s2script/s2script-cs2-egg/internal/server"
)

type PrivateHelper = nodetypes.PrivateHelper
type Server = nodetypes.Server
type Health = nodetypes.Health
type Module = nodetypes.Module
type Host = nodetypes.Host
type Actions = nodetypes.Actions

type moduleEntry struct {
	module Module
	grants map[string]bool
	health Health
	mu     sync.Mutex
}
type moduleHost struct {
	producer string
	core     *Core
	grants   map[string]bool
}

func (h moduleHost) Subscribe(n int) (*nodetypes.Subscription, error) { return h.core.Subscribe(n) }
func (h moduleHost) PublishOperational(ctx context.Context, e events.OperationalEvent) (events.Admission, error) {
	if !h.grants[nodetypes.GrantOperationalEvents] {
		return events.Admission{}, errors.New("no operational event grant")
	}
	return h.core.publishOperational(ctx, h.producer, e)
}
func (h moduleHost) ModuleHealth() map[string]Health { return h.core.ModuleHealth() }
func (h moduleHost) Servers() []Server               { return h.core.Servers() }
func (h moduleHost) Status(id string) server.Status  { return h.core.Status(id) }
func (h moduleHost) Publish(ctx context.Context, e events.Event) error {
	if !h.grants[protocol.CapEvents] {
		return errors.New("module has no event publishing grant")
	}
	return h.core.Publish(ctx, e)
}
func (h moduleHost) Dispatch(ctx context.Context, id, action string, payload any) (any, error) {
	if !h.grants[action] {
		return nil, errors.New("module action is not authorized")
	}
	return h.core.Dispatch(ctx, id, action, payload)
}
func (c *Core) AddModule(m Module, grants []string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.running || m == nil || m.Name() == "" {
		return errors.New("module registration unavailable")
	}
	if _, ok := c.modules[m.Name()]; ok {
		return errors.New("duplicate module")
	}
	g := map[string]bool{}
	for _, grant := range grants {
		if !knownCapability(grant) && grant != nodetypes.GrantOperationalEvents {
			return errors.New("unknown module grant")
		}
		g[grant] = true
	}
	c.modules[m.Name()] = &moduleEntry{module: m, grants: g, health: Health{Status: "degraded", Explanation: "not started"}}
	return nil
}
func knownCapability(cap string) bool {
	switch cap {
	case protocol.CapProjectUpdate, protocol.CapGuardAdmin, protocol.CapBackupAdmin, protocol.CapOperations, protocol.CapEvents, protocol.CapStatus, protocol.CapPrepare, protocol.CapCache, protocol.CapWorkshop, protocol.CapRestart, protocol.CapUpdateControl, protocol.CapUpdateExecute, protocol.CapUpdateAdmin:
		return true
	}
	return false
}
func safeModule(f func() error) (err error) {
	defer func() {
		if recover() != nil {
			err = errors.New("optional module panicked")
		}
	}()
	return f()
}
func (c *Core) ModuleHealth() map[string]Health {
	c.mu.Lock()
	entries := map[string]*moduleEntry{}
	for name, m := range c.modules {
		entries[name] = m
	}
	c.mu.Unlock()
	out := map[string]Health{}
	for name, m := range entries {
		m.mu.Lock()
		health := m.health
		m.mu.Unlock()
		if health.Status != "failed" {
			var reported Health
			e := safeModule(func() error { reported = m.module.Health(); return nil })
			if e != nil {
				health = Health{Status: "failed", Explanation: "module health check failed"}
			} else {
				switch reported.Status {
				case "healthy", "degraded", "failed":
					health = reported
				default:
					health = Health{Status: "failed", Explanation: "invalid module health"}
				}
			}
		}
		out[name] = health
	}
	return out
}
func (c *Core) startModules(ctx context.Context) {
	for _, m := range c.modules {
		err := safeModule(func() error {
			return m.module.Start(ctx, moduleHost{core: c, grants: m.grants, producer: m.module.Name()})
		})
		var capabilities []string
		if err == nil {
			if a, ok := m.module.(Actions); ok {
				err = safeModule(func() error { capabilities = append([]string(nil), a.Capabilities()...); return nil })
			}
		}
		c.mu.Lock()
		if err == nil {
			seen := map[string]bool{}
			for _, cap := range capabilities {
				if !knownCapability(cap) || cap == protocol.CapEvents || cap == protocol.CapStatus || !m.grants[cap] || seen[cap] || c.actions[cap] != nil {
					err = errors.New("invalid action registration")
					break
				}
				seen[cap] = true
			}
		}
		if err == nil {
			for _, cap := range capabilities {
				c.actions[cap] = m
			}
		}
		c.mu.Unlock()
		m.mu.Lock()
		if err != nil {
			m.health = Health{Status: "failed", Explanation: "module initialization failed"}
		} else {
			m.health = Health{Status: "healthy"}
		}
		m.mu.Unlock()
	}
}
func (c *Core) closeModules(ctx context.Context) {
	for _, m := range c.modules {
		_ = safeModule(func() error { return m.module.Close(ctx) })
	}
}
func (c *Core) invoke(ctx context.Context, s Server, cap string, msg protocol.Message) (out any, err error) {
	c.mu.Lock()
	m := c.actions[cap]
	c.mu.Unlock()
	if m == nil {
		return nil, errors.New("capability unavailable")
	}
	m.mu.Lock()
	failed := m.health.Status == "failed"
	m.mu.Unlock()
	if failed {
		return nil, errors.New("module unavailable")
	}
	err = safeModule(func() error { var e error; out, e = m.module.(Actions).Handle(ctx, s, cap, msg); return e })
	if err != nil {
		m.mu.Lock()
		m.health = Health{Status: "degraded", Explanation: fmt.Sprintf("%s request failed", cap)}
		m.mu.Unlock()
	}
	return
}

// InitializationReady checks initialization only, without depending on external
// Steam, Wings, Discord or game availability after a module has started.
func (c *Core) InitializationReady(names []string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, name := range names {
		m := c.modules[name]
		if m == nil {
			return fmt.Errorf("required module %s is not configured", name)
		}
		m.mu.Lock()
		failed := m.health.Status == "failed"
		m.mu.Unlock()
		if failed {
			return fmt.Errorf("required module %s failed initialization", name)
		}
	}
	return nil
}
