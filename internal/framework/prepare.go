// SPDX-License-Identifier: GPL-3.0-or-later
package framework

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"syscall"

	"github.com/s2script/s2script-cs2-egg/internal/artifact"
	"github.com/s2script/s2script-cs2-egg/internal/catalog"
)

const downloadLimit int64 = 256 << 20
const expandedLimit int64 = 512 << 20
const csgo = "game/csgo/"

type BundledArtifact struct {
	Name            string
	Version         string
	SHA256          string
	StagedPath      string
	Filename        string
	UpstreamEnabled bool
	// Manifest retains dependency-interface, publishes, compiledAgainst and
	// protocol evidence verbatim for downstream plugin reconciliation.
	Manifest json.RawMessage
}
type Prepared struct {
	Changes   []artifact.Change
	Bundled   []BundledArtifact
	Selection Selection
	StageDir  string
}

// Cleanup must follow Apply (or any later failure). Never use staged paths after it.
func (p Prepared) Cleanup() error {
	if p.StageDir == "" {
		return nil
	}
	return os.RemoveAll(p.StageDir)
}

type managedFact struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
}
type frameworkState struct {
	SchemaVersion int           `json:"schema_version"`
	Selection     Selection     `json:"selection"`
	Managed       []managedFact `json:"managed"`
}

func sum(b []byte) string { s := sha256.Sum256(b); return hex.EncodeToString(s[:]) }

