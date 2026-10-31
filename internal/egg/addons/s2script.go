// SPDX-License-Identifier: GPL-3.0-or-later

package addons

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/K4ryuu/CS2-Egg-Go/internal/logx"
)

// S2ScriptOptions selects an exact version or the latest release channel.
type S2ScriptOptions struct {
	Version    string
	Prerelease bool
}

var s2scriptVersion = regexp.MustCompile(`^v?[0-9]+\.[0-9]+\.[0-9]+(?:-[0-9A-Za-z.-]+)?(?:\+[0-9A-Za-z.-]+)?$`)

// IsTransientReleaseError identifies release-service outages, never failures of
// publisher integrity, TLS verification, authentication, or origin policy.
func IsTransientReleaseError(err error) bool {
	if err == nil {
		return false
	}
	// TLS errors sometimes arrive as a net.OpError with an unexported alert.
	message := strings.ToLower(err.Error())
	if strings.Contains(message, "tls") || strings.Contains(message, "x509") || strings.Contains(message, "certificate") {
		return false
	}
	var status *APIStatusError
	if errors.As(err, &status) {
		return status.StatusCode == 429 || status.StatusCode >= 500 && status.StatusCode <= 599
	}
	if errors.Is(err, context.Canceled) {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var dns *net.DNSError
	if errors.As(err, &dns) {
		return true
	}
	var op *net.OpError
	if errors.As(err, &op) {
		var address net.InvalidAddrError
		if errors.As(err, &address) {
			return false
		}
		return op.Op == "dial" || op.Op == "read" || op.Op == "write"
	}
	return false
}

// UpdateS2Script refreshes framework files and bundled plugins while keeping
// operator configuration, data, translation wording and installed plugin placement.
func (e *Env) UpdateS2Script(ctx context.Context, options S2ScriptOptions) error {
	want := strings.TrimSpace(options.Version)
	exact := want != "" && want != "latest"
	var rel *Release
	var err error
	if exact {
		if !s2scriptVersion.MatchString(want) {
			return fmt.Errorf("invalid S2Script version %q", want)
		}
		want = "v" + strings.TrimPrefix(want, "v")
		rel, err = FetchReleaseTag(ctx, e.HTTP, RepoS2Script, want)
	} else {
		rel, err = e.release(ctx, "S2Script", RepoS2Script, options.Prerelease)
	}
	if err != nil {
		return e.s2scriptRetained(err, want)
	}
	if !s2scriptVersion.MatchString(rel.Tag) || (!exact && !options.Prerelease && !rel.Pinned && rel.Prerelease) || (exact && rel.Tag != want) {
		return errors.New("S2Script release does not match the selected version/channel")
	}
	version := strings.TrimPrefix(rel.Tag, "v")
	if rel.Pinned {
		// A node pin is just as binding as an explicit panel version, including
		// when source or asset availability requires retaining installed files.
		want = rel.Tag
	}
	current, _ := os.ReadFile(filepath.Join(e.addonsDir(), "s2script", "VERSION"))
	installed := strings.TrimSpace(string(current))
	verdict := NeedsUpdate(installed, version)
	if installed != "" && (verdict == UpToDate || verdict == Downgrade && !exact && !rel.Pinned) {
		// Retry an incomplete installation even if its VERSION file survived.
		if s2scriptLayout(filepath.Join(e.Root, "game", "csgo"), installed) == nil && S2ScriptHostVerified(e.Root, installed) {
			return nil
		}
		if verdict == Downgrade {
			return errors.New("installed S2Script is newer than the channel but its files cannot be verified; select its exact release to repair it")
		}
	}
	var assetURL, digest string
	for _, asset := range rel.Assets {
		if asset.Name == "s2script-cs2-linux-"+version+".zip" {
			assetURL, digest = asset.URL, asset.Digest
			break
		}
	}
	if assetURL == "" {
		return errors.New("no S2Script CS2 Linux release asset")
	}
	expected, err := hex.DecodeString(strings.TrimPrefix(digest, "sha256:"))
	if err != nil || !strings.HasPrefix(digest, "sha256:") || len(expected) != sha256.Size {
		return errors.New("S2Script asset lacks a valid publisher SHA256 digest")
	}
	revision, err := e.s2scriptSource(ctx, rel.Tag)
	if err != nil {
		return e.s2scriptRetained(err, want)
	}
	if err := os.MkdirAll(e.Temp, 0o755); err != nil {
		return err
	}
	tmp, err := os.MkdirTemp(e.Temp, "s2script-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	archive := filepath.Join(tmp, "release.zip")
	if err := e.download(ctx, assetURL, archive); err != nil {
		return e.s2scriptRetained(err, want)
	}
	f, err := os.Open(archive)
	if err != nil {
		return err
	}
	hash := sha256.New()
	_, err = io.Copy(hash, f)
	f.Close()
	if err != nil {
		return err
	}
	if hex.EncodeToString(hash.Sum(nil)) != strings.ToLower(strings.TrimPrefix(digest, "sha256:")) {
		return errors.New("S2Script archive publisher SHA256 mismatch")
	}
	if err := s2scriptValidateZIP(archive); err != nil {
		return err
	}
	stage := filepath.Join(tmp, "extracted")
	if err := ExtractZip(archive, stage); err != nil {
		return err
	}
	if err := s2scriptLayout(stage, version); err != nil {
		return err
	}
	if err := e.installS2Script(stage, version, revision); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(e.Versions.Path), 0o755); err != nil {
		return err
	}
	if err := e.Versions.Set("S2Script", rel.Tag); err != nil {
		return err
	}
	e.Log.Logf(logx.Success, "S2Script updated to %s", rel.Tag)
	return nil
}

func (e *Env) s2scriptRetained(cause error, want string) error {
	if !IsTransientReleaseError(cause) {
		return cause
	}
	data, err := os.ReadFile(filepath.Join(e.addonsDir(), "s2script", "VERSION"))
	if err != nil {
		return cause
	}
	current := strings.TrimPrefix(strings.TrimSpace(string(data)), "v")
	if want != "" && want != "latest" && strings.TrimPrefix(want, "v") != current {
		return cause
	}
	if s2scriptLayout(filepath.Join(e.Root, "game", "csgo"), current) != nil || !S2ScriptHostVerified(e.Root, current) {
		return cause
	}
	e.Log.Logf(logx.Warning, "S2Script release service unavailable; keeping verified installed version %s", current)
	return nil
}

func s2scriptValidateZIP(archive string) error {
	z, err := zip.OpenReader(archive)
	if err != nil {
		return err
	}
	defer z.Close()
	seen := map[string]bool{}
	var size uint64
	for _, file := range z.File {
		name, err := entryPath(file.Name)
		if err != nil {
			return err
		}
		if seen[name] || !(file.Mode().IsRegular() || file.FileInfo().IsDir()) {
			return fmt.Errorf("S2Script ZIP has duplicate or unsupported entry %s", name)
		}
		seen[name] = true
		if file.UncompressedSize64 > 1<<30 || size > 1<<30-file.UncompressedSize64 {
			return errors.New("S2Script archive exceeds expanded size limit")
		}
		size += file.UncompressedSize64
	}
	return nil
}

// Only the reviewed API 3 family supports the API 2 compatibility exemptions.
// Resolve the tag to immutable source before checking the publisher's loader.
func (e *Env) s2scriptSource(ctx context.Context, tag string) (string, error) {
	body, err := get(ctx, e.HTTP, APIBase+"/repos/"+RepoS2Script+"/commits/"+tag, RepoS2Script)
	if err != nil {
		return "", err
	}
	var commit struct {
		SHA string `json:"sha"`
	}
	if json.Unmarshal(body, &commit) != nil || !regexp.MustCompile(`^[0-9a-f]{40}$`).MatchString(commit.SHA) {
		return "", errors.New("S2Script source revision is not immutable")
	}
	body, err = get(ctx, e.HTTP, APIBase+"/repos/"+RepoS2Script+"/contents/core/src/loader.rs?ref="+commit.SHA, RepoS2Script)
	if err != nil {
		return "", err
	}
	var file struct{ Content, Encoding string }
	if err := json.Unmarshal(body, &file); err != nil {
		return "", err
	}
	source, err := base64.StdEncoding.DecodeString(file.Content)
	if err != nil || file.Encoding != "base64" || !regexp.MustCompile(`HOST_API_VERSION_MAJOR\s*:\s*u32\s*=\s*3\s*;`).Match(source) || !regexp.MustCompile(`m\s*==\s*2\s*\|\|\s*m\s*==\s*HOST_API_VERSION_MAJOR`).Match(source) {
		return "", errors.New("S2Script release has an unreviewed host API compatibility family")
	}
	return commit.SHA, nil
}

func s2scriptLayout(root, version string) error {
	for _, name := range []string{"addons/metamod/s2script.vdf", "addons/s2script/bin/linuxsteamrt64/s2script.so", "addons/s2script/bin/linuxsteamrt64/libs2script_core.so", "addons/s2script/VERSION"} {
		info, err := os.Stat(filepath.Join(root, name))
		if err != nil || !info.Mode().IsRegular() || info.Size() == 0 {
			return fmt.Errorf("S2Script archive missing required Linux file %s", name)
		}
	}
	b, err := os.ReadFile(filepath.Join(root, "addons/s2script/VERSION"))
	if err != nil {
		return err
	}
	if strings.TrimPrefix(strings.TrimSpace(string(b)), "v") != strings.TrimPrefix(version, "v") {
		return errors.New("S2Script archive VERSION differs from release")
	}
	return nil
}

// s2scriptPluginID reads identity rather than guessing from a filename, because
// users may rename bundled packages or move them into plugins/disabled/.
func s2scriptPluginID(filename string) (string, error) {
	f, err := os.Open(filename)
	if err != nil {
		return "", err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, s2MaxPlugin+1))
	if err != nil {
		return "", err
	}
	if len(data) > s2MaxPlugin {
		return "", errors.New("S2Script plugin exceeds size limit")
	}
	manifest, _, err := s2Inspect(data)
	return manifest.ID, err
}

