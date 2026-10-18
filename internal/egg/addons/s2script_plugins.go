// SPDX-License-Identifier: GPL-3.0-or-later
package addons

import (
	"archive/zip"
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"golang.org/x/sys/unix"
)

// S2ScriptPlugin is a bundled release plugin and its optional panel override.
type S2ScriptPlugin struct {
	Name, Variable string
	DefaultEnabled bool
}

// S2ScriptPlugins returns an independent copy of the reviewed bundled catalog.
// Defaults describe fresh release placement; default never resets manual moves.
func S2ScriptPlugins() []S2ScriptPlugin {
	return []S2ScriptPlugin{
		{"@s2script/basecommands", "S2SCRIPT_PLUGIN_BASECOMMANDS", true},
		{"@s2script/basechat", "S2SCRIPT_PLUGIN_BASECHAT", true},
		{"@s2script/playercommands", "S2SCRIPT_PLUGIN_PLAYERCOMMANDS", true},
		{"@s2script/antiflood", "S2SCRIPT_PLUGIN_ANTIFLOOD", true},
		{"@s2script/adminhelp", "S2SCRIPT_PLUGIN_ADMINHELP", true},
		{"@s2script/basecomm", "S2SCRIPT_PLUGIN_BASECOMM", true},
		{"@s2script/basebans", "S2SCRIPT_PLUGIN_BASEBANS", true},
		{"@s2script/reservedslots", "S2SCRIPT_PLUGIN_RESERVEDSLOTS", true},
		{"@s2script/basetriggers", "S2SCRIPT_PLUGIN_BASETRIGGERS", true},
		{"@s2script/funcommands", "S2SCRIPT_PLUGIN_FUNCOMMANDS", true},
		{"@s2script/clientprefs", "S2SCRIPT_PLUGIN_CLIENTPREFS", true},
		{"@s2script/adminmenu", "S2SCRIPT_PLUGIN_ADMINMENU", true},
		{"@s2script/basevotes", "S2SCRIPT_PLUGIN_BASEVOTES", true},
		{"@s2script/zones", "S2SCRIPT_PLUGIN_ZONES", true},
		{"@s2script/nominations", "S2SCRIPT_PLUGIN_NOMINATIONS", false},
		{"@s2script/rockthevote", "S2SCRIPT_PLUGIN_ROCKTHEVOTE", false},
		{"@s2script/nextmap", "S2SCRIPT_PLUGIN_NEXTMAP", false},
		{"@s2script/funvotes", "S2SCRIPT_PLUGIN_FUNVOTES", false},
	}
}

const s2PluginDir = "game/csgo/addons/s2script/plugins"
const s2MaxPlugin = 64 << 20

var s2PluginName = regexp.MustCompile(`^(?:@[a-zA-Z0-9._-]+/)?[a-zA-Z0-9][a-zA-Z0-9._-]*$`)
var s2PluginFilename = regexp.MustCompile(`^[a-zA-Z0-9_][a-zA-Z0-9._-]*\.s2sp$`)

type s2Manifest struct {
	ID                   string            `json:"id"`
	Version              string            `json:"version"`
	APIVersion           string            `json:"apiVersion"`
	Dependencies         map[string]string `json:"pluginDependencies,omitempty"`
	OptionalDependencies map[string]string `json:"optionalPluginDependencies,omitempty"`
	Publishes            map[string]struct {
		Version     string `json:"version"`
		TypesSHA256 string `json:"typesSha256,omitempty"`
	} `json:"publishes,omitempty"`
	CompiledAgainst map[string]string `json:"compiledAgainst,omitempty"`
}

type s2Installed struct {
	Name             string          `json:"name"`
	Version          string          `json:"version"`
	APIVersion       string          `json:"api_version"`
	Enabled          bool            `json:"enabled"`
	Path             string          `json:"path"`
	SHA256           string          `json:"sha256"`
	Filename         string          `json:"filename"`
	Source           string          `json:"source,omitempty"`
	Managed          bool            `json:"managed,omitempty"`
	Verification     string          `json:"verification,omitempty"`
	LastCheckStatus  string          `json:"last_check_status,omitempty"`
	Parents          []string        `json:"parents,omitempty"`
	SourceURL        string          `json:"source_url,omitempty"`
	ReviewState      string          `json:"review_state,omitempty"`
	AvailableVersion string          `json:"available_version,omitempty"`
	LastCheckTime    string          `json:"last_check_time,omitempty"`
	Manifest         json.RawMessage `json:"manifest"`
}

