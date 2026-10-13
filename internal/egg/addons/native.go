// SPDX-License-Identifier: GPL-3.0-or-later

package addons

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/K4ryuu/CS2-Egg-Go/internal/logx"
)

// NativeOptions selects the native Metamod plugins and optional exact release
// tags. An empty version or "latest" follows the release channel in Env.
type NativeOptions struct {
	MultiAddonManager        bool
	GameFixes                bool
	MultiAddonManagerVersion string
	GameFixesVersion         string
}

type nativeAddon struct {
	label, repo, version, dir, binary, config string
	asset                                     *regexp.Regexp
}

// UpdateNative updates selected Metamod plugins. Each archive is downloaded,
// verified, and checked in scratch space before any installed file changes.
func (e *Env) UpdateNative(ctx context.Context, options NativeOptions) error {
	var failures []error
	selected := []struct {
		enabled bool
		addon   nativeAddon
	}{
		{options.MultiAddonManager, nativeAddon{
			label: "MultiAddonManager", repo: RepoMultiAddonManager, version: options.MultiAddonManagerVersion,
			dir: "multiaddonmanager", binary: "bin/multiaddonmanager.so", config: "cfg/multiaddonmanager",
			// The container uses SteamRT3 (sniper). Do not select SteamRT4,
			// even when its asset happens to appear first in a release.
			asset: regexp.MustCompile(`(?i)^MultiAddonManager[-_].*-(?:steamrt3|linux)\.tar\.gz$`),
		}},
		{options.GameFixes, nativeAddon{
			label: "GameFixes", repo: RepoGameFixes, version: options.GameFixesVersion,
			dir: "game_fixes", binary: "bin/linuxsteamrt64/game_fixes.so",
			asset: regexp.MustCompile(`(?i)^GameFixes_mm[-_].*-linux\.tar\.gz$`),
		}},
	}
	for _, item := range selected {
		if !item.enabled {
			continue
		}
		if err := e.updateNative(ctx, item.addon); err != nil {
			e.Log.Logf(logx.Error, "%s update failed: %v", item.addon.label, err)
			failures = append(failures, fmt.Errorf("%s: %w", item.addon.label, err))
		}
	}
	return errors.Join(failures...)
}

func (e *Env) updateNative(ctx context.Context, addon nativeAddon) error {
	want := strings.TrimSpace(addon.version)
	exact := want != "" && want != "latest"
	var rel *Release
	var err error
	if exact {
		rel, err = FetchReleaseTag(ctx, e.HTTP, addon.repo, want)
		if err == nil && rel.Tag != want {
			return fmt.Errorf("requested release %q, received %q", want, rel.Tag)
		}
	} else {
		rel, err = e.release(ctx, addon.label, addon.repo, e.Prerelease)
	}
	if err != nil {
		if e.nativeRetain(ctx, addon, want, exact, err) {
			return nil
		}
		return err
	}
	if rel.Tag == "" {
		return errors.New("release tag is empty")
	}
	if !e.decide(addon.label, addon.label, rel.Tag, exact || rel.Pinned) {
		if NeedsUpdate(e.Versions.Get(addon.label), rel.Tag) == Downgrade {
			// The requested channel may lag an installed prerelease. Keep
			// the newer files, but never claim a broken install is usable.
			return e.nativeInstalledLayout(addon)
		}
		if e.nativeVerifiedInstalled(addon) {
			return nil
		}
		e.Log.Logf(logx.Info, "Repairing or verifying %s at %s", addon.label, rel.Tag)
	}
	var asset Asset
	for _, candidate := range rel.Assets {
		if !addon.asset.MatchString(candidate.Name) {
			continue
		}
		// Recent MAM releases use SteamRT3; older releases used Linux.
		if asset.Name == "" || strings.Contains(strings.ToLower(candidate.Name), "-steamrt3.tar.gz") {
			asset = candidate
		}
	}
	if asset.URL == "" {
		return errors.New("release has no supported Linux archive")
	}
	if err := os.MkdirAll(e.Temp, 0o755); err != nil {
		return err
	}
	tmp, err := os.MkdirTemp(e.Temp, "native-"+addon.dir+"-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	archive := filepath.Join(tmp, "download.tar.gz")
	if err := e.download(ctx, asset.URL, archive); err != nil {
		pin := want
		if rel.Pinned {
			pin = rel.Tag
		}
		if e.nativeRetain(ctx, addon, pin, exact || rel.Pinned, err) {
			return nil
		}
		return err
	}
	if err := nativeVerifyDigest(archive, asset.Digest); err != nil {
		return err
	}
	if err := nativeValidateArchive(archive, addon); err != nil {
		return err
	}
	stage := filepath.Join(tmp, "stage")
	if err := ExtractTarGz(archive, stage); err != nil {
		return fmt.Errorf("extract archive: %w", err)
	}
	if err := nativeValidate(stage, addon); err != nil {
		return err
	}
	record, err := nativeBuildEvidence(stage, addon, rel.Tag)
	if err != nil {
		return err
	}
	if err := nativeInstall(stage, e.Root, addon); err != nil {
		return err
	}
	if err := e.nativeRecordEvidence(addon, record); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(e.Versions.Path), 0o755); err != nil {
		return err
	}
	if err := e.Versions.Set(addon.label, rel.Tag); err != nil {
		return fmt.Errorf("record installed version: %w", err)
	}
	e.Log.Logf(logx.Success, "%s updated to %s", addon.label, rel.Tag)
	return nil
}

