// SPDX-License-Identifier: GPL-3.0-or-later
package framework

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/s2script/s2script-cs2-egg/internal/artifact"
)

const HUDAddon = "3790153369"

// MergeHUDConfig preserves unrelated text/comments and appends the HUD addon to
// an existing comma-separated list. Ambiguous multiple assignments fail closed.
func MergeHUDConfig(data []byte) ([]byte, error) { return setHUDConfig(data, true) }
func setHUDConfig(data []byte, enabled bool) ([]byte, error) {
	if len(data) > 1<<20 {
		return nil, errors.New("HUD configuration is oversized")
	}
	re := regexp.MustCompile(`(?m)^[\t ]*mm_extra_addons[\t ]+"([^"\r\n]*)"[\t ]*(?://[^\r\n]*)?$`)
	matches := re.FindAllSubmatchIndex(data, -1)
	if len(matches) > 1 {
		return nil, errors.New("multiple mm_extra_addons assignments")
	}
	// Refuse unrecognized active assignment syntax rather than override it.
	assignments := regexp.MustCompile(`(?m)^[\t ]*mm_extra_addons\b`).FindAllIndex(data, -1)
	if len(assignments) != len(matches) {
		return nil, errors.New("unsupported mm_extra_addons syntax")
	}
	if len(matches) == 0 {
		if !enabled {
			return bytes.Clone(data), nil
		}
		b := bytes.Clone(data)
		if len(b) > 0 && b[len(b)-1] != '\n' {
			b = append(b, '\n')
		}
		return append(b, []byte("mm_extra_addons \""+HUDAddon+"\"\n")...), nil
	}
	m := matches[0]
	old := string(data[m[2]:m[3]])
	if !enabled {
		kept := []string{}
		for _, id := range strings.Split(old, ",") {
			if strings.TrimSpace(id) != HUDAddon {
				kept = append(kept, id)
			}
		}
		out := bytes.Clone(data[:m[2]])
		out = append(out, []byte(strings.Join(kept, ","))...)
		return append(out, data[m[3]:]...), nil
	}
	for _, id := range strings.Split(old, ",") {
		if strings.TrimSpace(id) == HUDAddon {
			return bytes.Clone(data), nil
		}
	}
	value := old
	if strings.TrimSpace(value) != "" {
		value += ","
	}
	value += HUDAddon
	out := bytes.Clone(data[:m[2]])
	out = append(out, []byte(value)...)
	out = append(out, data[m[3]:]...)
	return out, nil
}

// prepareHUDOff reverses only the HUD addon setting after an owned installation.
// MultiAddonManager may serve other addons, so its files stay tracked and intact.
func prepareHUDOff(live *os.File, stage string) ([]artifact.Change, []managedFact, error) {
	b, e := readPinned(live, StatePath, 4<<20)
	if errors.Is(e, os.ErrNotExist) {
		return nil, nil, nil
	}
	if e != nil {
		return nil, nil, e
	}
	var previous frameworkState
	if json.Unmarshal(b, &previous) != nil || previous.SchemaVersion != 1 {
		return nil, nil, errors.New("invalid prior framework ownership state")
	}
	facts := []managedFact{}
	seen := map[string]bool{}
	for _, f := range previous.Managed {
		if f.Path != csgo+"addons/metamod/multiaddonmanager.vdf" && !strings.HasPrefix(f.Path, csgo+"addons/multiaddonmanager/") {
			continue
		}
		if !relative(f.Path) || !validDigest(f.SHA256) || seen[f.Path] {
			return nil, nil, errors.New("invalid managed HUD helper identity")
		}
		data, e := readPinned(live, f.Path, expandedLimit)
		if e != nil || sum(data) != f.SHA256 {
			return nil, nil, errors.New("managed HUD helper is missing or modified; resolve ownership before disabling")
		}
		seen[f.Path] = true
		facts = append(facts, f)
	}
	if len(facts) == 0 {
		if previous.Selection.HUD != nil {
			return nil, nil, errors.New("prior HUD selection has no helper ownership")
		}
		return nil, nil, nil
	}
	cfg := csgo + "cfg/multiaddonmanager/multiaddonmanager.cfg"
	b, e = readPinned(live, cfg, 1<<20)
	if errors.Is(e, os.ErrNotExist) {
		return nil, facts, nil
	}
	if e != nil {
		return nil, nil, e
	}
	disabled, e := setHUDConfig(b, false)
	if e != nil {
		return nil, nil, e
	}
	if bytes.Equal(b, disabled) {
		return nil, facts, nil
	}
	path := filepath.Join(stage, "hud-disabled-config")
	if e = os.WriteFile(path, disabled, 0600); e != nil {
		return nil, nil, e
	}
	return []artifact.Change{{Path: cfg, Source: path}}, facts, nil
}
