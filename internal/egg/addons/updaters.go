// SPDX-License-Identifier: GPL-3.0-or-later

package addons

import (
	"context"
	"errors"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/K4ryuu/CS2-Egg-Go/internal/logx"
	"github.com/K4ryuu/CS2-Egg-Go/internal/proto"
)

// Env is everything an updater needs.
type Env struct {
	Root       string // /home/container
	Temp       string // scratch dir, removed by the caller
	Versions   Versions
	Log        *logx.Console
	HTTP       *http.Client
	Prerelease bool
	// Node answers addon queries from the node's cache (release info and
	// files); nil means GitHub directly.
	Node func(q *proto.AddonQuery) *proto.AddonRelease
}

// Selection says which addons the panel asked for.
type Selection struct {
	Metamod bool
}

func (e *Env) addonsDir() string { return filepath.Join(e.Root, "game", "csgo", "addons") }
func (e *Env) gameinfo() Gameinfo {
	return Gameinfo{Path: filepath.Join(e.Root, "game", "csgo", "gameinfo.gi")}
}

// release fetches and logs the way every updater does.
func (e *Env) release(ctx context.Context, label, repo string, prerelease bool) (*Release, error) {
	if e.Node != nil {
		switch r := e.Node(&proto.AddonQuery{Repo: repo, Prerelease: prerelease}); {
		case r == nil:
		case r.Err != "" && r.Pinned:
			// the node holds this addon at a version it could not resolve;
			// installing the newest instead is exactly what a pin forbids
			e.Log.Logf(logx.Error, "%s is pinned on the node but the version could not be found (%s) - keeping the installed version", label, r.Err)
			return nil, errors.New("pinned version unavailable")
		case r.Err != "":
			e.Log.Logf(logx.Debug, "Node cache has no release for %s (%s), asking GitHub", repo, r.Err)
		case r.Tag != "":
			if r.Pinned {
				e.Log.Logf(logx.Info, "%s is pinned to %s by the node", label, r.Tag)
			} else {
				e.Log.Logf(logx.Debug, "Release info for %s from the node cache (%s)", repo, r.Tag)
			}
			rel := &Release{Tag: r.Tag, Prerelease: r.Prerelease, Pinned: r.Pinned}
			for _, a := range r.Assets {
				rel.Assets = append(rel.Assets, Asset{Name: a.Name, URL: a.URL, Digest: a.Digest})
			}
			return rel, nil
		}
	}
	if prerelease {
		e.Log.Logf(logx.Debug, "Checking releases (prereleases enabled) for %s", repo)
	} else {
		e.Log.Logf(logx.Debug, "Checking latest stable release for %s", repo)
	}
	rel, err := FetchRelease(ctx, e.HTTP, repo, prerelease)
	if err != nil {
		switch {
		case errors.Is(err, ErrRateLimited):
			e.Log.Logf(logx.Warning, "GitHub API rate limited - skipping update check for %s this boot", repo)
		default:
			e.Log.Logf(logx.Warning, "%v - skipping update check", err)
		}
		e.Log.Logf(logx.Error, "Failed to get release info for %s (%s)", label, repo)
		return nil, err
	}
	return rel, nil
}

// decide logs the verdict and says whether to install. A pinned release is
// installed whenever it differs from what is here, downgrade included: the
// node was told to hold this exact version.
func (e *Env) decide(label, key, latest string, pinned bool) bool {
	if latest == "" {
		e.Log.Logf(logx.Error, "Failed to get version for %s", label)
		return false
	}
	current := e.Versions.Get(key)
	if pinned {
		switch {
		case current == latest:
			e.Log.Logf(logx.Success, "%s is at its pinned version (%s)", label, latest)
			return false
		case current == "":
			e.Log.Logf(logx.Info, "Installing %s at its pinned version %s", label, latest)
		case NeedsUpdate(current, latest) == Downgrade:
			e.Log.Logf(logx.Warning, "Rolling %s back to the pinned version %s (installed: %s)", label, latest, current)
		default:
			e.Log.Logf(logx.Info, "Moving %s to the pinned version %s (installed: %s)", label, latest, current)
		}
		return true
	}
	switch NeedsUpdate(current, latest) {
	case UpToDate:
		e.Log.Logf(logx.Success, "%s is up-to-date (%s)", label, current)
		return false
	case Downgrade:
		e.Log.Logf(logx.Info, "%s is at a newer version (%s) than latest (%s). Skipping downgrade.", label, current, latest)
		return false
	}
	if current == "" {
		current = "none"
	}
	e.Log.Logf(logx.Info, "Update available for %s: %s (current: %s)", label, latest, current)
	return true
}