func nativeVerifyDigest(file, digest string) error {
	// Older GitHub assets have no digest. When the publisher supplies one,
	// malformed or unsupported values are errors rather than a bypass.
	if digest == "" {
		return nil
	}
	want, ok := strings.CutPrefix(digest, "sha256:")
	decoded, err := hex.DecodeString(want)
	if !ok || err != nil || len(decoded) != sha256.Size {
		return errors.New("invalid release asset SHA-256 digest")
	}
	in, err := os.Open(file)
	if err != nil {
		return err
	}
	defer in.Close()
	h := sha256.New()
	if _, err := io.Copy(h, in); err != nil {
		return err
	}
	if !strings.EqualFold(hex.EncodeToString(h.Sum(nil)), want) {
		return errors.New("release asset SHA-256 mismatch")
	}
	return nil
}

func nativeValidate(stage string, addon nativeAddon) error {
	plugin := "addons/" + addon.dir
	vdf := "addons/metamod/" + addon.dir + ".vdf"
	err := filepath.WalkDir(stage, func(file string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(stage, file)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if (d.IsDir() || d.Type().IsRegular()) && nativeOwnedPath(rel, d.IsDir(), addon) {
			return nil
		}
		return fmt.Errorf("unexpected archive path or file type: %s", rel)
	})
	if err != nil {
		return err
	}
	for _, required := range []string{plugin + "/" + addon.binary, vdf} {
		st, err := os.Stat(filepath.Join(stage, filepath.FromSlash(required)))
		if err != nil || !st.Mode().IsRegular() || st.Size() == 0 {
			return fmt.Errorf("archive is missing required file %s", required)
		}
	}
	data, err := os.ReadFile(filepath.Join(stage, filepath.FromSlash(vdf)))
	if err != nil {
		return err
	}
	return nativeValidateVDF(data, addon)
}

func nativeValidateVDF(data []byte, addon nativeAddon) error {
	// Keep the publisher's VDF, but only accept its single expected loader.
	loader := regexp.MustCompile(`(?s)^\s*"Metamod Plugin"\s*\{\s*(?:"alias"\s*"[^"\r\n]+"\s*)?"file"\s*"([^"\r\n]+)"\s*\}\s*$`).FindSubmatch(data)
	if len(loader) != 2 || string(loader[1]) != strings.TrimSuffix("addons/"+addon.dir+"/"+addon.binary, ".so") {
		return errors.New("archive has an invalid Metamod VDF loader")
	}
	return nil
}

func nativeOwnedPath(rel string, directory bool, addon nativeAddon) bool {
	plugin := "addons/" + addon.dir
	if strings.HasPrefix(rel, plugin+"/") || (addon.config != "" && strings.HasPrefix(rel, addon.config+"/")) {
		return true
	}
	if directory {
		return rel == "." || rel == "addons" || rel == "addons/metamod" || rel == plugin || (addon.config != "" && (rel == "cfg" || rel == addon.config))
	}
	return rel == "addons/metamod/"+addon.dir+".vdf"
}