type s2PluginStore struct {
	root, active, disabled, egg, guard *os.File
	host                               *s2Host
}

// Every component is opened relative to a pinned directory descriptor, with
// O_NOFOLLOW. Filesystem races cannot redirect operations outside this tree.
func s2OpenDir(parent *os.File, path string, create bool) (*os.File, error) {
	fd, err := unix.Openat(int(parent.Fd()), ".", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	d := os.NewFile(uintptr(fd), path)
	for _, part := range strings.Split(filepath.ToSlash(path), "/") {
		if part == "" || part == "." {
			continue
		}
		if part == ".." {
			d.Close()
			return nil, errors.New("unsafe plugin directory")
		}
		fd, err = unix.Openat(int(d.Fd()), part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if errors.Is(err, unix.ENOENT) && create {
			err = unix.Mkdirat(int(d.Fd()), part, 0755)
			if err == nil || errors.Is(err, unix.EEXIST) {
				fd, err = unix.Openat(int(d.Fd()), part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
			}
		}
		d.Close()
		if err != nil {
			return nil, err
		}
		d = os.NewFile(uintptr(fd), part)
	}
	return d, nil
}

func s2OpenStore(path string) (*s2PluginStore, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	base, err := os.Open("/")
	if err != nil {
		return nil, err
	}
	defer base.Close()
	root, err := s2OpenDir(base, strings.TrimPrefix(abs, "/"), false)
	if err != nil {
		return nil, err
	}
	s := &s2PluginStore{root: root}
	if s.active, err = s2OpenDir(root, s2PluginDir, true); err == nil {
		s.disabled, err = s2OpenDir(s.active, "disabled", true)
	}
	if err == nil {
		s.egg, err = s2OpenDir(root, "egg", true)
	}
	if err == nil {
		var fd int
		fd, err = unix.Openat(int(s.egg.Fd()), "plugins.guard", unix.O_RDWR|unix.O_CREAT|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0600)
		if err == nil {
			s.guard = os.NewFile(uintptr(fd), "plugins.guard")
			var st os.FileInfo
			st, err = s.guard.Stat()
			if err == nil && !st.Mode().IsRegular() {
				err = errors.New("unsafe plugin guard")
			}
			if err == nil {
				err = unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB)
			}
		}
	}
	if err != nil {
		s.close()
		return nil, err
	}
	return s, nil
}

func (s *s2PluginStore) close() {
	for _, f := range []*os.File{s.guard, s.root, s.active, s.disabled, s.egg} {
		if f != nil {
			f.Close()
		}
	}
}
func (s *s2PluginStore) dir(enabled bool) *os.File {
	if enabled {
		return s.active
	}
	return s.disabled
}
func s2Target(filename string, enabled bool) string {
	if enabled {
		return s2PluginDir + "/" + filename
	}
	return s2PluginDir + "/disabled/" + filename
}

func s2ReadAt(dir *os.File, name string, limit int64) ([]byte, error) {
	fd, err := unix.Openat(int(dir.Fd()), name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), name)
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !st.Mode().IsRegular() || st.Size() > limit {
		return nil, fmt.Errorf("unsafe or oversized plugin file %s", name)
	}
	b, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err == nil && int64(len(b)) > limit {
		err = errors.New("plugin file exceeds limit")
	}
	return b, err
}

func s2CheckFile(dir *os.File, name string) error {
	var st unix.Stat_t
	err := unix.Fstatat(int(dir.Fd()), name, &st, unix.AT_SYMLINK_NOFOLLOW)
	if errors.Is(err, unix.ENOENT) {
		return nil
	}
	if err != nil {
		return err
	}
	if st.Mode&unix.S_IFMT != unix.S_IFREG {
		return fmt.Errorf("unsafe plugin metadata path %s", name)
	}
	return nil
}

func s2WriteAt(dir *os.File, name string, data []byte) error {
	if err := s2CheckFile(dir, name); err != nil {
		return err
	}
	var nonce [12]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return err
	}
	tmp := fmt.Sprintf(".s2plugins-%x", nonce)
	fd, err := unix.Openat(int(dir.Fd()), tmp, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0644)
	if err != nil {
		return err
	}
	defer unix.Unlinkat(int(dir.Fd()), tmp, 0)
	f := os.NewFile(uintptr(fd), tmp)
	_, err = f.Write(data)
	if err == nil {
		err = f.Sync()
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err = unix.Renameat(int(dir.Fd()), tmp, int(dir.Fd()), name); err != nil {
		return err
	}
	return dir.Sync()
}

func s2JSON(data []byte, v any) error {
	// JSON duplicate keys, including nested manifest evidence, are ambiguous.
	d := json.NewDecoder(bytes.NewReader(data))
	var walk func() error
	walk = func() error {
		tok, err := d.Token()
		if err != nil {
			return err
		}
		if delim, ok := tok.(json.Delim); ok {
			switch delim {
			case '{':
				seen := map[string]bool{}
				for d.More() {
					key, err := d.Token()
					if err != nil {
						return err
					}
					name := key.(string)
					if seen[name] {
						return errors.New("duplicate JSON key")
					}
					seen[name] = true
					if err = walk(); err != nil {
						return err
					}
				}
			case '[':
				for d.More() {
					if err = walk(); err != nil {
						return err
					}
				}
			default:
				return errors.New("invalid JSON delimiter")
			}
			_, err = d.Token()
			return err
		}
		return nil
	}
	if err := walk(); err != nil {
		return err
	}
	if _, err := d.Token(); err != io.EOF {
		return errors.New("trailing JSON data")
	}
	return json.Unmarshal(data, v)
}

func s2Inspect(data []byte) (s2Manifest, json.RawMessage, error) {
	var m s2Manifest
	z, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return m, nil, err
	}
	seen := map[string]bool{}
	var raw []byte
	var expanded uint64
	pluginJS := false
	for _, f := range z.File {
		name := f.Name
		if name == "" || filepath.IsAbs(name) || strings.ContainsAny(name, "\\\x00:") || strings.Contains("/"+name+"/", "/../") || seen[name] || f.Mode()&os.ModeSymlink != 0 || (!f.Mode().IsRegular() && !f.FileInfo().IsDir()) {
			return m, nil, errors.New("unsafe or duplicate plugin ZIP member")
		}
		seen[name] = true
		if f.UncompressedSize64 > 128<<20-expanded {
			return m, nil, errors.New("expanded plugin ZIP exceeds limit")
		}
		expanded += f.UncompressedSize64
		if name == "plugin.js" && f.Mode().IsRegular() {
			pluginJS = true
		}
		if name != "manifest.json" {
			continue
		}
		if f.UncompressedSize64 > 1<<20 {
			return m, nil, errors.New("plugin manifest exceeds limit")
		}
		r, err := f.Open()
		if err != nil {
			return m, nil, err
		}
		raw, err = io.ReadAll(io.LimitReader(r, (1<<20)+1))
		r.Close()
		if err != nil {
			return m, nil, err
		}
	}
	if len(raw) == 0 || len(raw) > 1<<20 || !pluginJS {
		return m, nil, errors.New("plugin ZIP needs manifest.json and regular plugin.js")
	}
	if err = s2JSON(raw, &m); err != nil {
		return m, nil, err
	}
	var fields map[string]json.RawMessage
	if err = json.Unmarshal(raw, &fields); err != nil {
		return m, nil, err
	}
	for _, key := range []string{"id", "version", "apiVersion"} {
		if _, ok := fields[key]; !ok {
			return m, nil, fmt.Errorf("plugin manifest needs canonical %s", key)
		}
	}
	for key := range fields {
		for _, canonical := range []string{"id", "version", "apiVersion", "pluginDependencies", "optionalPluginDependencies", "publishes", "compiledAgainst", "interfaceProtocol", "interfaceContracts"} {
			if strings.EqualFold(key, canonical) && key != canonical {
				return m, nil, errors.New("noncanonical plugin manifest field")
			}
		}
	}
	if !s2PluginName.MatchString(m.ID) || len(m.ID) > 200 || !s2ExactVersion(m.Version) || m.APIVersion == "" {
		return m, nil, errors.New("invalid plugin manifest identity/version/apiVersion")
	}
	return m, raw, nil
}

