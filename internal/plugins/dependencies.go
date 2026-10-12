// SPDX-License-Identifier: GPL-3.0-or-later
package plugins

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/s2script/s2script-cs2-egg/internal/config"
	"github.com/s2script/s2script-cs2-egg/internal/framework"
)

func compatible(m Manifest, s framework.Selection) error {
	if !s.SupportsAPI(m.APIVersion) {
		return errors.New("plugin API incompatible with selected host")
	}
	var raw struct {
		InterfaceProtocol int `json:"interfaceProtocol"`
	}
	if e := json.Unmarshal(m.Raw, &raw); e != nil {
		return e
	}
	if raw.InterfaceProtocol != 0 && raw.InterfaceProtocol != 1 && raw.InterfaceProtocol != 2 {
		return errors.New("unsupported interface protocol")
	}
	if raw.InterfaceProtocol == 2 && !strings.HasPrefix(strings.TrimLeft(m.APIVersion, "<>=^~"), fmt.Sprint(s.Protocol2APIMajor)+".") {
		return errors.New("interface protocol 2 requires selected protocol API major")
	}
	return nil
}

// Only the two API2 phantom dependencies covered by the exact host3 loader
// regression are exempted. A namespace prefix is never a builtin guarantee.
func legacyBuiltin(m Manifest, name string, s framework.Selection) bool {
	return s.HostAPIMajor == 3 && s.SupportsAPI("2.x") && strings.HasPrefix(strings.TrimLeft(m.APIVersion, "<>=^~"), "2.") && (name == "@s2script/entity" || name == "@s2script/math")
}
func dependencyGraph(facts []Installed, s framework.Selection) (map[string][]string, error) {
	type provider struct{ name, version, hash string }
	providers := map[string]provider{}
	manifests := map[string]Manifest{}
	graph := map[string][]string{}
	for _, p := range facts {
		m, e := ParseManifest(p.Manifest)
		if e != nil {
			return nil, e
		}
		manifests[p.Name] = m
		for name, published := range m.Publishes {
			if _, ok := providers[name]; ok {
				return nil, fmt.Errorf("multiple providers publish %s", name)
			}
			providers[name] = provider{p.Name, published.Version, published.TypesSHA256}
		}
	}
	for name, m := range manifests {
		var compiled map[string]string
		if len(m.CompiledAgainst) > 0 {
			if e := json.Unmarshal(m.CompiledAgainst, &compiled); e != nil {
				return nil, errors.New("invalid compiledAgainst evidence")
			}
		}
		for iface, version := range m.PluginDependencies {
			if legacyBuiltin(m, iface, s) {
				continue
			}
			provider, ok := providers[iface]
			if !ok {
				return nil, fmt.Errorf("%w: %s needs %s", ErrDependencyEvidenceUnavailable, name, iface)
			}
			if !satisfies(provider.version, version) {
				return nil, fmt.Errorf("published contract version conflict: %s needs %s", name, iface)
			}
			graph[name] = append(graph[name], provider.name)
		}
		for iface, hash := range compiled {
			if p, ok := providers[iface]; ok && hash != "" && p.hash != "" && hash != p.hash {
				return nil, fmt.Errorf("compiledAgainst contract drift: %s", iface)
			}
		}
		for iface, version := range m.OptionalPluginDependencies {
			if p, ok := providers[iface]; ok && !satisfies(p.version, version) {
				return nil, fmt.Errorf("optional published contract version conflict: %s", iface)
			}
		}
	}
	visiting := map[string]int{}
	var visit func(string) error
	visit = func(name string) error {
		if visiting[name] == 1 {
			return errors.New("plugin dependency cycle")
		}
		if visiting[name] == 2 {
			return nil
		}
		visiting[name] = 1
		for _, dep := range graph[name] {
			if e := visit(dep); e != nil {
				return e
			}
		}
		visiting[name] = 2
		return nil
	}
	for name := range manifests {
		if e := visit(name); e != nil {
			return nil, e
		}
	}
	return graph, nil
}

// Direct CLI operations use verified live framework evidence only when the
// legacy phantom-dependency exemption is needed; normal published interfaces
// need no native state read. No network requests or locks are acquired here.
func directDependencies(ctx context.Context, cfg config.Server, all []Installed) (map[string][]string, error) {
	selection := framework.Selection{}
	needsHost := false
	pins := map[string]string{}
	for _, p := range all {
		m, e := ParseManifest(p.Manifest)
		if e != nil {
			return nil, e
		}
		if p.Source == "release" {
			pins[p.Name] = p.Version
		}
		if strings.HasPrefix(strings.TrimLeft(m.APIVersion, "<>=^~"), "2.") {
			for name := range m.PluginDependencies {
				if name == "@s2script/entity" || name == "@s2script/math" {
					needsHost = true
				}
			}
		}
	}
	if needsHost {
		var e error
		selection, e = framework.Retained(ctx, cfg, pins)
		if e != nil {
			return nil, fmt.Errorf("legacy builtin needs verified live framework evidence: %w", e)
		}
	}
	for _, p := range all {
		if p.LastCheckStatus == "missing" {
			continue
		}
		target, e := Target(p.Filename, p.Enabled)
		if e != nil {
			return nil, e
		}
		b, e := readSafe(filepath.Join(cfg.Root, "game/csgo", target), maxPluginBytes)
		if e != nil || fileSHA256(b) != p.SHA256 {
			return nil, errors.New("dependency artifact changed during inspection")
		}
	}
	return dependencyGraph(all, selection)
}
func checkDirectEnable(ctx context.Context, cfg config.Server, name string, all []Installed) error {
	graph, e := directDependencies(ctx, cfg, all)
	if e != nil {
		return e
	}
	enabled := map[string]bool{}
	for _, p := range all {
		enabled[p.Name] = p.Enabled && p.LastCheckStatus != "missing"
	}
	enabled[name] = true
	for consumer, providers := range graph {
		if enabled[consumer] {
			for _, provider := range providers {
				if !enabled[provider] {
					return fmt.Errorf("enabled %s requires disabled %s; reconcile policy to enable its closure", consumer, provider)
				}
			}
		}
	}
	return nil
}
func checkDirectRemoval(ctx context.Context, cfg config.Server, candidate Installed, all []Installed, disable bool) error {
	graph, e := directDependencies(ctx, cfg, all)
	if e != nil {
		return e
	}
	return checkConsumers(candidate, all, graph, disable)
}
func checkConsumers(candidate Installed, all []Installed, graph map[string][]string, disable bool) error {
	for _, p := range all {
		if p.Name == candidate.Name || disable && !p.Enabled {
			continue
		}
		for _, provider := range graph[p.Name] {
			if provider == candidate.Name {
				return fmt.Errorf("plugin is still needed by %s", p.Name)
			}
		}
	}
	if !disable {
		for _, parent := range candidate.Parents {
			known := false
			for _, p := range all {
				if p.Name == parent {
					known = true
				}
			}
			if !known {
				return errors.New("plugin is still needed by a retained bundle; reconcile after removing its policy")
			}
		}
	}
	return nil
}
