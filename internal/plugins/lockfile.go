// SPDX-License-Identifier: GPL-3.0-or-later
package plugins

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/s2script/s2script-cs2-egg/internal/artifact"
)

const LockFilename = "s2script-plugins.lock.json"

type Lock struct {
	SchemaVersion int            `json:"schema_version"`
	Plugins       []Installed    `json:"plugins"`
	RegistryRoots []RootEvidence `json:"registry_roots,omitempty"`
}

// RootEvidence records a verified plan, including non-artifact bundle roots.
// It is evidence about a resolution, never an invented installed ZIP manifest.
type RootEvidence struct {
	Name     string       `json:"name"`
	Version  string       `json:"version"`
	Range    string       `json:"range"`
	With     []string     `json:"with"`
	Registry string       `json:"registry"`
	Members  []RootMember `json:"members"`
}
type RootMember struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	SHA256  string `json:"sha256"`
}

func validateLock(l Lock) error {
	if l.SchemaVersion != 1 || l.Plugins == nil {
		return errors.New("unsupported or invalid plugin lock schema_version")
	}
	names := map[string]bool{}
	filenames := map[string]bool{}
	for _, p := range l.Plugins {
		b, e := hex.DecodeString(p.SHA256)
		if !validName(p.Name) || names[p.Name] || !ExactVersion(p.Version) || !validSource(p.Source) || e != nil || len(b) != 32 || p.SHA256 != strings.ToLower(p.SHA256) {
			return errors.New("invalid or duplicate plugin lock facts")
		}
		names[p.Name] = true
		if _, e = Target(p.Filename, p.Enabled); e != nil {
			return e
		}
		if filenames[p.Filename] {
			return errors.New("duplicate plugin lock filename")
		}
		filenames[p.Filename] = true
		if p.Verification != "verified" && p.Verification != "unverified" {
			return errors.New("invalid plugin verification state")
		}
		if p.Managed && (p.Source == "local" || p.Verification != "verified") {
			return errors.New("managed plugin lacks verified source")
		}
		m, e := ParseManifest(p.Manifest)
		if e != nil || m.ID != p.Name || m.Version != p.Version {
			return errors.New("lock manifest does not match installed identity")
		}
		for _, parent := range p.Parents {
			if !validName(parent) {
				return errors.New("invalid dependency parent")
			}
		}
	}
	roots := map[string]bool{}
	for _, root := range l.RegistryRoots {
		if !validName(root.Name) || roots[root.Name] || !ExactVersion(root.Version) || root.Members == nil || len(root.Members) == 0 || root.With == nil {
			return errors.New("invalid registry root evidence")
		}
		roots[root.Name] = true
		if v, e := NormalizeVersion(root.Range); e != nil || v != root.Range || !satisfies(root.Version, v) {
			return errors.New("invalid registry root selector")
		}
		if _, e := safeRegistryURL(root.Registry); e != nil {
			return e
		}
		members := map[string]bool{}
		for _, member := range root.Members {
			if !validName(member.Name) || members[member.Name] || !ExactVersion(member.Version) || !validHash(member.SHA256) {
				return errors.New("invalid registry member evidence")
			}
			members[member.Name] = true
			found := false
			for _, p := range l.Plugins {
				if p.Name == member.Name && p.Version == member.Version && p.SHA256 == member.SHA256 && p.Source == "registry" && p.Managed && p.Verification == "verified" {
					found = true
				}
			}
			if !found {
				return errors.New("registry member evidence has no matching installed artifact")
			}
		}
		with := map[string]bool{}
		for _, name := range root.With {
			if !validName(name) || with[name] || !members[name] {
				return errors.New("invalid registry optional member evidence")
			}
			with[name] = true
		}
	}
	return nil
}
func validateLockFields(raw []byte) error {
	object, e := canonicalObject[Lock](raw)
	if e != nil {
		return e
	}
	var entries []json.RawMessage
	if e = json.Unmarshal(object["plugins"], &entries); e != nil {
		return e
	}
	for _, entry := range entries {
		if _, e = canonicalObject[Installed](entry); e != nil {
			return e
		}
	}
	var roots []json.RawMessage
	if raw, ok := object["registry_roots"]; ok {
		if string(raw) == "null" {
			return errors.New("null registry roots")
		}
		if e = json.Unmarshal(raw, &roots); e != nil {
			return e
		}
	}
	for _, raw := range roots {
		root, e := canonicalObject[RootEvidence](raw)
		if e != nil {
			return e
		}
		var members []json.RawMessage
		if e = json.Unmarshal(root["members"], &members); e != nil {
			return e
		}
		for _, member := range members {
			if _, e = canonicalObject[RootMember](member); e != nil {
				return e
			}
		}
	}
	return nil
}

func ReadLock(root string) (Lock, error) {
	l := Lock{SchemaVersion: 1, Plugins: []Installed{}}
	b, e := readSafe(filepath.Join(root, LockFilename), 4<<20)
	if errors.Is(e, os.ErrNotExist) {
		return l, nil
	}
	if e != nil {
		return l, e
	}
	if e = validateLockFields(b); e != nil {
		return l, e
	}
	l = Lock{}
	if e = decodeDocument(b, &l, true, true); e != nil {
		return l, e
	}
	return l, validateLock(l)
}

// StageLock returns a server-root-relative change. The caller owns runtimelease,
// artifact.Acquire/Recover and the combined artifact.Apply transaction.
func StageLock(stage string, plugins []Installed) (artifact.Change, error) {
	// Direct CLI mutation deliberately invalidates prior plan membership. A fresh
	// online reconcile re-establishes it before registry-outage fallback is allowed.
	return StageLockWithRoots(stage, plugins, nil)
}
func StageLockWithRoots(stage string, plugins []Installed, roots []RootEvidence) (artifact.Change, error) {
	p := append([]Installed{}, plugins...)
	sort.Slice(p, func(i, j int) bool { return p[i].Name < p[j].Name })
	l := Lock{SchemaVersion: 1, Plugins: p, RegistryRoots: roots}
	if e := validateLock(l); e != nil {
		return artifact.Change{}, e
	}
	b, e := json.MarshalIndent(l, "", "  ")
	if e != nil {
		return artifact.Change{}, e
	}
	if len(b) > 4<<20 {
		return artifact.Change{}, errors.New("plugin lock exceeds size limit")
	}
	path := filepath.Join(stage, "plugins-lock.json")
	if e = os.WriteFile(path, append(b, '\n'), 0600); e != nil {
		return artifact.Change{}, e
	}
	return artifact.Change{Path: LockFilename, Source: path}, nil
}
