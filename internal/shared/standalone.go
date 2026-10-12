// SPDX-License-Identifier: GPL-3.0-or-later
package shared

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/s2script/s2script-cs2-egg/internal/artifact"
)

// StandaloneLocked detaches all file links before removing the safety marker.
// Unknown/directory symlinks fail closed; they are never traversed to find files.
// The caller holds the runtime lease and artifact lock throughout the migration.
func StandaloneLocked(ctx context.Context, root string) error {
	if _, e := ReadPending(root); e == nil {
		return errors.New("resume interrupted shared preparation before standalone migration")
	} else if !errors.Is(e, os.ErrNotExist) {
		return e
	}
	local, e := ReadLocal(root)
	if e != nil {
		return e
	}
	d, e := artifact.OpenDirectory(root)
	if e != nil {
		return e
	}
	defer d.Close()
	type candidate struct {
		path string
		info os.FileInfo
	}
	var paths []candidate
	var need uint64 = 16 << 20
	e = filepath.WalkDir(root, func(p string, entry os.DirEntry, e error) error {
		if e != nil {
			return e
		}
		if entry.IsDir() {
			return nil
		}
		rel, e := filepath.Rel(root, p)
		if e != nil {
			return e
		}
		info, target, e := infoAt(d, rel)
		if e != nil {
			return e
		}
		if info.Mode()&os.ModeSymlink == 0 && (!info.Mode().IsRegular() || info.Sys().(*syscall.Stat_t).Nlink == 1 && !sharedVPK(rel)) {
			return nil
		}
		if !safePath(rel) {
			return fmt.Errorf("cannot detach control-file link %s; repair the link while stopped before migration; shared marker retained", rel)
		}
		// Pin and validate sources before counting bytes; no os.Stat through links.
		input, _, e := standaloneInput(d, root, rel, target, local)
		if e != nil {
			return e
		}
		sourceInfo, e := input.Stat()
		input.Close()
		if e != nil {
			return e
		}
		need += uint64(sourceInfo.Size())
		paths = append(paths, candidate{rel, sourceInfo})
		return nil
	})
	if e != nil {
		return e
	}
	if e = diskSpace(root, need); e != nil {
		return e
	}
	for _, path := range paths {
		if e = ctx.Err(); e != nil {
			return e
		}
		_, target, e := infoAt(d, path.path)
		if e != nil {
			return e
		}
		input, expected, e := standaloneInput(d, root, path.path, target, local)
		if e != nil {
			return e
		}
		info, e := input.Stat()
		if e != nil || !os.SameFile(info, path.info) {
			input.Close()
			return fmt.Errorf("standalone source changed: %s; retry migration with the shared marker retained", path.path)
		}
		mode := info.Mode().Perm() | 0200
		if private(path.path) {
			mode = 0600 | mode&0111
		}
		tmp := ".s2shared-copy-" + randomID()
		out, e := artifact.OpenRegular(d, tmp, syscall.O_WRONLY|syscall.O_CREAT|syscall.O_EXCL, mode)
		if e != nil {
			input.Close()
			return e
		}
		h := sha256.New()
		n, copyErr := io.Copy(io.MultiWriter(out, h), io.LimitReader(input, info.Size()+1))
		if copyErr == nil && (n != info.Size() || expected != nil && (n != expected.Size || hex.EncodeToString(h.Sum(nil)) != expected.Digest)) {
			copyErr = errors.New("standalone source changed or corrupt")
		}
		e = errors.Join(copyErr, input.Close(), out.Sync(), out.Close())
		if e == nil {
			e = artifact.Rename(d, tmp, path.path)
		}
		if e != nil {
			_ = artifact.Remove(d, tmp)
			return e
		}
	}
	// Verify no link remains, including private/override paths absent from Managed.
	// WalkDir does not traverse directory symlinks; any such entry is an error.
	e = filepath.WalkDir(root, func(p string, entry os.DirEntry, e error) error {
		if e != nil {
			return e
		}
		if entry.IsDir() {
			return nil
		}
		rel, e := filepath.Rel(root, p)
		if e != nil {
			return e
		}
		info, _, e := infoAt(d, rel)
		if e != nil {
			return e
		}
		if info.Mode()&os.ModeSymlink != 0 || info.Mode().IsRegular() && info.Sys().(*syscall.Stat_t).Nlink > 1 {
			return fmt.Errorf("shared or unrecognized link remains at %s; marker retained", rel)
		}
		return nil
	})
	if e != nil {
		return e
	}
	if e = ctx.Err(); e != nil {
		return e
	}
	return artifact.Remove(d, LocalState)
}

// standaloneInput accepts regular local files or exact file entries within a
// known generation manifest. It never opens arbitrary symlink targets, follows
// directory symlinks, or treats a generation-looking path alone as authority.
func standaloneInput(root *os.File, rootPath, path, target string, local Local) (*os.File, *File, error) {
	if target == "" {
		input, e := artifact.OpenRegular(root, path, syscall.O_RDONLY, 0)
		return input, nil, e
	}
	invalid := func() (*os.File, *File, error) {
		return nil, nil, fmt.Errorf("cannot safely detach symlink %s: restore a local regular file or a known generation file, then retry; shared marker retained", path)
	}
	if !filepath.IsAbs(target) {
		target = filepath.Join(rootPath, filepath.Dir(path), target)
	}
	if filepath.Clean(target) != target {
		return invalid()
	}
	prefix, suffix, found := strings.Cut(target, "/generations/")
	id, file, foundFile := strings.Cut(suffix, "/")
	if !found || !foundFile || prefix == "" || !validID.MatchString(id) || !safePath(file) {
		return invalid()
	}
	known, want := id == local.Generation.ID, ""
	if known {
		want = local.Generation.Digest
	}
	for _, dependency := range local.Dependencies {
		known = known || dependency == id
	}
	if !known {
		return invalid()
	}
	manifest, e := LoadManifest(prefix, id, want)
	if e != nil {
		return nil, nil, fmt.Errorf("cannot safely detach symlink %s; shared marker retained: %w", path, e)
	}
	for _, f := range manifest.Files {
		if f.Path != file {
			continue
		}
		source, e := artifact.OpenDirectory(manifest.Generation.Root)
		if e != nil {
			return nil, nil, e
		}
		input, e := artifact.OpenRegular(source, file, syscall.O_RDONLY, 0)
		source.Close()
		return input, &f, e
	}
	return invalid()
}
