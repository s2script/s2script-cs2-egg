// SPDX-License-Identifier: GPL-3.0-or-later
package plugins

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type Installed struct {
	Name            string          `json:"name"`
	Version         string          `json:"version"`
	Source          string          `json:"source"`
	SHA256          string          `json:"sha256"`
	Filename        string          `json:"filename"`
	Enabled         bool            `json:"enabled"`
	Parents         []string        `json:"parents,omitempty"`
	LastCheckStatus string          `json:"last_check_status"`
	LastCheckTime   time.Time       `json:"last_check_time,omitzero"`
	Managed         bool            `json:"managed"`
	Verification    string          `json:"verification"`
	SourceURL       string          `json:"source_url,omitempty"`
	SourceRevision  string          `json:"source_revision,omitempty"`
	Generation      string          `json:"generation,omitempty"`
	ReviewState     string          `json:"review_state,omitempty"`
	UpstreamEnabled *bool           `json:"upstream_enabled,omitempty"`
	Manifest        json.RawMessage `json:"manifest"`
}

// Discover scans serverRoot/game/csgo. Filenames never determine package identity.
// Matching retained identity/version/digest/filename preserves ownership across
// manual active/disabled moves; modified or renamed files are local/unverified.
// Missing retained entries remain visible as missing for startup checks.
func Discover(root string) ([]Installed, error) {
	directory, e := openSafe(root, true)
	if e != nil {
		return nil, e
	}
	directory.Close()
	lock, e := ReadLock(root)
	if e != nil {
		return nil, e
	}
	retained := map[string]Installed{}
	for _, p := range lock.Plugins {
		retained[p.Name] = p
	}
	seen := map[string]bool{}
	result := []Installed{}
	for _, enabled := range []bool{true, false} {
		dir := "game/csgo/addons/s2script/plugins"
		if !enabled {
			dir += "/disabled"
		}
		d, e := openSafe(filepath.Join(root, dir), true)
		if errors.Is(e, os.ErrNotExist) {
			continue
		}
		if e != nil {
			return nil, e
		}
		entries, e := d.ReadDir(-1)
		d.Close()
		if e != nil {
			return nil, e
		}
		for _, entry := range entries {
			if !strings.HasSuffix(entry.Name(), ".s2sp") {
				continue
			}
			if _, e = Target(entry.Name(), enabled); e != nil {
				return nil, e
			}
			path := filepath.Join(root, dir, entry.Name())
			b, e := readSafe(path, maxPluginBytes)
			if e != nil {
				return nil, e
			}
			m, e := manifestBytes(b)
			if e != nil {
				return nil, fmt.Errorf("inspect %s: %w", entry.Name(), e)
			}
			if seen[m.ID] {
				return nil, fmt.Errorf("duplicate plugin identity %s", m.ID)
			}
			seen[m.ID] = true
			sum := sha256.Sum256(b)
			digest := hex.EncodeToString(sum[:])
			p := Installed{Name: m.ID, Version: m.Version, Source: "local", SHA256: digest, Filename: entry.Name(), Enabled: enabled, Verification: "unverified", LastCheckStatus: "unverified", Manifest: m.Raw}
			if old, ok := retained[m.ID]; ok {
				if old.Version == p.Version && old.SHA256 == p.SHA256 && old.Filename == p.Filename {
					p = old
					p.Enabled = enabled
					p.Manifest = m.Raw
					if p.LastCheckStatus == "missing" {
						p.LastCheckStatus = "rediscovered"
					}
				} else {
					p.LastCheckStatus = "modified"
				}
			}
			result = append(result, p)
		}
	}
	for _, old := range lock.Plugins {
		if !seen[old.Name] {
			old.LastCheckStatus = "missing"
			result = append(result, old)
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result, nil
}
