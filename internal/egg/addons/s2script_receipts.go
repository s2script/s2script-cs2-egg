// SPDX-License-Identifier: GPL-3.0-or-later
package addons

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"golang.org/x/sys/unix"
)

type s2Host struct {
	Version        string            `json:"version"`
	SourceRevision string            `json:"source_revision"`
	Files          map[string]string `json:"files"`
}

type s2Lock struct {
	SchemaVersion int           `json:"schema_version"`
	Plugins       []s2Installed `json:"plugins"`
	Host          *s2Host       `json:"host,omitempty"`
}

// AcquireS2ScriptRuntime prevents competing egg processes from preparing or
// running the same server concurrently.
func AcquireS2ScriptRuntime(root string) (func(), error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	base, err := os.Open("/")
	if err != nil {
		return nil, err
	}
	defer base.Close()
	dir, err := s2OpenDir(base, strings.TrimPrefix(abs, "/"), false)
	if err != nil {
		return nil, err
	}
	defer dir.Close()
	fd, err := unix.Openat(int(dir.Fd()), ".s2script-runtime", unix.O_RDWR|unix.O_CREAT|unix.O_NOFOLLOW|unix.O_CLOEXEC|unix.O_NONBLOCK, 0600)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), ".s2script-runtime")
	st, err := f.Stat()
	if err != nil || !st.Mode().IsRegular() {
		f.Close()
		return nil, errors.New("unsafe S2Script runtime lock")
	}
	if err = unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB); err != nil {
		f.Close()
		return nil, errors.New("S2Script runtime is busy; stop the server before changing plugins")
	}
	return func() { unix.Flock(fd, unix.LOCK_UN); f.Close() }, nil
}

func (s *s2PluginStore) readJSON(path string, v any) error {
	dir, err := s2OpenDir(s.root, filepath.Dir(path), false)
	if err != nil {
		return err
	}
	defer dir.Close()
	b, err := s2ReadAt(dir, filepath.Base(path), 4<<20)
	if err != nil {
		return err
	}
	return s2JSON(b, v)
}

func (s *s2PluginStore) writeJSON(path string, v any) error {
	dir, err := s2OpenDir(s.root, filepath.Dir(path), true)
	if err != nil {
		return err
	}
	defer dir.Close()
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	if len(b) > 4<<20 {
		return errors.New("plugin metadata exceeds limit")
	}
	return s2WriteAt(dir, filepath.Base(path), append(b, '\n'))
}

func (s *s2PluginStore) retained(actual []s2Installed) ([]s2Installed, error) {
	l := s2Lock{SchemaVersion: 1, Plugins: []s2Installed{}}
	if err := s.readJSON("s2script-plugins.lock.json", &l); err != nil && !errors.Is(err, unix.ENOENT) {
		return nil, err
	}
	if l.SchemaVersion != 1 || l.Plugins == nil {
		return nil, errors.New("invalid plugin lock schema")
	}
	s.host = l.Host
	old := map[string]s2Installed{}
	for _, p := range l.Plugins {
		if !s2PluginName.MatchString(p.Name) || !s2PluginFilename.MatchString(p.Filename) || !s2Hash(p.SHA256) || old[p.Name].Name != "" {
			return nil, errors.New("invalid or duplicate plugin lock facts")
		}
		var m s2Manifest
		if err := s2JSON(p.Manifest, &m); err != nil || m.ID != p.Name || m.Version != p.Version {
			return nil, errors.New("plugin lock manifest does not match identity/version")
		}
		if p.Source != "registry" && p.Source != "release" && p.Source != "local" {
			return nil, errors.New("invalid plugin lock source")
		}
		if p.Managed && (p.Verification != "verified" || p.Source == "local") {
			return nil, errors.New("plugin ownership lacks verified source")
		}
		p.Path = s2Target(p.Filename, p.Enabled)
		p.APIVersion = m.APIVersion
		old[p.Name] = p
	}
	for i, p := range actual {
		if o, ok := old[p.Name]; ok {
			delete(old, p.Name)
			if p.Version == o.Version && p.SHA256 == o.SHA256 && p.Filename == o.Filename {
				o.Path = p.Path
				o.Enabled = p.Enabled
				o.Manifest = p.Manifest
				if o.LastCheckStatus == "missing" {
					o.LastCheckStatus = "rediscovered"
				}
				actual[i] = o
			} else {
				actual[i].LastCheckStatus = "modified"
			}
		}
	}
	for _, p := range old {
		p.LastCheckStatus = "missing"
		actual = append(actual, p)
	}
	sort.Slice(actual, func(i, j int) bool { return actual[i].Name < actual[j].Name })
	return actual, nil
}

