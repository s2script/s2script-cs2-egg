// SPDX-License-Identifier: GPL-3.0-or-later
package plugins

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/s2script/s2script-cs2-egg/internal/artifact"
	"github.com/s2script/s2script-cs2-egg/internal/config"
	"github.com/s2script/s2script-cs2-egg/internal/framework"
	"github.com/s2script/s2script-cs2-egg/internal/protocol"
)

type Check struct {
	Name             string          `json:"name"`
	AvailableVersion string          `json:"available_version,omitempty"`
	Status           string          `json:"status"`
	Metadata         json.RawMessage `json:"metadata,omitempty"`
}
type Prepared struct {
	Changes   []artifact.Change
	Installed []Installed
	Checks    []Check
	StageDir  string
}

func (p Prepared) Cleanup() error {
	if p.StageDir == "" {
		return nil
	}
	return os.RemoveAll(p.StageDir)
}

// Prepare requires caller-held runtime and artifact locks, after Recover. Apply
// its changes together with framework changes once, then Cleanup both stages.
func Prepare(ctx context.Context, cfg config.Server, policy Policy, installed []Installed, selection framework.Selection, bundled []framework.BundledArtifact) (Prepared, error) {
	return (Registry{}).Prepare(ctx, cfg, policy, installed, selection, bundled)
}

type candidate struct {
	fact     Installed
	manifest Manifest
	path     string
}

