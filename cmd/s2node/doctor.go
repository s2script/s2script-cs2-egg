// SPDX-License-Identifier: GPL-3.0-or-later
package main

import (
	"github.com/s2script/s2script-cs2-egg/internal/config"
	"path/filepath"
	"syscall"
)

type doctorCheck struct {
	Server     string `json:"server"`
	Name       string `json:"name"`
	Accessible bool   `json:"accessible"`
	Stopped    string `json:"stopped"`
}
type doctorReport struct {
	Schema        int           `json:"schema_version"`
	Configuration string        `json:"configuration"`
	GameReadiness string        `json:"game_readiness"`
	Checks        []doctorCheck `json:"checks"`
	Notes         []string      `json:"notes"`
}

func doctorResult(c config.Node) doctorReport {
	r := doctorReport{Schema: 1, Configuration: "valid", GameReadiness: "not_assessed", Checks: []doctorCheck{}, Notes: []string{"Path access does not prove stopped state. No locks are acquired.", "Private 0600 runtime files require the configured runtime UID helper for administrative operations.", "Systemd groups, mount confinement and remote delivery are not assessed."}}
	for _, s := range c.Servers {
		for _, n := range []string{".runtime-lease", ".artifact-lock", ".s2egg-status.sock"} {
			r.Checks = append(r.Checks, doctorCheck{Server: s.ID, Name: n, Accessible: syscall.Access(filepath.Join(s.Volume, n), 6) == nil, Stopped: "unknown"})
		}
	}
	return r
}