func nativeValidateArchive(file string, addon nativeAddon) error {
	in, err := os.Open(file)
	if err != nil {
		return err
	}
	defer in.Close()
	gz, err := gzip.NewReader(in)
	if err != nil {
		return fmt.Errorf("read archive: %w", err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	seen := make(map[string]bool)
	for {
		header, err := tr.Next()
		if err == io.EOF {
			// tar stops at its own end marker, before gzip necessarily checks
			// its trailer. Consume the rest so truncation and CRC errors fail.
			if _, err := io.Copy(io.Discard, gz); err != nil {
				return fmt.Errorf("read archive trailer: %w", err)
			}
			return nil
		}
		if err != nil {
			return fmt.Errorf("read archive entry: %w", err)
		}
		directory := header.Typeflag == tar.TypeDir
		if directory && (header.Name == "." || header.Name == "./") {
			continue
		}
		rel, err := entryPath(header.Name)
		if err != nil {
			return err
		}
		if (!directory && header.Typeflag != tar.TypeReg) || !nativeOwnedPath(rel, directory, addon) {
			return fmt.Errorf("unexpected archive path or file type: %s", rel)
		}
		if !directory && seen[rel] {
			return fmt.Errorf("duplicate archive file: %s", rel)
		}
		seen[rel] = true
	}
}

func nativeGameRoot(serverRoot string, create bool) (*os.Root, error) {
	server, err := os.OpenRoot(serverRoot)
	if err != nil {
		return nil, err
	}
	defer server.Close()
	if err := nativeCheckParents(server, "game/csgo/placeholder"); err != nil {
		return nil, err
	}
	if create {
		if err := server.MkdirAll("game/csgo", 0755); err != nil {
			return nil, err
		}
	}
	return server.OpenRoot("game/csgo")
}

func nativeInstall(stage, serverRoot string, addon nativeAddon) error {
	// Create a file plan before writing. Existing files outside bin/ and the
	// loader are private configuration/data, including files in future packages.
	root, err := nativeGameRoot(serverRoot, true)
	if err != nil {
		return err
	}
	defer root.Close()
	var files []string
	err = filepath.WalkDir(stage, func(file string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, err := filepath.Rel(stage, file)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if !nativeManagedFile(rel, addon) {
			if _, err := root.Lstat(rel); err == nil {
				return nil
			} else if !errors.Is(err, fs.ErrNotExist) {
				return err
			}
		}
		files = append(files, rel)
		return nil
	})
	if err != nil {
		return err
	}
	for _, rel := range files {
		if err := nativeCheckParents(root, rel); err != nil {
			return err
		}
	}
	for _, rel := range files {
		in, err := os.Open(filepath.Join(stage, filepath.FromSlash(rel)))
		if err != nil {
			return err
		}
		if err := root.MkdirAll(path.Dir(rel), 0o755); err != nil {
			in.Close()
			return err
		}
		err = writeFile(root, rel, in, 0o644)
		in.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

func nativeManagedFile(rel string, addon nativeAddon) bool {
	return strings.HasPrefix(rel, "addons/"+addon.dir+"/bin/") || rel == "addons/metamod/"+addon.dir+".vdf"
}

func nativeCheckParents(root *os.Root, rel string) error {
	parts := strings.Split(path.Dir(rel), "/")
	for i := range parts {
		parent := strings.Join(parts[:i+1], "/")
		st, err := root.Lstat(parent)
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		if !st.IsDir() {
			return fmt.Errorf("install directory is not a regular directory: %s", parent)
		}
	}
	return nil
}

type nativeEvidence struct {
	Format  int               `json:"format"`
	Repo    string            `json:"repo"`
	Version string            `json:"version"`
	Files   map[string]string `json:"files"`
}

func nativeEvidencePath(addon nativeAddon) string {
	return "egg/native-addons/" + addon.dir + ".json"
}

func nativeFileHash(root *os.Root, rel string) (string, error) {
	if err := nativeCheckParents(root, rel); err != nil {
		return "", err
	}
	st, err := root.Lstat(rel)
	if err != nil {
		return "", err
	}
	if !st.Mode().IsRegular() || st.Size() == 0 {
		return "", fmt.Errorf("native file is not a nonempty regular file: %s", rel)
	}
	in, err := root.Open(rel)
	if err != nil {
		return "", err
	}
	defer in.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, in); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func nativeBuildEvidence(stage string, addon nativeAddon, version string) (nativeEvidence, error) {
	record := nativeEvidence{Format: 1, Repo: addon.repo, Version: version, Files: map[string]string{}}
	root, err := os.OpenRoot(stage)
	if err != nil {
		return record, err
	}
	defer root.Close()
	err = filepath.WalkDir(stage, func(file string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		rel, err := filepath.Rel(stage, file)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if !nativeManagedFile(rel, addon) {
			return nil
		}
		digest, err := nativeFileHash(root, rel)
		if err == nil {
			record.Files[rel] = digest
		}
		return err
	})
	return record, err
}

func (e *Env) nativeVerifyFiles(addon nativeAddon, record nativeEvidence) error {
	if record.Format != 1 || record.Repo != addon.repo || record.Version == "" {
		return errors.New("native installation evidence has an invalid identity")
	}
	for _, required := range []string{"addons/" + addon.dir + "/" + addon.binary, "addons/metamod/" + addon.dir + ".vdf"} {
		if record.Files[required] == "" {
			return errors.New("native installation evidence is incomplete")
		}
	}
	root, err := nativeGameRoot(e.Root, false)
	if err != nil {
		return err
	}
	defer root.Close()
	for rel, expected := range record.Files {
		canonical, err := entryPath(rel)
		decoded, decodeErr := hex.DecodeString(expected)
		if err != nil || canonical != rel || !nativeManagedFile(rel, addon) || decodeErr != nil || len(decoded) != sha256.Size {
			return errors.New("native installation evidence has an invalid file")
		}
		actual, err := nativeFileHash(root, rel)
		if err != nil {
			return err
		}
		if actual != expected {
			return fmt.Errorf("installed native file changed: %s", rel)
		}
	}
	return nil
}

func (e *Env) nativeRecordEvidence(addon nativeAddon, record nativeEvidence) error {
	if err := e.nativeVerifyFiles(addon, record); err != nil {
		return err
	}
	data, err := json.Marshal(record)
	if err != nil {
		return err
	}
	root, err := os.OpenRoot(e.Root)
	if err != nil {
		return err
	}
	defer root.Close()
	rel := nativeEvidencePath(addon)
	if err := nativeCheckParents(root, rel); err != nil {
		return err
	}
	if err := root.MkdirAll(path.Dir(rel), 0o755); err != nil {
		return err
	}
	tmp := rel + ".tmp"
	if err := writeFile(root, tmp, bytes.NewReader(data), 0o600); err != nil {
		return err
	}
	if err := root.Rename(tmp, rel); err != nil {
		root.Remove(tmp)
		return err
	}
	return nil
}

func (e *Env) nativeVerifiedInstalled(addon nativeAddon) bool {
	version := e.Versions.Get(addon.label)
	if version == "" {
		return false
	}
	root, err := os.OpenRoot(e.Root)
	if err != nil {
		return false
	}
	defer root.Close()
	rel := nativeEvidencePath(addon)
	if nativeCheckParents(root, rel) != nil {
		return false
	}
	st, err := root.Lstat(rel)
	if err != nil || !st.Mode().IsRegular() || st.Size() > 1<<20 {
		return false
	}
	data, err := root.ReadFile(rel)
	var record nativeEvidence
	if err != nil || json.Unmarshal(data, &record) != nil || record.Version != version {
		return false
	}
	return e.nativeVerifyFiles(addon, record) == nil
}

func (e *Env) nativeInstalledLayout(addon nativeAddon) error {
	root, err := nativeGameRoot(e.Root, false)
	if err != nil {
		return err
	}
	defer root.Close()
	for _, rel := range []string{"addons/" + addon.dir + "/" + addon.binary, "addons/metamod/" + addon.dir + ".vdf"} {
		if _, err := nativeFileHash(root, rel); err != nil {
			return fmt.Errorf("installed newer %s is incomplete: %w", addon.label, err)
		}
	}
	vdf, err := root.ReadFile("addons/metamod/" + addon.dir + ".vdf")
	if err != nil {
		return err
	}
	return nativeValidateVDF(vdf, addon)
}

func (e *Env) nativeRetain(ctx context.Context, addon nativeAddon, want string, exact bool, cause error) bool {
	if ctx.Err() != nil || !IsTransientReleaseError(cause) || exact && e.Versions.Get(addon.label) != want || !e.nativeVerifiedInstalled(addon) {
		return false
	}
	e.Log.Logf(logx.Warning, "%s update service is temporarily unavailable; keeping verified installed version %s", addon.label, e.Versions.Get(addon.label))
	return true
}
