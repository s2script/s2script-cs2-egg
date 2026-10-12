// SPDX-License-Identifier: GPL-3.0-or-later
package guard

import (
	"github.com/s2script/s2script-cs2-egg/internal/config"
	"github.com/s2script/s2script-cs2-egg/internal/server"
	"sync"
	"time"
)

type Local struct {
	Policy   config.GuardPolicy
	detector *Detector
	mu       sync.Mutex
	actions  map[int]Action
}

func NewLocal(p config.GuardPolicy) *Local {
	return &Local{Policy: p, detector: NewDetector(), actions: map[int]Action{}}
}
func (l *Local) Observe(at time.Time, id, run, line string) []server.ConsoleAction {
	p := Policy{GuardPolicy: l.Policy, ServerID: id}
	var out []server.ConsoleAction
	for _, f := range l.detector.Observe(at, id, run, "local-"+run, line, p) {
		f.Provenance = "local-console-correlated"
		a := Decide(f, p)
		if a.Kind == "block" {
			a.Kind = "log"
			a.Reason = "host block requires node helper"
		}
		l.mu.Lock()
		for uid, old := range l.actions {
			if at.Sub(old.At) > 10*time.Second {
				delete(l.actions, uid)
			}
		}
		if a.Kind == "kick" && len(l.actions) < 256 {
			l.actions[a.UID] = a
		}
		l.mu.Unlock()
		out = append(out, server.ConsoleAction{Kind: a.Kind, ServerID: id, RunID: run, SessionID: a.SessionID, Reason: f.Rule, UID: a.UID, At: a.At})
	}
	return out
}
func (l *Local) Valid(a server.ConsoleAction, at time.Time) bool {
	l.mu.Lock()
	stored, ok := l.actions[a.UID]
	l.mu.Unlock()
	if !ok || stored.RunID != a.RunID || stored.ServerID != a.ServerID || stored.At != a.At {
		return false
	}
	return l.detector.Valid(stored, at, Policy{GuardPolicy: l.Policy, ServerID: a.ServerID})
}
