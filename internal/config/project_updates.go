// SPDX-License-Identifier: GPL-3.0-or-later
package config

import (
	"errors"
	"github.com/s2script/s2script-cs2-egg/internal/releasecontract"
	"path/filepath"
)

type ProjectUpdates struct {
	Enabled         bool     `json:"enabled"`
	Schedule        bool     `json:"schedule"`
	Activate        bool     `json:"activate"`
	Channel         string   `json:"channel"`
	Version         string   `json:"version"`
	AllowDowngrade  bool     `json:"allow_downgrade"`
	IntervalSeconds int      `json:"interval_seconds"`
	StartupSeconds  int      `json:"startup_seconds"`
	Root            string   `json:"root"`
	RequiredModules []string `json:"required_modules"`
}

func (p *ProjectUpdates) Validate(state string) error {
	if p.Channel == "" {
		p.Channel = "stable"
	}
	if p.Version == "" {
		p.Version = "latest"
	}
	if p.IntervalSeconds == 0 {
		p.IntervalSeconds = 86400
	}
	if p.StartupSeconds == 0 {
		p.StartupSeconds = 60
	}
	if p.Root == "" {
		p.Root = filepath.Join(state, "node-releases")
	}
	if p.Channel != "stable" && p.Channel != "prerelease" || p.IntervalSeconds < 300 || p.IntervalSeconds > 604800 || p.StartupSeconds < 5 || p.StartupSeconds > 300 || p.Root != filepath.Join(state, "node-releases") || len(p.RequiredModules) > 16 {
		return errors.New("invalid project update policy or managed release directory")
	}
	if p.Version != "latest" {
		v, e := releasecontract.ParseVersion(p.Version)
		if e != nil || p.Channel == "stable" && len(v.Pre) > 0 {
			return errors.New("invalid exact project release")
		}
	}
	known := map[string]bool{"shared": true, "workshop": true, "cache": true, "updates": true, "observability": true, "backup": true, "guard": true, "selfupdate": true}
	seen := map[string]bool{}
	for _, name := range p.RequiredModules {
		if !known[name] || seen[name] {
			return errors.New("invalid required module")
		}
		seen[name] = true
	}
	if !p.Enabled && (p.Schedule || p.Activate) {
		return errors.New("disabled project updates cannot schedule/activate")
	}
	return nil
}
