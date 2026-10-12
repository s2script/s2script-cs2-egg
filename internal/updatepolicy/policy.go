// SPDX-License-Identifier: GPL-3.0-or-later
package updatepolicy

import (
	"errors"
	"github.com/s2script/s2script-cs2-egg/internal/config"
	"time"
)

type Policy struct {
	Mode           string          `json:"mode"`
	MaximumDelay   time.Duration   `json:"maximum_delay"`
	Timezone       string          `json:"timezone,omitempty"`
	WindowStart    string          `json:"window_start,omitempty"`
	WindowEnd      string          `json:"window_end,omitempty"`
	WarningOffsets []time.Duration `json:"warning_offsets,omitempty"`
	Freshness      time.Duration   `json:"freshness"`
}
type Decision struct {
	Action string
	Reason string
}

func (p Policy) Validate() error {
	if p.MaximumDelay < time.Second || p.MaximumDelay > 7*24*time.Hour || p.Freshness < 0 || p.Freshness > 5*time.Minute || len(p.WarningOffsets) > 16 {
		return errors.New("invalid scheduler timing")
	}
	switch p.Mode {
	case "immediate", "when_empty":
	case "maintenance_window":
		if _, e := time.LoadLocation(p.Timezone); e != nil || p.Timezone == "" {
			return errors.New("IANA timezone required")
		}
		for _, v := range []string{p.WindowStart, p.WindowEnd} {
			if _, e := time.Parse("15:04", v); e != nil {
				return errors.New("invalid local maintenance window")
			}
		}
		if p.WindowStart == p.WindowEnd {
			return errors.New("maintenance window must have distinct endpoints")
		}
	default:
		return errors.New("unknown update policy")
	}
	for _, v := range p.WarningOffsets {
		if v < 0 || v > time.Hour {
			return errors.New("warning offset must be between zero and one hour")
		}
	}
	return nil
}
func Fresh(now time.Time, s Observation, max time.Duration) bool {
	if max == 0 {
		max = 15 * time.Second
	}
	return s.RunID != "" && !s.SampleAt.IsZero() && !s.ReceivedAt.IsZero() && !s.SampleAt.After(now) && !s.ReceivedAt.After(now) && now.Sub(s.SampleAt) <= max && now.Sub(s.ReceivedAt) <= max
}
func Eligible(now, pending time.Time, s Observation, p Policy) bool {
	if !now.Before(pending.Add(p.MaximumDelay)) {
		return true
	}
	switch p.Mode {
	case "immediate":
		return true
	case "when_empty":
		return Fresh(now, s, p.Freshness) && s.Ready && s.Players.Known && s.Players.Count == 0
	case "maintenance_window":
		loc, e := time.LoadLocation(p.Timezone)
		if e != nil {
			return false
		}
		v := now.In(loc).Format("15:04")
		if p.WindowStart > p.WindowEnd {
			return v >= p.WindowStart || v < p.WindowEnd
		}
		return v >= p.WindowStart && v < p.WindowEnd
	}
	return false
}
func Countdown(p Policy) time.Duration {
	var d time.Duration
	for _, v := range p.WarningOffsets {
		if v > d {
			d = v
		}
	}
	return d
}

// Decide is pure. Durable countdown/receipt bookkeeping belongs to Engine.
func Decide(now, pending time.Time, s Observation, p Policy) Decision {
	if p.Validate() != nil {
		return Decision{"wait", "invalid_policy"}
	}
	if !Eligible(now, pending, s, p) {
		return Decision{"wait", "policy_not_eligible"}
	}
	if !now.Before(pending.Add(p.MaximumDelay)) {
		return Decision{"restart", "maximum_delay"}
	}
	if now.Before(pending.Add(Countdown(p))) {
		return Decision{"warn", "countdown"}
	}
	return Decision{"restart", "eligible"}
}

func FromConfig(c config.RestartPolicy) Policy {
	c.Defaults()
	p := Policy{Mode: c.Mode, MaximumDelay: time.Duration(c.MaxDelaySeconds) * time.Second, Timezone: c.Timezone, WindowStart: c.WindowStart, WindowEnd: c.WindowEnd, Freshness: time.Duration(c.FreshnessSeconds) * time.Second}
	for _, v := range c.WarningSeconds {
		p.WarningOffsets = append(p.WarningOffsets, time.Duration(v)*time.Second)
	}
	return p
}

type Players struct {
	Known bool
	Count int
}
type Observation struct {
	RunID                string
	Ready                bool
	SampleAt, ReceivedAt time.Time
	Players              Players
}
