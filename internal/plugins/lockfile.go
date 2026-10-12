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
	SchemaVersion int         `json:"schema_version"`
	Plugins       []Installed `json:"plugins"`
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
	p := append([]Installed{}, plugins...)
	sort.Slice(p, func(i, j int) bool { return p[i].Name < p[j].Name })
	l := Lock{1, p}
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