func (e *Env) installS2Script(stage, version, revision string) error {
	store, err := s2OpenStore(e.Root)
	if err != nil {
		return err
	}
	native, err := store.nativeManifest()
	if err != nil {
		store.close()
		return err
	}
	var nativeOwned map[string]bool
	if native != nil {
		nativeOwned, err = store.nativeOwned(native)
		if err != nil {
			store.close()
			return err
		}
	}
	actual, err := store.discover()
	if err == nil {
		actual, err = store.retained(actual)
	}
	if err == nil {
		err = s2CheckFile(store.egg, "installed-list.json")
	}
	store.close()
	if err != nil {
		return err
	}
	receipts := map[string]s2Installed{}
	for _, plugin := range actual {
		receipts[plugin.Name] = plugin
	}
	server, err := os.OpenRoot(e.Root)
	if err != nil {
		return err
	}
	err = s2scriptCheckParents(server, "game/csgo/placeholder")
	server.Close()
	if err != nil {
		return err
	}
	game := filepath.Join(e.Root, "game", "csgo")
	plugins := filepath.Join(game, "addons", "s2script", "plugins")
	existing := map[string]string{}
	occupied := map[string]bool{}
	for _, dir := range []string{plugins, filepath.Join(plugins, "disabled")} {
		entries, err := os.ReadDir(dir)
		if err != nil && !os.IsNotExist(err) {
			return err
		}
		for _, entry := range entries {
			if !entry.Type().IsRegular() || !strings.HasSuffix(entry.Name(), ".s2sp") {
				continue
			}
			p := filepath.Join(dir, entry.Name())
			occupied[p] = true
			id, err := s2scriptPluginID(p)
			if err != nil {
				continue
			} // unrelated user packages are left alone
			if _, duplicate := existing[id]; duplicate {
				return fmt.Errorf("multiple installed S2Script plugins have identity %s", id)
			}
			existing[id] = p
		}
	}
	type update struct {
		source, target                string
		preserve, plugin, translation bool
	}
	var writes []update
	bundled := map[string]bool{}
	err = filepath.WalkDir(filepath.Join(stage, "addons"), func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(stage, p)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if rel != "addons/metamod/s2script.vdf" && !strings.HasPrefix(rel, "addons/s2script/") {
			return nil
		}
		if !d.Type().IsRegular() {
			return fmt.Errorf("unsupported S2Script archive file %s", rel)
		}
		if rel == "addons/s2script/VERSION" {
			return nil
		} // publish VERSION only after the other copies succeed
		preserve := rel == "addons/s2script/plugins.json" || rel == "addons/s2script/plugins-lock.json" || strings.HasPrefix(rel, "addons/s2script/configs/") || strings.HasPrefix(rel, "addons/s2script/data/") || strings.Contains(rel, "/gamedata/") && strings.Contains(rel, "/custom/")
		plugin := strings.HasPrefix(rel, "addons/s2script/plugins/")
		if plugin {
			if !strings.HasSuffix(rel, ".s2sp") {
				return nil
			}
			parent := path.Dir(rel)
			if parent != "addons/s2script/plugins" && parent != "addons/s2script/plugins/disabled" {
				return errors.New("unsupported bundled S2Script plugin location")
			}
			id, err := s2scriptPluginID(p)
			if err != nil {
				return fmt.Errorf("bundled plugin %s: %w", rel, err)
			}
			if bundled[id] {
				return fmt.Errorf("duplicate bundled S2Script plugin identity %s", id)
			}
			bundled[id] = true
			if nativeOwned[id] {
				return nil // the native registry updater owns this package
			}
			if target, ok := existing[id]; ok {
				// retained only carries ownership when the current identity,
				// version, filename and bytes match the verified prior receipt.
				receipt := receipts[id]
				if receipt.Source == "registry" {
					return fmt.Errorf("bundled S2Script plugin %s has a registry source conflict; refusing to replace it with release content", id)
				}
				if !receipt.Managed || receipt.Verification != "verified" {
					data, err := os.ReadFile(p)
					if err != nil {
						return err
					}
					hash := sha256.Sum256(data)
					if receipt.SHA256 != hex.EncodeToString(hash[:]) {
						return fmt.Errorf("bundled S2Script plugin %s is unowned or modified; refusing to overwrite it", id)
					}
				}
				rel, err = filepath.Rel(game, target)
				if err != nil {
					return err
				}
			} else if occupied[filepath.Join(game, rel)] {
				return fmt.Errorf("bundled S2Script plugin %s would overwrite a different installed identity", id)
			}
		}
		writes = append(writes, update{
			source: p, target: rel, preserve: preserve, plugin: plugin,
			translation: strings.HasPrefix(rel, "addons/s2script/translations/") && strings.HasSuffix(rel, ".phrases.json"),
		})
		return nil
	})
	if err != nil {
		return err
	}
	root, err := openTarget(game)
	if err != nil {
		return err
	}
	defer root.Close()
	for i := range writes {
		file := &writes[i]
		if err := s2scriptCheckParents(root, file.target); err != nil {
			return err
		}
		if file.translation {
			skip, err := s2scriptMergeTranslations(root, file.target, file.source)
			if err != nil {
				return err
			}
			file.preserve = skip
		}
	}
	verifiedPlugins := map[string]string{}
	verifiedNative := map[string]string{}
	copyFile := func(file update) error {
		if file.preserve {
			if _, err := root.Lstat(file.target); err == nil {
				return nil
			} else if !os.IsNotExist(err) {
				return err
			}
		}
		if err := root.MkdirAll(filepath.Dir(file.target), 0o755); err != nil {
			return err
		}
		in, err := os.Open(file.source)
		if err != nil {
			return err
		}
		info, err := in.Stat()
		hash := sha256.New()
		if err == nil {
			if file.translation {
				err = s2scriptWriteTranslation(root, file.target, in, info.Mode())
			} else {
				err = writeFile(root, file.target, io.TeeReader(in, hash), info.Mode())
			}
		}
		in.Close()
		if err != nil {
			return err
		}
		if file.plugin {
			verifiedPlugins[filepath.ToSlash(filepath.Join("game", "csgo", file.target))] = hex.EncodeToString(hash.Sum(nil))
		}
		if file.target == "addons/metamod/s2script.vdf" || file.target == "addons/s2script/bin/linuxsteamrt64/s2script.so" || file.target == "addons/s2script/bin/linuxsteamrt64/libs2script_core.so" {
			verifiedNative[filepath.ToSlash(filepath.Join("game", "csgo", file.target))] = hex.EncodeToString(hash.Sum(nil))
		}
		return nil
	}
	for _, file := range writes {
		if err := copyFile(file); err != nil {
			return err
		}
	}
	if err := RegisterS2ScriptReleasePlugins(e.Root, verifiedPlugins); err != nil {
		return err
	}
	if err := RegisterS2ScriptHost(e.Root, version, revision, verifiedNative); err != nil {
		return err
	}
	return copyFile(update{source: filepath.Join(stage, "addons/s2script/VERSION"), target: "addons/s2script/VERSION"})
}

// A confined root also permits in-tree symlinks; reject those as destination
// parents so addon updates cannot overwrite another game directory.
func s2scriptCheckParents(root *os.Root, target string) error {
	parts := strings.Split(path.Dir(filepath.ToSlash(target)), "/")
	for i := range parts {
		parent := strings.Join(parts[:i+1], "/")
		info, err := root.Lstat(parent)
		if os.IsNotExist(err) {
			break
		}
		if err != nil {
			return err
		}
		if !info.IsDir() {
			return fmt.Errorf("S2Script install parent is not a directory: %s", parent)
		}
	}
	return nil
}
