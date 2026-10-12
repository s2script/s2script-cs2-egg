// SPDX-License-Identifier: GPL-3.0-or-later
package server

import (
	"context"
	"github.com/s2script/s2script-cs2-egg/internal/protocol"
	"regexp"
	"strings"
	"sync"
	"time"
)

type ControlProvider interface {
	Control(context.Context, Status) (protocol.UpdateControl, error)
}

var stopToken = regexp.MustCompile(`^[a-f0-9]{32}$`)

type stopControl struct {
	mu       sync.Mutex
	run      string
	intent   protocol.UpdateControl
	veto     bool
	executed string
}

func (g *stopControl) Arm(c protocol.UpdateControl) {
	g.mu.Lock()
	defer g.mu.Unlock()
	now := time.Now()
	if !g.veto && c.RunID == g.run && stopToken.MatchString(c.ActionID) && now.Before(c.ExpiresAt) && c.ExpiresAt.Sub(now) <= time.Minute {
		g.intent = c
	}
}
func (g *stopControl) Veto() { g.mu.Lock(); g.veto = true; g.mu.Unlock() }
func (g *stopControl) Evidence() (string, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.executed != "" {
		return g.executed, g.veto
	}
	if time.Now().Before(g.intent.ExpiresAt) {
		return g.intent.ActionID, g.veto
	}
	return "", g.veto
}
func (g *stopControl) Handle(line string) (bool, string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	line = strings.TrimSpace(line)
	if strings.EqualFold(line, "quit") || strings.EqualFold(line, "exit") {
		g.veto = true
		return false, ""
	}
	if !strings.HasPrefix(strings.ToLower(line), "s2egg_scheduled_stop") {
		return false, ""
	}
	parts := strings.Fields(line)
	if len(parts) != 2 || parts[0] != "s2egg_scheduled_stop" || g.veto || !stopToken.MatchString(parts[1]) || parts[1] != g.intent.ActionID || !time.Now().Before(g.intent.ExpiresAt) {
		return true, ""
	}
	g.executed = parts[1]
	return true, "scheduled_update"
}