func (s *s2PluginStore) discover() ([]s2Installed, error) {
	all := []s2Installed{}
	seen := map[string]bool{}
	for _, enabled := range []bool{true, false} {
		// Open an independent directory description so repeated scans start at 0.
		d, err := s2OpenDir(s.dir(enabled), ".", false)
		if err != nil {
			return nil, err
		}
		if _, err = d.Seek(0, 0); err != nil {
			d.Close()
			return nil, err
		}
		entries, err := d.ReadDir(-1)
		d.Close()
		if err != nil {
			return nil, err
		}
		for _, entry := range entries {
			if !strings.HasSuffix(entry.Name(), ".s2sp") {
				continue
			}
			if !s2PluginFilename.MatchString(entry.Name()) || len(entry.Name()) > 240 {
				return nil, errors.New("unsafe plugin filename")
			}
			data, err := s2ReadAt(s.dir(enabled), entry.Name(), s2MaxPlugin)
			if err != nil {
				return nil, err
			}
			m, raw, err := s2Inspect(data)
			if err != nil {
				return nil, fmt.Errorf("inspect %s: %w", entry.Name(), err)
			}
			if seen[m.ID] {
				return nil, fmt.Errorf("duplicate plugin identity %s", m.ID)
			}
			seen[m.ID] = true
			all = append(all, s2Installed{Name: m.ID, Version: m.Version, APIVersion: m.APIVersion, Enabled: enabled, Path: s2Target(entry.Name(), enabled), SHA256: fmt.Sprintf("%x", sha256.Sum256(data)), Filename: entry.Name(), Source: "local", Verification: "unverified", Manifest: raw})
		}
	}
	sort.Slice(all, func(i, j int) bool { return all[i].Name < all[j].Name })
	return all, nil
}

