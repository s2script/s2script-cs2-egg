// SPDX-License-Identifier: GPL-3.0-or-later
package addons

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"golang.org/x/sys/unix"
)

const s2NativeManifestPath = "game/csgo/addons/s2script/plugins.json"

// CheckS2ScriptPlugins prepares bundled controls; the native runtime alone
// resolves registry dependencies and downloads plugins during its startup.
func (e *Env) CheckS2ScriptPlugins(_ context.Context, getenv func(string) string) error {
	return ConfigureS2ScriptPlugins(e.Root, getenv)
}

// The native runtime owns registry resolution and plugins-lock.json. Keep its
// manifest as raw fields so panel overrides preserve settings added upstream.
type s2NativeManifest struct {
	raw          map[string]json.RawMessage
	dependencies map[string]string
	disabled     map[string]bool
}

// Framework releases must also leave registry-owned transitive packages alone.
func (s *s2PluginStore) nativeOwned(m *s2NativeManifest) (map[string]bool, error) {
	owned := map[string]bool{}
	for name := range m.dependencies {
		owned[name] = true
	}
	var lock struct {
		Version  int `json:"lockfileVersion"`
		Packages map[string]struct {
			Name string `json:"name"`
		} `json:"packages"`
	}
	err := s.readJSON("game/csgo/addons/s2script/plugins-lock.json", &lock)
	if errors.Is(err, unix.ENOENT) {
		return owned, nil
	}
	if err != nil {
		return nil, err
	}
	if lock.Version != 3 || lock.Packages == nil {
		return nil, errors.New("invalid native plugins-lock.json")
	}
	for path, plugin := range lock.Packages {
		if path != "" {
			if !s2PluginName.MatchString(plugin.Name) {
				return nil, errors.New("invalid native locked plugin identity")
			}
			owned[plugin.Name] = true
		}
	}
	return owned, nil
}

func (s *s2PluginStore) nativeManifest() (*s2NativeManifest, error) {
	m := &s2NativeManifest{dependencies: map[string]string{}, disabled: map[string]bool{}}
	if err := s.readJSON(s2NativeManifestPath, &m.raw); err != nil {
		if errors.Is(err, unix.ENOENT) {
			return nil, nil
		}
		return nil, err
	}
	if m.raw == nil {
		return nil, errors.New("native plugins.json must be an object")
	}
	if raw, ok := m.raw["dependencies"]; ok {
		if err := s2JSON(raw, &m.dependencies); err != nil || m.dependencies == nil {
			return nil, errors.New("native plugins.json dependencies must map plugin names to ranges")
		}
		for name, version := range m.dependencies {
			if !s2PluginName.MatchString(name) || version == "" {
				return nil, errors.New("invalid native plugins.json dependency")
			}
		}
	}
	if raw, ok := m.raw["disabled"]; ok {
		var disabled []string
		if err := s2JSON(raw, &disabled); err != nil || disabled == nil {
			return nil, errors.New("native plugins.json disabled must be an array of plugin names")
		}
		for _, name := range disabled {
			if _, ok := m.dependencies[name]; !ok {
				return nil, fmt.Errorf("native plugins.json disabled plugin %s is not in dependencies", name)
			}
			m.disabled[name] = true
		}
	}
	return m, nil
}

