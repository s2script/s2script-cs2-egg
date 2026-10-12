// SPDX-License-Identifier: GPL-3.0-or-later
package config

import (
	"errors"
	"github.com/s2script/s2script-cs2-egg/internal/wings"
	"time"
	_ "time/tzdata"
)

type RestartPolicy struct {
	Mode             string `json:"mode"`
	MaxDelaySeconds  int    `json:"max_delay_seconds"`
	Timezone         string `json:"timezone,omitempty"`
	WindowStart      string `json:"window_start,omitempty"`
	WindowEnd        string `json:"window_end,omitempty"`
	WarningSeconds   []int  `json:"warning_seconds,omitempty"`
	FreshnessSeconds int    `json:"freshness_seconds"`
}

func (p *RestartPolicy) Defaults() {
	if len(p.WarningSeconds) == 0 {
		p.WarningSeconds = []int{0}
	}
	if p.Mode == "" {
		p.Mode = "when_empty"
	}
	if p.MaxDelaySeconds == 0 {
		p.MaxDelaySeconds = 3600
	}
	if p.FreshnessSeconds == 0 {
		p.FreshnessSeconds = 15
	}
}
func (p RestartPolicy) Validate() error {
	if p.MaxDelaySeconds < 1 || p.MaxDelaySeconds > 604800 || p.FreshnessSeconds < 1 || p.FreshnessSeconds > 300 || len(p.WarningSeconds) > 16 {
		return errors.New("invalid update policy timing")
	}
	switch p.Mode {
	case "immediate", "when_empty":
	case "maintenance_window":
		if _, e := time.LoadLocation(p.Timezone); e != nil || p.Timezone == "" {
			return errors.New("update window requires IANA timezone")
		}
		for _, s := range []string{p.WindowStart, p.WindowEnd} {
			if _, e := time.Parse("15:04", s); e != nil {
				return errors.New("invalid update window")
			}
		}
		if p.WindowStart == p.WindowEnd {
			return errors.New("equal maintenance endpoints")
		}
	default:
		return errors.New("invalid update mode")
	}
	for _, v := range p.WarningSeconds {
		if v < 0 || v > 3600 {
			return errors.New("invalid warning offset")
		}
	}
	return nil
}

type NodeUpdates struct {
	Enabled     bool         `json:"enabled"`
	PollSeconds int          `json:"poll_seconds"`
	Wings       wings.Config `json:"wings"`
}
type ServerUpdates struct {
	Enabled     bool          `json:"enabled"`
	PollSeconds int           `json:"poll_seconds"`
	Adapter     string        `json:"adapter"`
	Policy      RestartPolicy `json:"policy"`
}
type RegisteredUpdates struct {
	Enabled    bool          `json:"enabled"`
	AttachMode string        `json:"attach_mode,omitempty"`
	Policy     RestartPolicy `json:"policy"`
}

func (n NodeUpdates) Validate() error { _, e := wings.New(n.Wings); return e }
