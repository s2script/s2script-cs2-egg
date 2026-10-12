// SPDX-License-Identifier: GPL-3.0-or-later
package scheduler

import (
	"github.com/s2script/s2script-cs2-egg/internal/config"
	"github.com/s2script/s2script-cs2-egg/internal/server"
	"github.com/s2script/s2script-cs2-egg/internal/updatepolicy"
	"time"
)

type Policy = updatepolicy.Policy
type Decision = updatepolicy.Decision

func observation(s server.Status) updatepolicy.Observation {
	return updatepolicy.Observation{RunID: s.RunID, Ready: s.Ready, SampleAt: s.SampleAt, ReceivedAt: s.ReceivedAt, Players: updatepolicy.Players{Known: s.Players.Known, Count: s.Players.Count}}
}
func Decide(n, p time.Time, s server.Status, policy Policy) Decision {
	return updatepolicy.Decide(n, p, observation(s), policy)
}
func Eligible(n, p time.Time, s server.Status, policy Policy) bool {
	return updatepolicy.Eligible(n, p, observation(s), policy)
}
func Fresh(n time.Time, s server.Status, d time.Duration) bool {
	return updatepolicy.Fresh(n, observation(s), d)
}
func Countdown(p Policy) time.Duration         { return updatepolicy.Countdown(p) }
func FromConfig(c config.RestartPolicy) Policy { return updatepolicy.FromConfig(c) }
