// SPDX-License-Identifier: GPL-3.0-or-later
// Package releasecontract is shared by the release producer and node updater.
package releasecontract

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/s2script/s2script-cs2-egg/internal/protocol"
	"io"
	"reflect"
	"regexp"
	"sort"
	"strings"
)

const Repository = "s2script/s2script-cs2-egg"
const ManifestName = "s2script-release-v1.json"
const License = "GPL-3.0-or-later"
const LauncherVersion = 1
const StateABI = 1
const MaxManifest = 256 << 10
const MaxBinary = 256 << 20

var revisionPattern = regexp.MustCompile(`^[a-f0-9]{40}$`)
var digestPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)
var identifier = regexp.MustCompile(`^[a-z][a-z0-9]*(?:[.-][a-z0-9]+)*$`)

type Range struct {
	Min int `json:"min"`
	Max int `json:"max"`
}

func (r Range) valid() bool         { return r.Min > 0 && r.Max >= r.Min && r.Max <= 65535 }
func (r Range) contains(i int) bool { return i >= r.Min && i <= r.Max }

type FileDigest struct {
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}
type Descriptor struct {
	Name   string `json:"name"`
	URL    string `json:"url"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}
type Artifact struct {
	Component string `json:"component"`
	OS        string `json:"os"`
	Arch      string `json:"arch"`
	Descriptor
}
type Source struct {
	Revision  string     `json:"revision"`
	CommitURL string     `json:"commit_url"`
	Archive   Descriptor `json:"archive"`
}
type Distribution struct {
	Egg        Descriptor `json:"egg"`
	Deployment Descriptor `json:"deployment"`
}
type Wire struct {
	Protocol     Range    `json:"protocol"`
	Capabilities []string `json:"capabilities"`
}
type Helper struct {
	DiscoveryVersion int                 `json:"discovery_version"`
	Protocol         Range               `json:"protocol"`
	Provides         []string            `json:"provides"`
	Requires         map[string][]string `json:"requires_by_operation"`
}
type State struct {
	Read  Range `json:"read"`
	Write int   `json:"write"`
}
type Compatibility struct {
	NodeContainer Wire   `json:"node_container"`
	Helper        Helper `json:"helper"`
	LauncherMin   int    `json:"launcher_min"`
	State         State  `json:"state"`
}
type Manifest struct {
	SchemaVersion int           `json:"schema_version"`
	Repository    string        `json:"repository"`
	Tag           string        `json:"tag"`
	Version       string        `json:"version"`
	Channel       string        `json:"channel"`
	License       string        `json:"license"`
	Source        Source        `json:"source"`
	Artifacts     []Artifact    `json:"artifacts"`
	Distribution  Distribution  `json:"distribution"`
	Compatibility Compatibility `json:"compatibility"`
}

func AssetURL(tag, name string) string {
	return "https://github.com/" + Repository + "/releases/download/" + tag + "/" + name
}
func Names(version string) map[string]string {
	tag := "v" + version
	return map[string]string{"s2node": "s2node-" + tag + "-linux-amd64", "s2egg": "s2egg-" + tag + "-linux-amd64", "source": "s2script-cs2-egg-" + tag + "-source.tar.gz", "egg": "egg-s2script-cs2.json", "deployment": "s2script-cs2-egg-" + tag + "-deployment.tar.gz"}
}

// Inventory describes implemented support, never grants runtime authority. ABI 1
// includes delivery format 2 and all currently shipped module journals.
func Inventory() Compatibility {
	return Compatibility{NodeContainer: Wire{Protocol: Range{protocol.Version, protocol.Version}, Capabilities: []string{protocol.CapStatus, protocol.CapEvents, protocol.CapPrepare, protocol.CapCache, protocol.CapWorkshop, protocol.CapRestart, protocol.CapUpdateControl, protocol.CapUpdateExecute, protocol.CapUpdateAdmin, protocol.CapOperations, protocol.CapBackupAdmin, protocol.CapGuardAdmin, protocol.CapProjectUpdate}}, Helper: Helper{DiscoveryVersion: 1, Protocol: Range{1, 1}, Provides: []string{"shared.attach.v1", "workshop.attach.v1", "operations.private.v1", "backup.catalog.v1", "guard.enforce.v1"}, Requires: map[string][]string{"shared.attach": {"shared.attach.v1"}, "workshop.attach": {"workshop.attach.v1"}, "operations.private": {"operations.private.v1"}, "backup.catalog": {"backup.catalog.v1"}, "guard.enforce": {"guard.enforce.v1"}}}, LauncherMin: 1, State: State{Read: Range{1, 1}, Write: 1}}
}
func New(version, revision string, files map[string]FileDigest) (Manifest, error) {
	m := Manifest{SchemaVersion: 1, Repository: Repository, Version: version, Tag: "v" + version, Channel: "stable", License: License, Compatibility: Inventory()}
	v, err := ParseVersion(version)
	if err != nil {
		return m, err
	}
	if len(v.Pre) > 0 {
		m.Channel = "prerelease"
	}
	names := Names(version)
	descs := map[string]Descriptor{}
	if len(files) != len(names) {
		return m, errors.New("exact release asset set required")
	}
	for key, name := range names {
		f := files[key]
		descs[key] = Descriptor{Name: name, URL: AssetURL(m.Tag, name), SHA256: f.SHA256, Size: f.Size}
	}
	m.Source = Source{Revision: revision, CommitURL: "https://github.com/" + Repository + "/commit/" + revision, Archive: descs["source"]}
	for _, key := range []string{"s2node", "s2egg"} {
		m.Artifacts = append(m.Artifacts, Artifact{Component: key, OS: "linux", Arch: "amd64", Descriptor: descs[key]})
	}
	m.Distribution = Distribution{Egg: descs["egg"], Deployment: descs["deployment"]}
	return m, m.Validate()
}
func identifiers(v []string) bool {
	if v == nil || len(v) > 128 {
		return false
	}
	seen := map[string]bool{}
	for _, s := range v {
		if len(s) > 80 || !identifier.MatchString(s) || seen[s] {
			return false
		}
		seen[s] = true
	}
	return true
}
func (m Manifest) Descriptors() []Descriptor {
	v := []Descriptor{m.Source.Archive, m.Distribution.Egg, m.Distribution.Deployment}
	for _, a := range m.Artifacts {
		v = append(v, a.Descriptor)
	}
	return v
}
func (m Manifest) Node() Artifact {
	for _, a := range m.Artifacts {
		if a.Component == "s2node" {
			return a
		}
	}
	return Artifact{}
}
func (m Manifest) Validate() error {
	v, err := ParseVersion(m.Version)
	if err != nil {
		return err
	}
	if m.SchemaVersion != 1 || m.Repository != Repository || m.License != License || m.Tag != "v"+m.Version || m.Channel != "stable" && m.Channel != "prerelease" || len(v.Pre) == 0 != (m.Channel == "stable") {
		return errors.New("release identity mismatch")
	}
	if !revisionPattern.MatchString(m.Source.Revision) || m.Source.CommitURL != "https://github.com/"+Repository+"/commit/"+m.Source.Revision {
		return errors.New("release source identity mismatch")
	}
	names := Names(m.Version)
	expected := map[string]bool{}
	for _, name := range names {
		expected[name] = true
	}
	for _, d := range m.Descriptors() {
		if !expected[d.Name] || d.URL != AssetURL(m.Tag, d.Name) || !digestPattern.MatchString(d.SHA256) || d.Size <= 0 || d.Size > 1<<30 {
			return errors.New("invalid release asset descriptor")
		}
		delete(expected, d.Name)
	}
	if len(expected) > 0 || m.Source.Archive.Name != names["source"] || m.Distribution.Egg.Name != names["egg"] || m.Distribution.Deployment.Name != names["deployment"] || len(m.Artifacts) != 2 {
		return errors.New("release asset set mismatch")
	}
	components := map[string]bool{}
	for _, a := range m.Artifacts {
		if a.Component != "s2node" && a.Component != "s2egg" || components[a.Component] || a.OS != "linux" || a.Arch != "amd64" || a.Name != names[a.Component] || a.Size > MaxBinary {
			return errors.New("invalid compiled artifact")
		}
		components[a.Component] = true
	}
	c := m.Compatibility
	if !c.NodeContainer.Protocol.valid() || !identifiers(c.NodeContainer.Capabilities) || !c.Helper.Protocol.valid() || c.Helper.DiscoveryVersion < 1 || c.Helper.DiscoveryVersion > 65535 || !identifiers(c.Helper.Provides) || c.Helper.Requires == nil || len(c.Helper.Requires) > 128 || c.LauncherMin < 1 || c.LauncherMin > 65535 || !c.State.Read.valid() || !c.State.Read.contains(c.State.Write) {
		return errors.New("invalid release compatibility")
	}
	for op, caps := range c.Helper.Requires {
		if len(op) > 80 || !identifier.MatchString(op) || len(caps) == 0 || !identifiers(caps) {
			return errors.New("invalid helper operation contract")
		}
	}
	return nil
}
func Compatible(old, candidate Manifest, required []string) error {
	if err := old.Validate(); err != nil {
		return err
	}
	if err := candidate.Validate(); err != nil {
		return err
	}
	a, b := old.Compatibility, candidate.Compatibility
	if b.NodeContainer.Protocol.Min > a.NodeContainer.Protocol.Min || b.NodeContainer.Protocol.Max < a.NodeContainer.Protocol.Max || !b.NodeContainer.Protocol.contains(protocol.Version) {
		return errors.New("candidate cannot preserve installed node/container protocol")
	}
	if b.LauncherMin > LauncherVersion || !b.State.Read.contains(a.State.Write) || !a.State.Read.contains(b.State.Write) {
		return errors.New("launcher/state rollback compatibility unavailable")
	}
	for _, cap := range required {
		if !contains(b.NodeContainer.Capabilities, cap) {
			return fmt.Errorf("candidate missing required capability %s", cap)
		}
	}
	return nil
}
func contains(v []string, s string) bool {
	for _, x := range v {
		if x == s {
			return true
		}
	}
	return false
}
func HelperCompatible(candidate Helper, actual Helper, operations []string) error {
	if actual.DiscoveryVersion != 1 || candidate.DiscoveryVersion != 1 || !actual.Protocol.valid() || actual.Protocol.Min > candidate.Protocol.Max || actual.Protocol.Max < candidate.Protocol.Min {
		return errors.New("helper discovery/protocol incompatible")
	}
	for _, op := range operations {
		needed, ok := candidate.Requires[op]
		if !ok || len(needed) == 0 {
			return errors.New("missing candidate helper requirement")
		}
		for _, c := range needed {
			if !contains(actual.Provides, c) {
				return fmt.Errorf("helper lacks %s", c)
			}
		}
	}
	return nil
}

// Strict rejects duplicate keys, missing fields, unknown fields, null containers,
// trailing input, and oversized metadata before semantic identity validation.
func Decode(b []byte) (Manifest, error) {
	var m Manifest
	if len(b) > MaxManifest {
		return m, errors.New("manifest size bound")
	}
	if err := Strict(b, &m); err != nil {
		return m, err
	}
	return m, m.Validate()
}
func Strict(b []byte, v any) error {
	d := json.NewDecoder(bytes.NewReader(b))
	d.UseNumber()
	raw, err := tokens(d)
	if err != nil {
		return err
	}
	if _, err = d.Token(); err != io.EOF {
		return errors.New("trailing JSON")
	}
	if err = required(raw, reflect.TypeOf(v).Elem()); err != nil {
		return err
	}
	d = json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	return d.Decode(v)
}
func tokens(d *json.Decoder) (any, error) {
	t, err := d.Token()
	if err != nil {
		return nil, err
	}
	switch t {
	case json.Delim('{'):
		m := map[string]any{}
		for d.More() {
			key, err := d.Token()
			if err != nil {
				return nil, err
			}
			k, ok := key.(string)
			if !ok {
				return nil, errors.New("JSON key")
			}
			if _, ok = m[k]; ok {
				return nil, errors.New("duplicate JSON key")
			}
			v, err := tokens(d)
			if err != nil {
				return nil, err
			}
			m[k] = v
		}
		_, err = d.Token()
		return m, err
	case json.Delim('['):
		a := []any{}
		for d.More() {
			v, err := tokens(d)
			if err != nil {
				return nil, err
			}
			a = append(a, v)
		}
		_, err = d.Token()
		return a, err
	}
	return t, nil
}
func required(v any, t reflect.Type) error {
	if v == nil {
		return errors.New("null field")
	}
	switch t.Kind() {
	case reflect.Struct:
		m, ok := v.(map[string]any)
		if !ok {
			return errors.New("object required")
		}
		fields := jsonFields(t)
		for name := range m {
			if _, ok := fields[name]; !ok {
				return fmt.Errorf("unknown field %s", name)
			}
		}
		for name, typ := range fields {
			value, ok := m[name]
			if !ok {
				return fmt.Errorf("missing field %s", name)
			}
			if err := required(value, typ); err != nil {
				return err
			}
		}

	case reflect.Slice:
		a, ok := v.([]any)
		if !ok {
			return errors.New("array required")
		}
		for _, x := range a {
			if err := required(x, t.Elem()); err != nil {
				return err
			}
		}
	case reflect.Map:
		m, ok := v.(map[string]any)
		if !ok {
			return errors.New("map required")
		}
		for _, x := range m {
			if err := required(x, t.Elem()); err != nil {
				return err
			}
		}
	}
	return nil
}
func SortedCapabilities(v []string) []string {
	out := append([]string(nil), v...)
	sort.Strings(out)
	return out
}

func jsonFields(t reflect.Type) map[string]reflect.Type {
	out := map[string]reflect.Type{}
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if f.Anonymous {
			for k, v := range jsonFields(f.Type) {
				out[k] = v
			}
			continue
		}
		name := strings.Split(f.Tag.Get("json"), ",")[0]
		if name == "" {
			name = f.Name
		}
		out[name] = f.Type
	}
	return out
}
