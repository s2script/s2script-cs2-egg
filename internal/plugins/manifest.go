// SPDX-License-Identifier: GPL-3.0-or-later
package plugins

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

const maxPluginBytes = 64 << 20
const maxManifestBytes = 1 << 20

// Manifest preserves published interface dependencies independently of package ID.
type Manifest struct {
	ID                         string               `json:"id"`
	Version                    string               `json:"version"`
	APIVersion                 string               `json:"apiVersion"`
	PluginDependencies         map[string]string    `json:"pluginDependencies,omitempty"`
	OptionalPluginDependencies map[string]string    `json:"optionalPluginDependencies,omitempty"`
	Publishes                  map[string]Published `json:"publishes,omitempty"`
	CompiledAgainst            json.RawMessage      `json:"compiledAgainst,omitempty"`
	Raw                        json.RawMessage      `json:"-"`
}
type Published struct {
	Version     string `json:"version"`
	TypesSHA256 string `json:"typesSha256,omitempty"`
}

// openSafe pins every ancestor without following symlinks, also rejecting
// special files. This read boundary complements artifact's safe write boundary.
func openSafe(path string, directory bool) (*os.File, error) {
	abs, e := filepath.Abs(path)
	if e != nil {
		return nil, e
	}
	parts := strings.Split(strings.TrimPrefix(abs, "/"), "/")
	f, e := os.Open("/")
	if e != nil {
		return nil, e
	}
	for i, p := range parts {
		if p == "" {
			continue
		}
		flags := syscall.O_RDONLY | syscall.O_NOFOLLOW | syscall.O_CLOEXEC | syscall.O_NONBLOCK
		if i < len(parts)-1 || directory {
			flags |= syscall.O_DIRECTORY
		}
		fd, e := syscall.Openat(int(f.Fd()), p, flags, 0)
		f.Close()
		if e != nil {
			return nil, e
		}
		f = os.NewFile(uintptr(fd), p)
	}
	info, e := f.Stat()
	if e != nil || (!directory && !info.Mode().IsRegular()) || (directory && !info.IsDir()) {
		f.Close()
		return nil, errors.New("plugin path must be a regular file or expected directory")
	}
	return f, nil
}
func readSafe(path string, limit int64) ([]byte, error) {
	f, e := openSafe(path, false)
	if e != nil {
		return nil, e
	}
	defer f.Close()
	b, e := io.ReadAll(io.LimitReader(f, limit+1))
	if e != nil {
		return nil, e
	}
	if int64(len(b)) > limit {
		return nil, errors.New("plugin metadata or archive exceeds size limit")
	}
	return b, nil
}
func safeMember(s string) bool {
	return s != "" && s != "." && s != ".." && !filepath.IsAbs(s) && filepath.Clean(s) == s && !strings.HasPrefix(s, "../") && !strings.ContainsAny(s, "\\:\x00")
}
func ParseManifest(raw []byte) (Manifest, error) {
	var m Manifest
	if len(raw) > maxManifestBytes || decodeDocument(raw, &m, true, false) != nil || !validName(m.ID) || !ExactVersion(m.Version) || !validAPIVersion(m.APIVersion) {
		return m, errors.New("invalid plugin manifest identity, version or API version")
	}
	var fields map[string]json.RawMessage
	if e := json.Unmarshal(raw, &fields); e != nil {
		return m, e
	}
	for field := range fields {
		for _, canonical := range []string{"id", "version", "apiVersion", "pluginDependencies", "optionalPluginDependencies", "publishes", "compiledAgainst", "interfaceProtocol", "interfaceContracts"} {
			if field != canonical && strings.EqualFold(field, canonical) {
				return m, errors.New("case-aliased plugin manifest field")
			}
		}
	}
	for _, deps := range []map[string]string{m.PluginDependencies, m.OptionalPluginDependencies} {
		for name, v := range deps {
			if !validName(name) {
				return m, errors.New("invalid manifest dependency identity")
			}
			if _, e := NormalizeVersion(v); e != nil {
				return m, e
			}
		}
	}
	for name, p := range m.Publishes {
		if !validName(name) || !ExactVersion(p.Version) {
			return m, errors.New("invalid published interface")
		}
	}
	m.Raw = append(json.RawMessage(nil), raw...)
	return m, nil
}
func manifestBytes(data []byte) (Manifest, error) {
	var m Manifest
	z, e := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if e != nil {
		return m, e
	}
	if len(z.File) > 4096 {
		return m, errors.New("too many plugin archive members")
	}
	seen := map[string]bool{}
	regular := map[string]bool{}
	code := false
	var raw []byte
	var expanded uint64
	for _, f := range z.File {
		path := strings.TrimSuffix(f.Name, "/")
		if !safeMember(path) || seen[path] || (!f.Mode().IsRegular() && !f.Mode().IsDir()) {
			return m, errors.New("unsafe or duplicate plugin archive member")
		}
		for parent := filepath.Dir(path); parent != "."; parent = filepath.Dir(parent) {
			if regular[parent] {
				return m, errors.New("plugin file/directory member conflict")
			}
		}
		if f.Mode().IsRegular() {
			for previous := range seen {
				if strings.HasPrefix(previous, path+"/") {
					return m, errors.New("plugin file/directory member conflict")
				}
			}
			regular[path] = true
		}
		seen[path] = true
		if f.UncompressedSize64 > 256<<20 || expanded > 256<<20-f.UncompressedSize64 {
			return m, errors.New("plugin expanded size exceeds limit")
		}
		expanded += f.UncompressedSize64
		if f.Name == "plugin.js" && f.Mode().IsRegular() {
			code = true
		}
		if f.Name != "manifest.json" {
			continue
		}
		if !f.Mode().IsRegular() || f.UncompressedSize64 > maxManifestBytes {
			return m, errors.New("unsafe or oversized plugin manifest")
		}
		r, e := f.Open()
		if e != nil {
			return m, e
		}
		raw, e = io.ReadAll(io.LimitReader(r, maxManifestBytes+1))
		ce := r.Close()
		if e != nil {
			return m, e
		}
		if ce != nil {
			return m, ce
		}
	}
	if raw == nil || !code {
		return m, errors.New("plugin archive needs one manifest.json and plugin.js")
	}
	return ParseManifest(raw)
}

// ReadManifest inspects only bounded ZIP metadata and JSON, never plugin code.
func ReadManifest(path string) (Manifest, error) {
	b, e := readSafe(path, maxPluginBytes)
	if e != nil {
		return Manifest{}, e
	}
	return manifestBytes(b)
}
