// SPDX-License-Identifier: GPL-3.0-or-later
package plugins

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
)

const PolicyFilename = "s2script-plugins.json"

type Policy struct {
	SchemaVersion int              `json:"schema_version"`
	Plugins       map[string]Entry `json:"plugins"`
}
type Entry struct {
	Source  string   `json:"source"`
	Version string   `json:"version"`
	Enabled *bool    `json:"enabled,omitempty"`
	With    []string `json:"with,omitempty"`
}

func EmptyPolicy() Policy       { return Policy{SchemaVersion: 1, Plugins: map[string]Entry{}} }
func validSource(s string) bool { return s == "release" || s == "registry" || s == "local" }
func validatePolicy(p *Policy) error {
	if p.SchemaVersion != 1 || p.Plugins == nil {
		return errors.New("unsupported or invalid plugin policy schema_version (requires 1 and plugins object)")
	}
	for name, e := range p.Plugins {
		if !validName(name) || !validSource(e.Source) {
			return fmt.Errorf("invalid plugin identity or source: %q", name)
		}
		v, err := NormalizeVersion(e.Version)
		if err != nil {
			return fmt.Errorf("plugin %s: %w", name, err)
		}
		e.Version = v
		seen := map[string]bool{}
		for _, w := range e.With {
			if !validName(w) || seen[w] {
				return fmt.Errorf("invalid or duplicate optional plugin identity for %s", name)
			}
			seen[w] = true
		}
		p.Plugins[name] = e
	}
	return nil
}

// canonicalObject checks only a declared schema object. Identity-keyed maps and
// upstream raw JSON are data, so callers deliberately do not recurse into them.
// Deriving names from JSON tags keeps this boundary aligned with durable types.
func canonicalObject[T any](raw []byte) (map[string]json.RawMessage, error) {
	var object map[string]json.RawMessage
	if e := json.Unmarshal(raw, &object); e != nil {
		return nil, e
	}
	if object == nil {
		return nil, errors.New("expected JSON object")
	}
	allowed := map[string]bool{}
	typ := reflect.TypeFor[T]()
	for i := 0; i < typ.NumField(); i++ {
		name := strings.Split(typ.Field(i).Tag.Get("json"), ",")[0]
		if name != "" && name != "-" {
			allowed[name] = true
		}
	}
	for name := range object {
		if !allowed[name] {
			return nil, fmt.Errorf("noncanonical or unknown JSON field %q", name)
		}
	}
	return object, nil
}
func validatePolicyFields(raw []byte) error {
	object, e := canonicalObject[Policy](raw)
	if e != nil {
		return e
	}
	var entries map[string]json.RawMessage
	if e = json.Unmarshal(object["plugins"], &entries); e != nil {
		return e
	}
	for name, entry := range entries {
		if _, e = canonicalObject[Entry](entry); e != nil {
			return fmt.Errorf("plugin %s: %w", name, e)
		}
	}
	return nil
}

// decodeStrict rejects duplicate object keys and nulls rather than allowing
// ambiguous operator policy. Lock/manifest raw extensions may contain null.
func decodeStrict(b []byte, v any) error { return decodeDocument(b, v, false, true) }
func decodeDocument(b []byte, v any, allowNull, strict bool) error {
	d := json.NewDecoder(bytes.NewReader(b))
	var value func() error
	value = func() error {
		tok, e := d.Token()
		if e != nil {
			return e
		}
		if tok == nil {
			if allowNull {
				return nil
			}
			return errors.New("null is not supported")
		}
		delim, ok := tok.(json.Delim)
		if !ok {
			return nil
		}
		switch delim {
		case '{':
			seen := map[string]bool{}
			for d.More() {
				key, e := d.Token()
				if e != nil {
					return e
				}
				s, ok := key.(string)
				if !ok || seen[s] {
					return errors.New("duplicate or invalid JSON key")
				}
				seen[s] = true
				if e = value(); e != nil {
					return e
				}
			}
		case '[':
			for d.More() {
				if e = value(); e != nil {
					return e
				}
			}
		default:
			return errors.New("unexpected JSON delimiter")
		}
		_, e = d.Token()
		return e
	}
	if e := value(); e != nil {
		return e
	}
	if _, e := d.Token(); e != io.EOF {
		return errors.New("trailing JSON data")
	}
	d = json.NewDecoder(bytes.NewReader(b))
	if strict {
		d.DisallowUnknownFields()
	}
	return d.Decode(v)
}
func ReadPolicy(path string) (Policy, error) {
	p := EmptyPolicy()
	b, e := readSafe(path, 1<<20)
	if errors.Is(e, os.ErrNotExist) {
		return p, nil
	}
	if e != nil {
		return Policy{}, e
	}
	if e = validatePolicyFields(b); e != nil {
		return Policy{}, fmt.Errorf("invalid plugin policy: %w", e)
	}
	p = Policy{}
	if e = decodeStrict(b, &p); e != nil {
		return Policy{}, fmt.Errorf("invalid plugin policy: %w", e)
	}
	if e = validatePolicy(&p); e != nil {
		return Policy{}, e
	}
	return p, nil
}
func ResolveEnabled(name string, desired *bool, upstream bool, panel string) (bool, error) {
	if !validName(name) {
		return false, errors.New("invalid plugin identity")
	}
	switch panel {
	case "1":
		return true, nil
	case "0":
		return false, nil
	case "", "default":
		if desired != nil {
			return *desired, nil
		}
		return upstream, nil
	default:
		return false, errors.New("plugin panel override must be default, 0 or 1")
	}
}

var filenamePattern = regexp.MustCompile(`^[a-zA-Z0-9._-]+\.s2sp$`)

// Target returns a path relative to game/csgo, never the server root. Callers
// applying artifacts must prepend game/csgo/ exactly once.
func Target(filename string, enabled bool) (string, error) {
	if !filenamePattern.MatchString(filename) || filepath.Base(filename) != filename || strings.HasPrefix(filename, ".") || len(filename) > 240 {
		return "", errors.New("unsafe plugin filename")
	}
	dir := "addons/s2script/plugins/"
	if !enabled {
		dir += "disabled/"
	}
	return dir + filename, nil
}