func s2Panel(getenv func(string) string) (map[string]bool, error) {
	if getenv == nil {
		getenv = os.Getenv
	}
	overrides := map[string]bool{}
	for _, p := range S2ScriptPlugins() {
		switch v := getenv(p.Variable); v {
		case "", "default":
		case "0":
			overrides[p.Name] = false
		case "1":
			overrides[p.Name] = true
		default:
			return nil, fmt.Errorf("%s must be default, 0 or 1", p.Variable)
		}
	}
	return overrides, nil
}

func (s *s2PluginStore) inventory(all []s2Installed) error {
	raw, err := json.MarshalIndent(struct {
		SchemaVersion int           `json:"schema_version"`
		Plugins       []s2Installed `json:"plugins"`
	}{1, all}, "", "  ")
	if err != nil {
		return err
	}
	return s2WriteAt(s.egg, "installed-list.json", append(raw, '\n'))
}

// ConfigureS2ScriptPlugins applies explicit panel overrides to actual manifest
// identities. Default inherits the current active/disabled release placement.
// Unknown local packages are included in inventory and retain their placement.
func ConfigureS2ScriptPlugins(root string, getenv func(string) string) error {
	overrides, err := s2Panel(getenv)
	if err != nil {
		return err
	}
	s, err := s2OpenStore(root)
	if err != nil {
		return err
	}
	defer s.close()
	native, err := s.nativeManifest()
	if err != nil {
		return err
	}
	if native == nil {
		native = &s2NativeManifest{
			raw:          map[string]json.RawMessage{"dependencies": json.RawMessage(`{}`), "autoUpdate": json.RawMessage(`true`)},
			dependencies: map[string]string{},
			disabled:     map[string]bool{},
		}
		if err := s.writeJSON(s2NativeManifestPath, native.raw); err != nil {
			return err
		}
	}
	return s.configureNative(native, overrides)
}