func (e *Env) download(ctx context.Context, url, dest string) error {
	if e.Node != nil {
		if r := e.Node(&proto.AddonQuery{URL: url}); r != nil && r.Err == "" && len(r.Assets) == 1 && r.Assets[0].Path != "" {
			e.Log.Logf(logx.Debug, "Taking %s from the node cache", r.Assets[0].Name)
			url = "file://" + filepath.Join(e.Root, "egg", r.Assets[0].Path)
		} else if r != nil && r.Err != "" {
			e.Log.Logf(logx.Debug, "Node cache miss (%s), downloading", r.Err)
		}
	}
	e.Log.Logf(logx.Debug, "Downloading from: %s", url)
	return Download(ctx, e.HTTP, url, dest, func(_ string, err error) {
		e.Log.Logf(logx.Warning, "Download failed, trying next mirror... (%v)", err)
	})
}

func (e *Env) fetchAndExtract(ctx context.Context, url, file, dir string) error {
	if err := e.download(ctx, url, file); err != nil {
		e.Log.Log(logx.Error, "All download sources failed")
		return err
	}
	var err error
	if strings.HasSuffix(file, ".zip") {
		err = ExtractZip(file, dir)
	} else {
		err = ExtractTarGz(file, dir)
	}
	if err != nil {
		e.Log.Logf(logx.Error, "Failed to extract %s: %v", filepath.Base(file), err)
	}
	return err
}

// UpdateMetamod: CS2 builds are prereleases; the version lives in the asset
// name (mmsource-2.0.0-git1391-linux.tar.gz), not the tag.
func (e *Env) UpdateMetamod(ctx context.Context) error {
	if _, err := os.Stat(filepath.Join(e.addonsDir(), "metamod")); err != nil {
		e.Log.Log(logx.Info, "Installing Metamod...")
	}
	rel, err := e.release(ctx, "Metamod", RepoMetamod, true)
	if err != nil {
		return err
	}
	asset, ok := rel.Asset(regexp.MustCompile(`linux\.tar\.gz$`))
	if !ok {
		e.Log.Log(logx.Error, "No Linux asset found in Metamod release")
		return errors.New("no metamod linux asset")
	}
	latest := regexp.MustCompile(`git[0-9]+`).FindString(asset.Name)
	if !e.decide("Metamod", "Metamod", latest, rel.Pinned) {
		return nil
	}
	tmp := filepath.Join(e.Temp, "metamod")
	if err := e.fetchAndExtract(ctx, asset.URL, filepath.Join(e.Temp, "metamod.tar.gz"), tmp); err != nil {
		return err
	}
	if err := CopyTree(filepath.Join(tmp, "addons"), e.addonsDir()); err != nil {
		// version file NOT bumped, so the updater retries next boot
		e.Log.Log(logx.Error, "Metamod copy failed - keeping previous version")
		return err
	}
	e.Versions.Set("Metamod", latest)
	e.Log.Logf(logx.Success, "Metamod updated to %s", latest)
	return nil
}

// UpdateAll runs the selected updaters and the gameinfo.gi patches in the
// order the egg always used. Errors are logged per addon, never fatal.
func (e *Env) UpdateAll(ctx context.Context, sel Selection, allowTokenless bool) {
	os.MkdirAll(e.Temp, 0o755)
	defer os.RemoveAll(e.Temp)
	gi := e.gameinfo()
	if sel.Metamod {
		e.UpdateMetamod(ctx)
		e.addPath(gi, "csgo/addons/metamod")
	}
	switch changed, err := gi.MetamodFirst(); {
	case err != nil && !errors.Is(err, errNoGameinfo):
		e.Log.Logf(logx.Error, "Failed to reposition MetaMod: %v", err)
	case changed:
		e.Log.Log(logx.Success, "MetaMod repositioned successfully")
	}
	// backup_round*.txt snapshots need their own search path, ahead of
	// MetaMod, or the engine writes them into MetaMod's addon dir
	os.MkdirAll(filepath.Join(e.Root, "game", "csgo", "backups"), 0o755)
	e.addPath(gi, "csgo/backups")
	if _, err := gi.SetTokenless(allowTokenless); err != nil && !errors.Is(err, errNoGameinfo) {
		e.Log.Logf(logx.Error, "Failed to patch RequireLoginForDedicatedServers: %v", err)
	}
}

func (e *Env) addPath(gi Gameinfo, addon string) {
	changed, err := gi.Add(addon)
	switch {
	case errors.Is(err, errNoGameinfo):
		e.Log.Logf(logx.Error, "gameinfo.gi not found at %s", gi.Path)
	case err != nil:
		e.Log.Logf(logx.Error, "Failed to add %s to gameinfo.gi: %v", addon, err)
	case changed:
		e.Log.Logf(logx.Info, "Added %s to gameinfo.gi", addon)
	default:
		e.Log.Logf(logx.Debug, "%s already in gameinfo.gi", addon)
	}
}

// findDir returns the first directory named name whose path ends in suffix.
func findDir(root, name, suffix string) string {
	var found string
	filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || found != "" {
			return nil
		}
		if d.IsDir() && d.Name() == name && strings.HasSuffix(path, suffix) {
			found = path
			return filepath.SkipAll
		}
		return nil
	})
	return found
}