func (s *s2PluginStore) save(all []s2Installed) error {
	sort.Slice(all, func(i, j int) bool { return all[i].Name < all[j].Name })
	lock, err := s2ReadAt(s.root, "s2script-plugins.lock.json", 4<<20)
	lockMissing := errors.Is(err, unix.ENOENT)
	if err != nil && !lockMissing {
		return err
	}
	inventory, err := s2ReadAt(s.egg, "installed-list.json", 4<<20)
	inventoryMissing := errors.Is(err, unix.ENOENT)
	if err != nil && !inventoryMissing {
		return err
	}
	if err = s.writeJSON("s2script-plugins.lock.json", s2Lock{SchemaVersion: 1, Plugins: all, Host: s.host}); err != nil {
		return err
	}
	if err = s.inventory(all); err != nil {
		if lockMissing {
			unix.Unlinkat(int(s.root.Fd()), "s2script-plugins.lock.json", 0)
		} else {
			s2WriteAt(s.root, "s2script-plugins.lock.json", lock)
		}
		if inventoryMissing {
			unix.Unlinkat(int(s.egg.Fd()), "installed-list.json", 0)
		} else {
			s2WriteAt(s.egg, "installed-list.json", inventory)
		}
		return err
	}
	return nil
}

// RegisterS2ScriptReleasePlugins records only packages copied from a verified
// release by the native updater. Other files retain their ownership and bytes.
func RegisterS2ScriptReleasePlugins(root string, files map[string]string) error {
	s, err := s2OpenStore(root)
	if err != nil {
		return err
	}
	defer s.close()
	all, err := s.discover()
	if err != nil {
		return err
	}
	all, err = s.retained(all)
	if err != nil {
		return err
	}
	known := map[string]bool{}
	for _, p := range S2ScriptPlugins() {
		known[p.Name] = true
	}
	left := len(files)
	for i, p := range all {
		if hash, ok := files[p.Path]; ok {
			if !known[p.Name] || p.LastCheckStatus == "missing" || hash != p.SHA256 {
				return errors.New("release plugin registration does not match actual artifact")
			}
			all[i].Source = "release"
			all[i].Managed = true
			all[i].Verification = "verified"
			left--
		}
	}
	if left != 0 {
		return errors.New("release plugin registration references missing packages")
	}
	return s.save(all)
}

var s2NativePaths = []string{"game/csgo/addons/metamod/s2script.vdf", "game/csgo/addons/s2script/bin/linuxsteamrt64/s2script.so", "game/csgo/addons/s2script/bin/linuxsteamrt64/libs2script_core.so"}

func (s *s2PluginStore) hostVerified() bool {
	if s.host == nil || !s2ExactVersion(s.host.Version) || !regexp.MustCompile(`^[a-f0-9]{40}$`).MatchString(s.host.SourceRevision) || len(s.host.Files) != len(s2NativePaths) {
		return false
	}
	for _, path := range s2NativePaths {
		hash := s.host.Files[path]
		if !s2Hash(hash) {
			return false
		}
		dir, err := s2OpenDir(s.root, filepath.Dir(path), false)
		if err != nil {
			return false
		}
		data, err := s2ReadAt(dir, filepath.Base(path), 256<<20)
		dir.Close()
		if err != nil || fmt.Sprintf("%x", sha256.Sum256(data)) != hash {
			return false
		}
	}
	return true
}

// RegisterS2ScriptHost is called only after the updater verifies the immutable
// release's source declares the supported host3/API2 compatibility family.
func RegisterS2ScriptHost(root, version, sourceRevision string, native map[string]string) error {
	s, err := s2OpenStore(root)
	if err != nil {
		return err
	}
	defer s.close()
	all, err := s.discover()
	if err != nil {
		return err
	}
	all, err = s.retained(all)
	if err != nil {
		return err
	}
	s.host = &s2Host{Version: strings.TrimPrefix(version, "v"), SourceRevision: sourceRevision, Files: native}
	if !s.hostVerified() {
		return errors.New("verified host source/native evidence is incomplete or changed")
	}
	return s.save(all)
}

func S2ScriptHostVerified(root, version string) bool {
	s, err := s2OpenStore(root)
	if err != nil {
		return false
	}
	defer s.close()
	if _, err = s.retained(nil); err != nil {
		return false
	}
	return s.host != nil && s.host.Version == strings.TrimPrefix(version, "v") && s.hostVerified()
}

func s2Hash(s string) bool {
	b, err := hex.DecodeString(s)
	return err == nil && len(b) == 32 && s == strings.ToLower(s)
}