func (s *s2PluginStore) configureNative(m *s2NativeManifest, overrides map[string]bool) error {
	all, err := s.discover()
	if err != nil {
		return err
	}
	all, err = s.retained(all)
	if err != nil {
		return err
	}
	updated := append([]s2Installed(nil), all...)
	for i, plugin := range all {
		if enabled, ok := overrides[plugin.Name]; ok && plugin.LastCheckStatus != "missing" {
			updated[i].Enabled = enabled
			updated[i].Path = s2Target(plugin.Filename, enabled)
		}
	}
	changed := false
	for name, enabled := range overrides {
		if _, managed := m.dependencies[name]; managed && m.disabled[name] == enabled {
			changed = true
			if enabled {
				delete(m.disabled, name)
			} else {
				m.disabled[name] = true
			}
		}
	}
	if !changed {
		return s.applyNative(all, updated)
	}
	disabled := make([]string, 0, len(m.disabled))
	for name := range m.disabled {
		disabled = append(disabled, name)
	}
	sort.Strings(disabled)
	m.raw["disabled"], err = json.Marshal(disabled)
	if err != nil {
		return err
	}
	dir, err := s2OpenDir(s.root, filepath.Dir(s2NativeManifestPath), false)
	if err != nil {
		return err
	}
	defer dir.Close()
	old, err := s2ReadAt(dir, "plugins.json", 4<<20)
	if err != nil {
		return err
	}
	if err = s.writeJSON(s2NativeManifestPath, m.raw); err != nil {
		return err
	}
	if err = s.applyNative(all, updated); err != nil {
		if restoreErr := s2WriteAt(dir, "plugins.json", old); restoreErr != nil {
			return errors.Join(err, restoreErr)
		}
		return err
	}
	return nil
}

// Moving a native-owned archive must move its ownership entry too, including
// renamed packages. Otherwise the native updater loses its pruning evidence.
// Leave the lock's root settings stale so S2Script resolves the changed policy.
func (s *s2PluginStore) applyNative(before, after []s2Installed) error {
	moves := map[string]s2Installed{}
	for i, old := range before {
		if old.Path != after[i].Path && old.LastCheckStatus != "missing" {
			moves[old.Path] = after[i]
		}
	}
	if len(moves) == 0 {
		return s.s2Apply(before, after, nil)
	}
	dir, err := s2OpenDir(s.root, "game/csgo/addons/s2script", false)
	if err != nil {
		return err
	}
	defer dir.Close()
	old, err := s2ReadAt(dir, "plugins-lock.json", 4<<20)
	if errors.Is(err, unix.ENOENT) {
		return s.s2Apply(before, after, nil)
	}
	if err != nil {
		return err
	}
	var lock map[string]json.RawMessage
	if err = s2JSON(old, &lock); err != nil {
		return err
	}
	var version int
	if err = s2JSON(lock["lockfileVersion"], &version); err != nil || version != 3 {
		return errors.New("invalid native plugins-lock.json version")
	}
	var packages map[string]json.RawMessage
	if err = s2JSON(lock["packages"], &packages); err != nil || packages == nil {
		return errors.New("invalid native plugins-lock.json packages")
	}
	changed := false
	const prefix = "game/csgo/addons/s2script/"
	for from, plugin := range moves {
		from, to := strings.TrimPrefix(from, prefix), strings.TrimPrefix(plugin.Path, prefix)
		entry, exists := packages[from]
		if !exists {
			continue
		}
		var fact struct{ Name, Version, Integrity string }
		if err = s2JSON(entry, &fact); err != nil {
			return err
		}
		hash, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(fact.Integrity, "sha256-"))
		if err != nil || !strings.HasPrefix(fact.Integrity, "sha256-") || fmt.Sprintf("%x", hash) != plugin.SHA256 || fact.Name != plugin.Name || fact.Version != plugin.Version {
			return fmt.Errorf("native locked plugin %s has changed; refusing to move its ownership", plugin.Name)
		}
		if _, occupied := packages[to]; occupied {
			return errors.New("native locked plugin destination is occupied")
		}
		packages[to] = entry
		delete(packages, from)
		changed = true
	}
	if !changed {
		return s.s2Apply(before, after, nil)
	}
	lock["packages"], err = json.Marshal(packages)
	if err != nil {
		return err
	}
	if err = s.writeJSON("game/csgo/addons/s2script/plugins-lock.json", lock); err != nil {
		return err
	}
	if err = s.s2Apply(before, after, nil); err != nil {
		return errors.Join(err, s2WriteAt(dir, "plugins-lock.json", old))
	}
	return nil
}