func (r Registry) Prepare(ctx context.Context, cfg config.Server, policy Policy, installed []Installed, selection framework.Selection, bundled []framework.BundledArtifact) (Prepared, error) {
	out, e := r.prepare(ctx, cfg, policy, installed, selection, bundled, false)
	var unavailable *UnavailableError
	if errors.As(e, &unavailable) && ctx.Err() == nil {
		return r.prepare(ctx, cfg, policy, installed, selection, bundled, true)
	}
	return out, e
}
func (r Registry) prepare(ctx context.Context, cfg config.Server, policy Policy, installed []Installed, selection framework.Selection, bundled []framework.BundledArtifact, retained bool) (out Prepared, err error) {
	if err = ctx.Err(); err != nil {
		return out, err
	}
	if policy.Plugins == nil {
		return out, errors.New("plugin policy requires plugins object")
	}
	// Normalize a copy: desired policy and caller slices remain immutable.
	copyPolicy := EmptyPolicy()
	for n, e := range policy.Plugins {
		copyPolicy.Plugins[n] = e
	}
	copyPolicy.SchemaVersion = policy.SchemaVersion
	policy = copyPolicy
	if err = validatePolicy(&policy); err != nil {
		return out, err
	}
	if err = validateLock(Lock{SchemaVersion: 1, Plugins: append([]Installed{}, installed...)}); err != nil {
		return out, err
	}
	if len(selection.SupportedAPIMajors) != 2 || selection.HostAPIMajor != 3 || selection.Protocol2APIMajor != 3 || !selection.SupportsAPI("2.x") || !selection.SupportsAPI("3.x") {
		return out, errors.New("selected framework lacks supported API compatibility evidence")
	}
	stage, e := os.MkdirTemp("", "s2egg-plugins-")
	if e != nil {
		return out, e
	}
	out.StageDir = stage
	defer func() {
		if err != nil {
			out.Cleanup()
			out = Prepared{}
		}
	}()
	old := map[string]Installed{}
	all := map[string]*candidate{}
	desired := map[string]Entry{}
	roots := map[string]bool{}
	explicit := map[string]bool{}
	members := map[string][]string{}
	checks := map[string]Check{}
	selectedEntries := map[string]PlanEntry{}
	evidence := []RootEvidence{}
	saved, e := ReadLock(cfg.Root)
	if e != nil {
		return out, e
	}
	savedRoots := map[string]RootEvidence{}
	for _, root := range saved.RegistryRoots {
		savedRoots[root.Name] = root
	}
	for _, p := range installed {
		old[p.Name] = p
		desired[p.Name] = Entry{Source: p.Source, Version: "*"}
		roots[p.Name] = len(p.Parents) == 0
		m, e := ParseManifest(p.Manifest)
		if e != nil {
			return out, e
		}
		all[p.Name] = &candidate{fact: p, manifest: m}
		if p.LastCheckStatus != "missing" {
			path, _ := Target(p.Filename, p.Enabled)
			b, e := readSafe(filepath.Join(cfg.Root, "game/csgo", path), maxPluginBytes)
			if e != nil || fileSHA256(b) != p.SHA256 {
				return out, fmt.Errorf("installed plugin changed: %s", p.Name)
			}
			actual, e := manifestBytes(b)
			if e != nil || string(actual.Raw) != string(p.Manifest) {
				return out, errors.New("installed manifest evidence differs from artifact")
			}
			all[p.Name].path = filepath.Join(stage, p.SHA256+".s2sp")
			if e = os.WriteFile(all[p.Name].path, b, 0600); e != nil {
				return out, e
			}
		}
	}
	for _, b := range bundled {
		if _, ok := desired[b.Name]; !ok {
			desired[b.Name] = Entry{Source: "release", Version: "*"}
			roots[b.Name] = true
		}
	}
	for name, entry := range policy.Plugins {
		desired[name] = entry
		roots[name] = true
	}
	names := make([]string, 0, len(desired))
	for name := range desired {
		names = append(names, name)
	}
	sort.Strings(names)
	// Every inventoried identity gets a public metadata/availability check, even
	// when its local or release source intentionally prevents registry replacement.
	for _, name := range names {
		entry := desired[name]
		if p, ok := old[name]; ok && p.Source != entry.Source && p.Source != "local" {
			return out, fmt.Errorf("source conflict for %s", name)
		}
		check := Check{Name: name, Status: "checked"}
		var resolved RegistryPlan
		if retained {
			check.Status = "retained-unavailable"
			if entry.Source == "registry" {
				root, ok := savedRoots[name]
				// Implicit installed members are checked by their saved parent closure;
				// they need no fabricated standalone root record.
				if !ok && !roots[name] {
					for _, parent := range saved.RegistryRoots {
						for _, member := range parent.Members {
							if member.Name == name {
								root = RootEvidence{Name: name, Version: member.Version, Range: "*", With: []string{}, Registry: parent.Registry, Members: []RootMember{member}}
								ok = true
							}
						}
					}
				}
				if !ok || root.Registry != strings.TrimRight(r.defaults().BaseURL, "/") || !satisfies(root.Version, entry.Version) || !sameNames(root.With, entry.With) {
					return out, fmt.Errorf("registry unavailable: retained resolution cannot satisfy %s", name)
				}
				resolved = RegistryPlan{Root: &PlanRoot{Name: name, Version: root.Version}}
				for _, member := range root.Members {
					p, ok := old[member.Name]
					if !ok || p.LastCheckStatus == "missing" || !p.Managed || p.Verification != "verified" || p.Source != "registry" || p.Version != member.Version || p.SHA256 != member.SHA256 {
						return out, errors.New("registry unavailable: incomplete retained closure")
					}
					if e = r.trust(p.SourceURL); e != nil {
						return out, e
					}
					resolved.Install = append(resolved.Install, PlanEntry{Name: p.Name, Version: p.Version, SHA256: p.SHA256, URL: p.SourceURL, Filename: p.Filename, ReviewState: p.ReviewState})
				}
			}
		} else {
			meta, e := r.Meta(ctx, name)
			if e == nil {
				check.Metadata = meta.Raw
			} else {
				var status *HTTPError
				if !(entry.Source != "registry" && errors.As(e, &status) && status.Status == 404) {
					return out, e
				}
				check.Status = "not-found"
			}
			available, e := r.Plan(ctx, name, "*", entry.With)
			if e != nil {
				var status *HTTPError
				if !(entry.Source != "registry" && errors.As(e, &status) && status.Status == 404) {
					return out, e
				}
				check.Status = "not-found"
			} else {
				check.AvailableVersion = available.Root.Version
			}
			resolved = available
			if entry.Source == "registry" && entry.Version != "*" {
				resolved, e = r.Plan(ctx, name, entry.Version, entry.With)
				if e != nil {
					return out, e
				}
			}
		}
		checks[name] = check
		// Installed implicit members still get availability checks above. Their
		// selected artifacts and durable closure evidence belong to actual roots;
		// selecting their standalone wildcard plan could contradict a pinned parent.
		if entry.Source != "registry" || !roots[name] {
			continue
		}
		root := RootEvidence{Name: name, Version: resolved.Root.Version, Range: entry.Version, With: append([]string{}, entry.With...), Registry: strings.TrimRight(r.defaults().BaseURL, "/"), Members: []RootMember{}}
		sort.Strings(root.With)
		for _, p := range resolved.Install {
			root.Members = append(root.Members, RootMember{Name: p.Name, Version: p.Version, SHA256: p.SHA256})
		}
		sort.Slice(root.Members, func(i, j int) bool { return root.Members[i].Name < root.Members[j].Name })
		evidence = append(evidence, root)

		for _, w := range entry.With {
			found := false
			for _, p := range resolved.Install {
				if p.Name == w {
					found = true
				}
			}
			if !found {
				return out, fmt.Errorf("requested optional member %s absent from plan", w)
			}
		}
		for _, p := range resolved.Install {
			if cfg.Plugins.ReviewedOnly && p.ReviewState != "reviewed" {
				return out, fmt.Errorf("registry plugin %s is not reviewed", p.Name)
			}
			if previous, ok := selectedEntries[p.Name]; ok && previous != p {
				return out, fmt.Errorf("conflicting registry closures for %s", p.Name)
			}
			if source, ok := desired[p.Name]; ok && source.Source != "registry" {
				return out, fmt.Errorf("registry closure conflicts with %s source", p.Name)
			}
			selectedEntries[p.Name] = p
			members[name] = append(members[name], p.Name)
		}
	}
	entryNames := make([]string, 0, len(selectedEntries))
	for n := range selectedEntries {
		entryNames = append(entryNames, n)
	}
	sort.Strings(entryNames)
	for _, name := range entryNames {
		p := selectedEntries[name]
		if validator, ok := r.Fetcher.(artifact.SelectedValidator); ok {
			if e = validator.ValidateArtifact(ctx, protocol.ArtifactSelection{Source: "registry", Name: p.Name, Version: p.Version, SHA256: p.SHA256}); e != nil {
				return out, e
			}
		}
		path := filepath.Join(stage, p.SHA256+".s2sp")
		prior, exists := old[name]
		if exists && (!prior.Managed || prior.Verification != "verified") && (prior.SHA256 != p.SHA256 || prior.Version != p.Version) {
			return out, fmt.Errorf("local plugin cannot be adopted by name: %s", name)
		}
		if exists && prior.Version == p.Version && prior.SHA256 != p.SHA256 {
			return out, fmt.Errorf("immutable plugin version digest changed: %s", name)
		}
		if !exists || prior.SHA256 != p.SHA256 || all[name].path == "" {
			if e = r.Fetch(ctx, p, path); e != nil {
				return out, e
			}
		} else {
			path = all[name].path
		}
		m, e := ReadManifest(path)
		if e != nil {
			return out, e
		}
		if m.ID != name || m.Version != p.Version {
			return out, errors.New("registry archive manifest identity/version mismatch")
		}
		fact := Installed{Name: name, Version: p.Version, Source: "registry", SHA256: p.SHA256, Filename: p.Filename, Managed: true, Verification: "verified", SourceURL: p.URL, ReviewState: p.ReviewState, Manifest: m.Raw}
		all[name] = &candidate{fact: fact, manifest: m, path: path}
	}
	for _, b := range bundled {
		entry := desired[b.Name]
		if entry.Source != "release" {
			return out, fmt.Errorf("bundled source conflict for %s", b.Name)
		}
		if !satisfies(b.Version, entry.Version) {
			return out, errors.New("selected bundled version violates plugin pin")
		}
		data, e := readSafe(b.StagedPath, maxPluginBytes)
		if e != nil || fileSHA256(data) != b.SHA256 {
			return out, errors.New("bundled artifact digest mismatch")
		}
		m, e := manifestBytes(data)
		if e != nil || m.ID != b.Name || m.Version != b.Version || string(m.Raw) != string(b.Manifest) {
			return out, errors.New("bundled manifest evidence mismatch")
		}
		if prior, exists := old[b.Name]; exists && (!prior.Managed || prior.Verification != "verified") && prior.SHA256 != b.SHA256 {
			return out, errors.New("bundled artifact conflicts with local modification")
		}
		path := filepath.Join(stage, b.SHA256+".s2sp")
		if e = os.WriteFile(path, data, 0600); e != nil {
			return out, e
		}
		enabled := b.UpstreamEnabled
		all[b.Name] = &candidate{fact: Installed{Name: b.Name, Version: b.Version, Source: "release", SHA256: b.SHA256, Filename: b.Filename, Managed: true, Verification: "verified", SourceURL: selection.S2Script.URL, SourceRevision: selection.S2Script.SourceRevision, Generation: selection.S2Script.SHA256, UpstreamEnabled: &enabled, Manifest: m.Raw}, manifest: m, path: path}
	}
	for name, entry := range desired {
		c, ok := all[name]
		if !ok {
			if entry.Source == "registry" && len(members[name]) > 0 {
				continue
			}
			return out, fmt.Errorf("requested plugin has no complete artifact: %s", name)
		}
		if c.path == "" || !satisfies(c.fact.Version, entry.Version) {
			return out, fmt.Errorf("retained plugin cannot satisfy policy: %s", name)
		}
	}
	filenames := map[string]string{}
	for name, c := range all {
		if other, ok := filenames[c.fact.Filename]; ok && other != name {
			return out, errors.New("plugin filename collision")
		}
		filenames[c.fact.Filename] = name
		if e = compatible(c.manifest, selection); e != nil {
			return out, fmt.Errorf("%s: %w", name, e)
		}
		entry := desired[name]
		enabled := true
		if c.fact.UpstreamEnabled != nil {
			enabled = *c.fact.UpstreamEnabled
		}
		if prior, ok := old[name]; ok && (prior.LastCheckStatus != "missing" || c.fact.Source != "release") {
			enabled = prior.Enabled
		}
		if !roots[name] {
			enabled = false
		}
		enabled, e = ResolveEnabled(name, entry.Enabled, enabled, PanelOverride(cfg.Plugins.Overrides, name))
		if e != nil {
			return out, e
		}
		_, panel := cfg.Plugins.Overrides[name]
		explicit[name] = panel || entry.Enabled != nil
		c.fact.Enabled = enabled
		if prior, ok := old[name]; ok && !prior.Managed && prior.Source == "local" && entry.Source != "registry" && enabled != prior.Enabled {
			return out, errors.New("local plugin placement requires manual change")
		}
	}
	// Bundle members and implicit closure artifacts follow their root; explicit
	// disable remains authoritative and is validated against required edges below.
	for name, list := range members {
		enabled := true
		if c, ok := all[name]; ok {
			enabled = c.fact.Enabled
		} else {
			entry := desired[name]
			enabled, e = ResolveEnabled(name, entry.Enabled, true, PanelOverride(cfg.Plugins.Overrides, name))
			if e != nil {
				return out, e
			}
		}
		if enabled {
			for _, member := range list {
				if !explicit[member] {
					all[member].fact.Enabled = true
				}
			}
		}
	}
	facts := make([]Installed, 0, len(all))
	for _, c := range all {
		facts = append(facts, c.fact)
	}
	graph, e := dependencyGraph(facts, selection)
	if e != nil {
		return out, e
	}
	// Propagate enabled required consumers to implicit providers to a fixed point.
	for changed := true; changed; {
		changed = false
		for consumer, providers := range graph {
			if !all[consumer].fact.Enabled {
				continue
			}
			for _, provider := range providers {
				if !all[provider].fact.Enabled {
					if explicit[provider] {
						return out, fmt.Errorf("enabled %s requires disabled %s", consumer, provider)
					}
					all[provider].fact.Enabled = true
					changed = true
				}
			}
		}
	}
	parents := map[string]map[string]bool{}
	addParent := func(child, parent string) {
		if child == parent {
			return
		}
		if parents[child] == nil {
			parents[child] = map[string]bool{}
		}
		parents[child][parent] = true
	}
	for parent, children := range graph {
		for _, child := range children {
			addParent(child, parent)
		}
	}
	for parent, children := range members {
		for _, child := range children {
			addParent(child, parent)
		}
	}
	now := time.Now().UTC()
	for name, c := range all {
		c.fact.Parents = nil
		for parent := range parents[name] {
			c.fact.Parents = append(c.fact.Parents, parent)
		}
		sort.Strings(c.fact.Parents)
		check, ok := checks[name]
		if !ok {
			check = Check{Name: name, AvailableVersion: c.fact.Version, Status: "dependency-verified"}
		}
		if c.fact.Source == "local" {
			check.Status = "local-preserved"
		}
		c.fact.LastCheckStatus = check.Status
		c.fact.LastCheckTime = now
		checks[name] = check
		out.Installed = append(out.Installed, c.fact)
		previous, exists := old[name]
		if exists && previous.SHA256 == c.fact.SHA256 && previous.Filename == c.fact.Filename && previous.Enabled == c.fact.Enabled && previous.LastCheckStatus != "missing" {
			continue
		}
		if exists && !previous.Managed && c.fact.Source == "local" {
			continue
		}
		target, e := Target(c.fact.Filename, c.fact.Enabled)
		if e != nil {
			return out, e
		}
		if exists && previous.LastCheckStatus != "missing" {
			oldTarget, _ := Target(previous.Filename, previous.Enabled)
			if oldTarget != target {
				out.Changes = append(out.Changes, artifact.Change{Path: "game/csgo/" + oldTarget, Remove: true})
			}
		}
		out.Changes = append(out.Changes, artifact.Change{Path: "game/csgo/" + target, Source: c.path})
	}
	sort.Slice(out.Installed, func(i, j int) bool { return out.Installed[i].Name < out.Installed[j].Name })
	for _, c := range checks {
		out.Checks = append(out.Checks, c)
	}
	sort.Slice(out.Checks, func(i, j int) bool { return out.Checks[i].Name < out.Checks[j].Name })
	lock, e := StageLockWithRoots(stage, out.Installed, evidence)
	if e != nil {
		return out, e
	}
	out.Changes = append(out.Changes, lock)
	sort.Slice(out.Changes, func(i, j int) bool { return out.Changes[i].Path < out.Changes[j].Path })
	return out, nil
}
func sameNames(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	aa, bb := append([]string{}, a...), append([]string{}, b...)
	sort.Strings(aa)
	sort.Strings(bb)
	for i := range aa {
		if aa[i] != bb[i] {
			return false
		}
	}
	return true
}