// All downloads/manifest checks precede live changes. A failed write rolls back
// previous package writes and moves; metadata is committed only afterwards.
func (s *s2PluginStore) s2Apply(before, after []s2Installed, payloads map[string][]byte) error {
	oldByName := map[string]s2Installed{}
	oldByPath := map[string]s2Installed{}
	destinations := map[string]bool{}
	for _, p := range before {
		oldByName[p.Name] = p
		if p.LastCheckStatus != "missing" {
			data, err := s2ReadAt(s.dir(p.Enabled), p.Filename, s2MaxPlugin)
			if err != nil {
				return err
			}
			if fmt.Sprintf("%x", sha256.Sum256(data)) != p.SHA256 {
				return fmt.Errorf("plugin changed during inspection: %s", p.Name)
			}
			oldByPath[p.Path] = p
		}
	}
	for _, p := range after {
		if p.LastCheckStatus == "missing" {
			continue
		}
		if destinations[p.Path] {
			return errors.New("plugin destination collision")
		}
		destinations[p.Path] = true
		if occupant, ok := oldByPath[p.Path]; ok && occupant.Name != p.Name {
			return errors.New("plugin destination belongs to another package")
		}
		if err := s2CheckFile(s.dir(p.Enabled), p.Filename); err != nil {
			return err
		}
		if _, known := oldByPath[p.Path]; !known {
			var st unix.Stat_t
			err := unix.Fstatat(int(s.dir(p.Enabled).Fd()), p.Filename, &st, unix.AT_SYMLINK_NOFOLLOW)
			if !errors.Is(err, unix.ENOENT) {
				return errors.New("plugin destination is occupied")
			}
		}
	}
	for _, path := range []string{"s2script-plugins.lock.json", "egg/installed-list.json"} {
		dir, err := s2OpenDir(s.root, filepath.Dir(path), false)
		if err != nil {
			return err
		}
		err = s2CheckFile(dir, filepath.Base(path))
		dir.Close()
		if err != nil {
			return err
		}
	}
	type undo struct {
		p      s2Installed
		data   []byte
		exists bool
	}
	journal := []undo{}
	rollback := func() {
		for i := len(journal) - 1; i >= 0; i-- {
			u := journal[i]
			if u.exists {
				s2WriteAt(s.dir(u.p.Enabled), u.p.Filename, u.data)
			} else {
				unix.Unlinkat(int(s.dir(u.p.Enabled).Fd()), u.p.Filename, 0)
			}
		}
	}
	for _, p := range after {
		if p.LastCheckStatus == "missing" {
			continue
		}
		old, exists := oldByName[p.Name]
		data, downloaded := payloads[p.Name]
		if !downloaded && exists && old.Path == p.Path {
			continue
		}
		if !downloaded {
			var err error
			data, err = s2ReadAt(s.dir(old.Enabled), old.Filename, s2MaxPlugin)
			if err != nil {
				rollback()
				return err
			}
		}
		previous, err := s2ReadAt(s.dir(p.Enabled), p.Filename, s2MaxPlugin)
		present := err == nil
		if err != nil && !errors.Is(err, unix.ENOENT) {
			rollback()
			return err
		}
		journal = append(journal, undo{p, previous, present})
		if err = s2WriteAt(s.dir(p.Enabled), p.Filename, data); err != nil {
			rollback()
			return err
		}
		if exists && old.LastCheckStatus != "missing" && old.Path != p.Path {
			oldData, err := s2ReadAt(s.dir(old.Enabled), old.Filename, s2MaxPlugin)
			if err != nil {
				rollback()
				return err
			}
			journal = append(journal, undo{old, oldData, true})
			if err = unix.Unlinkat(int(s.dir(old.Enabled).Fd()), old.Filename, 0); err != nil {
				rollback()
				return err
			}
		}
	}
	remaining := map[string]bool{}
	for _, p := range after {
		remaining[p.Name] = true
	}
	for _, p := range before {
		if remaining[p.Name] || p.LastCheckStatus == "missing" {
			continue
		}
		data, err := s2ReadAt(s.dir(p.Enabled), p.Filename, s2MaxPlugin)
		if err != nil {
			rollback()
			return err
		}
		journal = append(journal, undo{p, data, true})
		if err = unix.Unlinkat(int(s.dir(p.Enabled).Fd()), p.Filename, 0); err != nil {
			rollback()
			return err
		}
	}
	if err := s.save(after); err != nil {
		rollback()
		return err
	}
	return nil
}

// Artifact versions are exact SemVer, including prerelease/build suffixes.
var s2VersionRE = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(?:-([0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*))?(?:\+[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?$`)

func s2ExactVersion(v string) bool {
	m := s2VersionRE.FindStringSubmatch(v)
	if m == nil || len(v) > 128 {
		return false
	}
	for _, part := range strings.Split(m[4], ".") {
		if len(part) > 1 && part[0] == '0' && strings.Trim(part, "0123456789") == "" {
			return false
		}
	}
	return true
}