// openDirectory pins every ancestor without following symlinks. Preparation only
// reads the live tree; all writes target its private disposable staging tree.
func openDirectory(path string) (*os.File, error) {
	abs, e := filepath.Abs(path)
	if e != nil {
		return nil, e
	}
	f, e := os.Open("/")
	if e != nil {
		return nil, e
	}
	for _, part := range strings.Split(strings.TrimPrefix(abs, "/"), "/") {
		if part == "" {
			continue
		}
		fd, e := syscall.Openat(int(f.Fd()), part, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
		f.Close()
		if e != nil {
			return nil, e
		}
		f = os.NewFile(uintptr(fd), part)
	}
	return f, nil
}
func relative(path string) bool {
	return path != "" && path != "." && !filepath.IsAbs(path) && filepath.Clean(path) == path && !strings.ContainsAny(path, "\\:\x00") && path != ".." && !strings.HasPrefix(path, "../")
}
func readPinned(root *os.File, path string, limit int64) ([]byte, error) {
	if !relative(path) {
		return nil, errors.New("unsafe relative framework path")
	}
	fd, e := syscall.Dup(int(root.Fd()))
	if e != nil {
		return nil, e
	}
	syscall.CloseOnExec(fd)
	f := os.NewFile(uintptr(fd), "framework read")
	parts := strings.Split(path, "/")
	for i, part := range parts {
		flags := syscall.O_RDONLY | syscall.O_NOFOLLOW | syscall.O_CLOEXEC | syscall.O_NONBLOCK
		if i < len(parts)-1 {
			flags |= syscall.O_DIRECTORY
		}
		next, e := syscall.Openat(int(f.Fd()), part, flags, 0)
		f.Close()
		if e != nil {
			return nil, e
		}
		f = os.NewFile(uintptr(next), part)
	}
	defer f.Close()
	info, e := f.Stat()
	if e != nil || !info.Mode().IsRegular() {
		return nil, errors.New("framework path is not a regular file")
	}
	b, e := io.ReadAll(io.LimitReader(f, limit+1))
	if e != nil {
		return nil, e
	}
	if int64(len(b)) > limit {
		return nil, errors.New("framework file exceeds limit")
	}
	return b, nil
}
func validateSelection(s Selection) error {
	if s.Channel != "stable" && s.Channel != "prerelease" || !exactVersion.MatchString(s.S2Script.Version) || !regexp.MustCompile(`^2\.0\.0-git[0-9]+$`).MatchString(s.Metamod.Version) {
		return errors.New("unsupported framework version/channel selection")
	}
	if len(s.SupportedAPIMajors) != 2 || s.HostAPIMajor != 3 || s.Protocol2APIMajor != 3 || !s.SupportsAPI("2.x") || !s.SupportsAPI("3.x") || s.SupportsAPI("1.x") || s.MetamodPLAPI != 18 {
		return errors.New("unsupported native/API compatibility selection")
	}
	releases := []Release{s.S2Script, s.Metamod}
	if s.HUD != nil {
		if s.HUD.Version != "1.6.2" {
			return errors.New("unsupported HUD helper compatibility")
		}
		releases = append(releases, *s.HUD)
	}
	for _, r := range releases {
		if r.Integrity != "github-publisher-sha256" || !validDigest(r.SHA256) || !commitPattern.MatchString(r.SourceRevision) || r.Version == "" {
			return errors.New("incomplete verified release selection")
		}
	}
	return nil
}
func loadState(root string) (frameworkState, error) {
	var state frameworkState
	dir, e := openDirectory(root)
	if e != nil {
		return state, e
	}
	defer dir.Close()
	b, e := readPinned(dir, StatePath, 4<<20)
	if e != nil {
		return state, errors.New("retained framework state missing or unreadable")
	}
	if json.Unmarshal(b, &state) != nil || state.SchemaVersion != 1 {
		return state, errors.New("invalid retained framework state")
	}
	if e = validateSelection(state.Selection); e != nil {
		return state, e
	}
	required := map[string]bool{csgo + "addons/s2script/bin/linuxsteamrt64/s2script.so": false, csgo + "addons/s2script/bin/linuxsteamrt64/libs2script_core.so": false, csgo + "addons/s2script/game-packages.json": false, csgo + "addons/metamod/bin/linuxsteamrt64/metamod.2.cs2.so": false, csgo + "addons/metamod/bin/linuxsteamrt64/libserver.so": false}
	seen := map[string]bool{}
	for _, f := range state.Managed {
		if seen[f.Path] || !strings.HasPrefix(f.Path, csgo+"addons/") || !validDigest(f.SHA256) {
			return state, errors.New("invalid retained managed-file facts")
		}
		seen[f.Path] = true
		b, e := readPinned(dir, f.Path, expandedLimit)
		if e != nil || sum(b) != f.SHA256 {
			return state, errors.New("retained framework file is missing or modified")
		}
		if _, ok := required[f.Path]; ok {
			required[f.Path] = true
		}
	}
	for _, present := range required {
		if !present {
			return state, errors.New("retained framework state lacks native generation facts")
		}
	}
	return state, nil
}

// Prepare downloads/checks/extracts the selected generation, returning changes
// without applying them. Bundled plugin placement belongs to the plugin caller.
// The caller must keep the artifact lock held and combine all changes once.
func Prepare(ctx context.Context, root string, s Selection) (prepared Prepared, err error) {
	if err = ctx.Err(); err != nil {
		return prepared, err
	}
	if err = validateSelection(s); err != nil {
		return prepared, err
	}
	live, e := openDirectory(root)
	if e != nil {
		return prepared, e
	}
	defer live.Close()
	if s.HUD != nil {
		manifest, e := readPinned(live, "steamapps/appmanifest_730.acf", 1<<20)
		if e != nil || !regexp.MustCompile(`"appid"\s*"730"`).Match(manifest) {
			return prepared, errors.New("HUD requires the installed app 730 manifest")
		}
		match := regexp.MustCompile(`"buildid"\s*"([0-9]+)"`).FindSubmatch(manifest)
		if len(match) != 2 {
			return prepared, errors.New("HUD requires a valid installed CS2 build ID")
		}
		build, e := strconv.ParseUint(string(match[1]), 10, 64)
		if e != nil || build < 24934554 {
			return prepared, errors.New("HUD requires CS2 build 24934554 or newer")
		}
	}
	gi, e := readPinned(live, csgo+"gameinfo.gi", 4<<20)
	if e != nil {
		return prepared, fmt.Errorf("read gameinfo: %w", e)
	}
	gi, e = PatchGameInfo(gi)
	if e != nil {
		return prepared, e
	}
	stage, e := os.MkdirTemp("", "s2script-prepared-")
	if e != nil {
		return prepared, e
	}
	prepared = Prepared{Selection: s, StageDir: stage}
	defer func() {
		if err != nil {
			_ = prepared.Cleanup()
			prepared = Prepared{}
		}
	}()
	client := s.client
	if client == nil {
		client = publicClient()
	}
	cacheChanges := []artifact.Change{}
	extract := func(name string, r Release) (string, error) {
		dest := filepath.Join(stage, name+".archive")
		cached, e := readPinned(live, cachePath(r), downloadLimit)
		if e == nil {
			if sum(cached) != r.SHA256 {
				return "", errors.New("cached framework archive digest mismatch")
			}
			if e = os.WriteFile(dest, cached, 0600); e != nil {
				return "", e
			}
		} else if errors.Is(e, os.ErrNotExist) {
			c, availability := availabilityClient(client)
			// Fetch treats nil as same-origin-only. The availability wrapper
			// must not turn an absent caller policy into redirect permission.
			if client.CheckRedirect == nil {
				c.CheckRedirect = nil
			}
			if e = artifact.Fetch(ctx, c, r.URL, r.SHA256, dest, downloadLimit); e != nil {
				if ctx.Err() != nil {
					return "", ctx.Err()
				}
				if availability.temporary() {
					return "", &UnavailableError{}
				}
				return "", e
			}
			cacheChanges = append(cacheChanges, artifact.Change{Path: cachePath(r), Source: dest, CreateOnly: true})
		} else {
			return "", e
		}
		dir := filepath.Join(stage, name)
		if e := os.Mkdir(dir, 0700); e != nil {
			return "", e
		}
		if e := artifact.Extract(dest, dir, expandedLimit); e != nil {
			return "", e
		}
		return dir, nil
	}
	s2, e := extract("s2script", s.S2Script)
	if e != nil {
		return prepared, e
	}
	mm, e := extract("metamod", s.Metamod)
	if e != nil {
		return prepared, e
	}
	s2dir, e := openDirectory(s2)
	if e != nil {
		return prepared, e
	}
	defer s2dir.Close()
	if e = validateGamePackage(s2dir); e != nil {
		return prepared, e
	}
	for _, p := range []string{"addons/metamod/s2script.vdf", "addons/s2script/bin/linuxsteamrt64/s2script.so", "addons/s2script/bin/linuxsteamrt64/libs2script_core.so", "addons/s2script/LICENSE"} {
		if b, e := readPinned(s2dir, p, expandedLimit); e != nil || len(b) == 0 {
			return prepared, fmt.Errorf("required S2Script member missing: %s", p)
		}
	}
	version, e := readPinned(s2dir, "addons/s2script/VERSION", 4096)
	if e != nil || normalized(strings.TrimSpace(string(version))) != s.S2Script.Version {
		return prepared, errors.New("S2Script archive version mismatch")
	}
	mmdir, e := openDirectory(mm)
	if e != nil {
		return prepared, e
	}
	defer mmdir.Close()
	for _, p := range []string{"addons/metamod/bin/linuxsteamrt64/metamod.2.cs2.so", "addons/metamod/bin/linuxsteamrt64/libserver.so"} {
		if b, e := readPinned(mmdir, p, expandedLimit); e != nil || len(b) == 0 {
			return prepared, fmt.Errorf("required Metamod CS2 member missing: %s", p)
		}
	}
	state := frameworkState{SchemaVersion: 1, Selection: s}
	prepared.Changes = append(prepared.Changes, cacheChanges...)
	paths := map[string]bool{}
	add := func(path, source string, createOnly, record bool) error {
		if paths[path] {
			return errors.New("duplicate prepared framework path")
		}
		paths[path] = true
		prepared.Changes = append(prepared.Changes, artifact.Change{Path: path, Source: source, CreateOnly: createOnly})
		if record {
			b, e := os.ReadFile(source)
			if e != nil {
				return e
			}
			state.Managed = append(state.Managed, managedFact{path, sum(b)})
		}
		return nil
	}
	walk := func(base string, visit func(string, string) error) error {
		return filepath.WalkDir(base, func(path string, d fs.DirEntry, e error) error {
			if e != nil {
				return e
			}
			if e = ctx.Err(); e != nil {
				return e
			}
			if d.IsDir() {
				return nil
			}
			if !d.Type().IsRegular() {
				return errors.New("nonregular prepared member")
			}
			rel, e := filepath.Rel(base, path)
			if e != nil {
				return e
			}
			return visit(filepath.ToSlash(rel), path)
		})
	}
	known := map[string]bool{}
	for _, b := range catalog.Bundled() {
		known[b.Name] = true
	}
	bundledSeen := map[string]bool{}
	e = walk(s2, func(rel, path string) error {
		if strings.HasSuffix(rel, ".s2sp") {
			b, e := inspectBundled(path, rel, s)
			if e != nil {
				return e
			}
			if !known[b.Name] || bundledSeen[b.Name] {
				return errors.New("unknown or duplicate bundled plugin identity")
			}
			bundledSeen[b.Name] = true
			prepared.Bundled = append(prepared.Bundled, b)
			return nil
		}
		if strings.HasPrefix(rel, "addons/s2script/plugins/") {
			return add(csgo+rel, path, true, false)
		}
		if rel != "addons/metamod/s2script.vdf" && !strings.HasPrefix(rel, "addons/s2script/") {
			return errors.New("unexpected S2Script archive layout")
		}
		create := strings.HasPrefix(rel, "addons/s2script/configs/") || strings.HasPrefix(rel, "addons/s2script/data/") || strings.Contains(rel, "/gamedata/") && strings.Contains(rel, "/custom/")
		return add(csgo+rel, path, create, !create)
	})
	if e != nil {
		return prepared, e
	}
	if !bundledSeen["@s2script/clientprefs"] {
		return prepared, errors.New("release lacks required clientprefs bundled plugin")
	}
	e = walk(mm, func(rel, path string) error {
		if strings.Contains(rel, "/bin/") && rel != "addons/metamod/bin/linuxsteamrt64/metamod.2.cs2.so" && rel != "addons/metamod/bin/linuxsteamrt64/libserver.so" {
			return nil
		}
		if rel != "addons/metamod.vdf" && rel != "addons/metamod_x64.vdf" && !strings.HasPrefix(rel, "addons/metamod/") {
			return errors.New("unexpected Metamod archive layout")
		}
		create := strings.HasSuffix(rel, "metaplugins.ini")
		return add(csgo+rel, path, create, !create)
	})
	if e != nil {
		return prepared, e
	}
	if s.HUD != nil {
		cacheCount := len(cacheChanges)
		hud, e := extract("hud", *s.HUD)
		if e != nil {
			return prepared, e
		}
		prepared.Changes = append(prepared.Changes, cacheChanges[cacheCount:]...)
		hdir, e := openDirectory(hud)
		if e != nil {
			return prepared, e
		}
		for _, p := range []string{"addons/multiaddonmanager/bin/multiaddonmanager.so", "addons/metamod/multiaddonmanager.vdf"} {
			b, e := readPinned(hdir, p, expandedLimit)
			if e != nil || len(b) == 0 {
				hdir.Close()
				return prepared, errors.New("HUD helper archive is incomplete")
			}
		}
		hdir.Close()
		e = walk(hud, func(rel, path string) error {
			if rel == "cfg/multiaddonmanager/multiaddonmanager.cfg" {
				return nil
			}
			if !strings.HasPrefix(rel, "addons/multiaddonmanager/") && rel != "addons/metamod/multiaddonmanager.vdf" {
				return errors.New("unexpected HUD helper archive layout")
			}
			return add(csgo+rel, path, false, true)
		})
		if e != nil {
			return prepared, e
		}
		cfg := csgo + "cfg/multiaddonmanager/multiaddonmanager.cfg"
		b, e := readPinned(live, cfg, 1<<20)
		if errors.Is(e, os.ErrNotExist) {
			b, e = os.ReadFile(filepath.Join(hud, "cfg/multiaddonmanager/multiaddonmanager.cfg"))
		}
		if e != nil {
			return prepared, e
		}
		b, e = MergeHUDConfig(b)
		if e != nil {
			return prepared, e
		}
		path := filepath.Join(stage, "hud-config")
		if e = os.WriteFile(path, b, 0600); e != nil {
			return prepared, e
		}
		if e = add(cfg, path, false, false); e != nil {
			return prepared, e
		}
	}
	gipath := filepath.Join(stage, "gameinfo.gi")
	if e = os.WriteFile(gipath, gi, 0644); e != nil {
		return prepared, e
	}
	if e = add(csgo+"gameinfo.gi", gipath, false, false); e != nil {
		return prepared, e
	}
	sort.Slice(state.Managed, func(i, j int) bool { return state.Managed[i].Path < state.Managed[j].Path })
	stateBytes, e := json.MarshalIndent(state, "", "  ")
	if e != nil {
		return prepared, e
	}
	stateFile := filepath.Join(stage, "framework-state.json")
	if e = os.WriteFile(stateFile, stateBytes, 0600); e != nil {
		return prepared, e
	}
	if e = add(StatePath, stateFile, false, false); e != nil {
		return prepared, e
	}
	sort.Slice(prepared.Bundled, func(i, j int) bool { return prepared.Bundled[i].Name < prepared.Bundled[j].Name })
	return prepared, nil
}
func validateGamePackage(root *os.File) error {
	b, e := readPinned(root, "addons/s2script/game-packages.json", 4<<20)
	if e != nil {
		return errors.New("release game-package manifest missing")
	}
	type ref struct {
		Path   string `json:"path"`
		SHA256 string `json:"sha256"`
	}
	var m struct {
		Schema   int `json:"schemaVersion"`
		Packages []struct {
			ID        string                        `json:"id"`
			Match     struct{ Engine, Game string } `json:"match"`
			Owner     string                        `json:"gamedataOwner"`
			Bootstrap ref                           `json:"bootstrap"`
			Gamedata  ref                           `json:"gamedata"`
			Trusted   ref                           `json:"trustedFunctions"`
		} `json:"packages"`
	}
	if json.Unmarshal(b, &m) != nil || m.Schema != 2 {
		return errors.New("unsupported game-package manifest schema")
	}
	count := 0
	for _, p := range m.Packages {
		if p.ID != "@s2script/cs2" {
			continue
		}
		count++
		if p.Match.Engine != "source2" || p.Match.Game != "csgo" || p.Owner != "cs2" {
			return errors.New("CS2 game-package match is invalid")
		}
		for _, r := range []ref{p.Bootstrap, p.Gamedata, p.Trusted} {
			if !relative(r.Path) || !strings.HasPrefix(r.Path, "game-packages/cs2/") || !validDigest(r.SHA256) {
				return errors.New("invalid game-package file reference")
			}
			data, e := readPinned(root, "addons/s2script/"+r.Path, expandedLimit)
			if e != nil || sum(data) != r.SHA256 {
				return errors.New("game-package referenced file digest mismatch")
			}
		}
	}
	if count != 1 {
		return errors.New("release must contain exactly one matched CS2 game package")
	}
	return nil
}
func inspectBundled(path, rel string, s Selection) (BundledArtifact, error) {
	var b BundledArtifact
	data, e := os.ReadFile(path)
	if e != nil {
		return b, e
	}
	if len(data) > 64<<20 {
		return b, errors.New("bundled archive is oversized")
	}
	z, e := zip.OpenReader(path)
	if e != nil {
		return b, e
	}
	defer z.Close()
	count, code := 0, 0
	for _, f := range z.File {
		if !relative(strings.TrimSuffix(f.Name, "/")) || !f.Mode().IsRegular() && !f.Mode().IsDir() {
			return b, errors.New("unsafe bundled archive member")
		}
		if f.Name == "plugin.js" {
			code++
		}
		if f.Name != "manifest.json" {
			continue
		}
		count++
		if f.UncompressedSize64 > 1<<20 {
			return b, errors.New("bundled manifest is oversized")
		}
		r, e := f.Open()
		if e != nil {
			return b, e
		}
		b.Manifest, e = io.ReadAll(io.LimitReader(r, (1<<20)+1))
		r.Close()
		if e != nil || len(b.Manifest) > 1<<20 {
			return b, errors.New("bundled manifest unreadable")
		}
	}
	if count != 1 || code != 1 {
		return b, errors.New("bundled archive needs one manifest and plugin.js")
	}
	var m struct{ ID, Version, APIVersion string }
	if json.Unmarshal(b.Manifest, &m) != nil || m.ID == "" || m.Version != s.S2Script.Version || !strings.HasPrefix(m.APIVersion, "3.") {
		return b, errors.New("bundled manifest does not match native/API generation")
	}
	filename := regexp.MustCompile(`[^a-zA-Z0-9._-]`).ReplaceAllString(m.ID, "_") + ".s2sp"
	if filepath.Base(rel) != filename {
		return b, errors.New("bundled filename does not match canonical identity")
	}
	parent := filepath.ToSlash(filepath.Dir(rel))
	if parent != "addons/s2script/plugins" && parent != "addons/s2script/plugins/disabled" {
		return b, errors.New("bundled archive has unsupported location")
	}
	b.Name = m.ID
	b.Version = m.Version
	b.SHA256 = sum(data)
	b.StagedPath = path
	b.Filename = filename
	b.UpstreamEnabled = parent == "addons/s2script/plugins"
	return b, nil
}
