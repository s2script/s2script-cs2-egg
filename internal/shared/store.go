// SPDX-License-Identifier: GPL-3.0-or-later
// Package shared publishes independent immutable app730 generations. Operators
// must keep the store daemon-owned and outside writable container mounts.
package shared

import (
	"context"
	"crypto/rand"
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
	"strings"
	"syscall"
	"time"

	"github.com/s2script/s2script-cs2-egg/internal/artifact"
	node "github.com/s2script/s2script-cs2-egg/internal/nodetypes"
	"github.com/s2script/s2script-cs2-egg/internal/server"
	"github.com/s2script/s2script-cs2-egg/internal/steam"
)

const DefaultRoot = "/srv/s2node/shared"
const MountRoot = "/mnt/s2node"
const maxMetadata = 64 << 20

var validID = regexp.MustCompile(`^[a-f0-9]{32}$`)

type Generation struct {
	ID        string    `json:"id"`
	BuildID   string    `json:"build_id"`
	Root      string    `json:"-"`
	Published time.Time `json:"published"`
	Digest    string    `json:"digest"`
}
type File struct {
	Path   string `json:"path"`
	Digest string `json:"digest"`
	Size   int64  `json:"size"`
	Mode   uint32 `json:"mode"`
}
type Manifest struct {
	Generation Generation `json:"generation"`
	Files      []File     `json:"files"`
}
type ServerState interface {
	Servers() []node.Server
	Status(string) server.Status
}
type Options struct {
	State          ServerState
	Registered     []node.Server
	Root, SteamCMD string
	ReserveBytes   uint64
	Update         func(context.Context, string) (string, error)
	Helpers        map[string]HelperEndpoint
}
type Store struct {
	root    string
	options Options
}

func New(o Options) (*Store, error) {
	if o.Root == "" {
		o.Root = DefaultRoot
	}
	if !filepath.IsAbs(o.Root) || filepath.Clean(o.Root) != o.Root || o.Root == "/" {
		return nil, errors.New("shared root must be a clean absolute directory")
	}
	if o.ReserveBytes == 0 {
		o.ReserveBytes = 64 << 30
	}
	for _, d := range []string{o.Root, filepath.Join(o.Root, "generations"), filepath.Join(o.Root, ".build")} {
		if e := os.MkdirAll(d, 0755); e != nil {
			return nil, e
		}
		f, e := artifact.OpenDirectory(d)
		if e != nil {
			return nil, e
		}
		f.Close()
	}
	return &Store{root: o.Root, options: o}, nil
}
func randomID() string {
	var b [16]byte
	if _, e := rand.Read(b[:]); e != nil {
		panic(e)
	}
	return hex.EncodeToString(b[:])
}
func digest(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }
func diskSpace(path string, needed uint64) error {
	var s syscall.Statfs_t
	if e := syscall.Statfs(path, &s); e != nil {
		return e
	}
	if s.Bavail*uint64(s.Bsize) < needed {
		return fmt.Errorf("insufficient disk space: need at least %d free bytes", needed)
	}
	return nil
}
func readJSON(root, name string, v any) error {
	d, e := artifact.OpenDirectory(root)
	if e != nil {
		return e
	}
	defer d.Close()
	f, e := artifact.OpenRegular(d, name, syscall.O_RDONLY, 0)
	if e != nil {
		return e
	}
	defer f.Close()
	b, e := io.ReadAll(io.LimitReader(f, maxMetadata+1))
	if e != nil || len(b) > maxMetadata {
		return errors.New("shared metadata unreadable or oversized")
	}
	dec := json.NewDecoder(strings.NewReader(string(b)))
	dec.DisallowUnknownFields()
	if e = dec.Decode(v); e != nil {
		return e
	}
	if dec.Decode(new(any)) != io.EOF {
		return errors.New("trailing shared metadata")
	}
	return nil
}
func writeJSON(root, name string, v any, mode os.FileMode) error {
	b, e := json.Marshal(v)
	if e != nil {
		return e
	}
	if len(b) > maxMetadata {
		return errors.New("shared metadata exceeds limit")
	}
	d, e := artifact.OpenDirectory(root)
	if e != nil {
		return e
	}
	defer d.Close()
	tmp := name + ".tmp-" + randomID()
	f, e := artifact.OpenRegular(d, tmp, syscall.O_WRONLY|syscall.O_CREAT|syscall.O_EXCL, mode)
	if e != nil {
		return e
	}
	defer artifact.Remove(d, tmp)
	_, e = f.Write(b)
	e = errors.Join(e, f.Sync(), f.Close())
	if e != nil {
		return e
	}
	return artifact.Rename(d, tmp, name)
}
func (s *Store) Current() (Generation, error) {
	var g Generation
	if e := readJSON(s.root, "current.json", &g); e != nil {
		return g, fmt.Errorf("no published generation; run s2node shared-build: %w", e)
	}
	m, e := LoadManifest(s.root, g.ID, g.Digest)
	return m.Generation, e
}
func LoadManifest(root, id, want string) (Manifest, error) {
	var m Manifest
	if !validID.MatchString(id) {
		return m, errors.New("invalid generation ID")
	}
	p := filepath.Join(root, "generations", id)
	if e := readJSON(p, "manifest.json", &m); e != nil {
		return m, e
	}
	b, e := json.Marshal(m.Files)
	if e != nil || m.Generation.ID != id || digest(b) != m.Generation.Digest || want != "" && want != m.Generation.Digest {
		return m, errors.New("generation manifest digest mismatch")
	}
	if len(m.Files) == 0 || len(m.Files) > 100000 {
		return m, errors.New("invalid generation file count")
	}
	seen := map[string]bool{}
	for _, f := range m.Files {
		if !safePath(f.Path) || seen[f.Path] || f.Size < 0 || len(f.Digest) != 64 || f.Mode&^uint32(0777) != 0 || f.Mode&0222 != 0 {
			return m, errors.New("invalid generation file entry")
		}
		seen[f.Path] = true
	}
	m.Generation.Root = p
	return m, nil
}
func safePath(p string) bool {
	if p == "" || p == "." || filepath.IsAbs(p) || filepath.Clean(p) != p || strings.ContainsAny(p, "\\:\x00") || strings.HasPrefix(p, "../") {
		return false
	}
	for _, c := range strings.Split(p, "/") {
		if strings.HasPrefix(c, ".s2shared") || c == ".runtime-lease" || strings.HasPrefix(c, ".artifact-") {
			return false
		}
	}
	return true
}
func scan(root string) ([]File, error) {
	d, e := artifact.OpenDirectory(root)
	if e != nil {
		return nil, e
	}
	defer d.Close()
	var files []File
	e = filepath.WalkDir(root, func(p string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		rel, e := filepath.Rel(root, p)
		if e == nil && entry.Type()&os.ModeSymlink != 0 && private(rel) {
			return os.Remove(p)
		} // HOME bookkeeping links are not distributable game data.
		if e != nil || !safePath(rel) || !entry.Type().IsRegular() {
			return errors.New("Steam staging contains unsupported path or symlink")
		}
		f, e := artifact.OpenRegular(d, rel, syscall.O_RDONLY, 0)
		if e != nil {
			return e
		}
		info, e := f.Stat()
		if e != nil {
			f.Close()
			return e
		}
		h := sha256.New()
		_, e = io.Copy(h, f)
		e = errors.Join(e, f.Close())
		if e != nil {
			return e
		}
		mode := uint32(0444)
		if info.Mode().Perm()&0111 != 0 {
			mode = 0555
		}
		files = append(files, File{rel, hex.EncodeToString(h.Sum(nil)), info.Size(), mode})
		if len(files) > 100000 {
			return errors.New("generation exceeds file limit")
		}
		return nil
	})
	return files, e
}
func (s *Store) Build(ctx context.Context) (Generation, error) {
	var zero Generation
	unlock, e := artifact.Acquire(filepath.Join(s.root, ".build"))
	if e != nil {
		return zero, e
	}
	defer unlock()
	if e = ctx.Err(); e != nil {
		return zero, e
	}
	if e = diskSpace(s.root, s.options.ReserveBytes); e != nil {
		return zero, e
	}
	id := randomID()
	stage := filepath.Join(s.root, ".build", id)
	if e = os.Mkdir(stage, 0700); e != nil {
		return zero, e
	}
	defer removeTree(stage)
	var build string
	if s.options.Update != nil {
		build, e = s.options.Update(ctx, stage)
	} else {
		build, e = steam.Update(ctx, s.options.SteamCMD, stage, true, nil)
		if e == nil {
			e = seedSDK(stage, s.options.SteamCMD)
		}
	}
	if e != nil {
		return zero, e
	}
	actual, e := steam.InstalledBuild(stage)
	if e != nil || actual != build {
		return zero, errors.New("staging installed build does not match Steam result")
	}
	files, e := scan(stage)
	if e != nil {
		return zero, e
	}
	required := map[string]bool{"game/bin/linuxsteamrt64/cs2": false, "game/csgo/gameinfo.gi": false, ".steam/sdk64/steamclient.so": false}
	vpk := false
	for _, f := range files {
		if _, ok := required[f.Path]; ok && f.Size > 0 && (f.Path != "game/bin/linuxsteamrt64/cs2" || f.Mode&0111 != 0) {
			required[f.Path] = true
		}
		if strings.HasSuffix(f.Path, ".vpk") && f.Size > 0 {
			vpk = true
		}
	}
	for _, ok := range required {
		if !ok {
			return zero, errors.New("incomplete CS2 generation")
		}
	}
	if !vpk {
		return zero, errors.New("generation has no VPK")
	}
	b, _ := json.Marshal(files)
	g := Generation{ID: id, BuildID: build, Published: time.Now().UTC(), Digest: digest(b)}
	if e = writeJSON(stage, "manifest.json", Manifest{g, files}, 0444); e != nil {
		return zero, e
	}
	for _, f := range files {
		file, e := os.OpenFile(filepath.Join(stage, f.Path), os.O_RDONLY, 0)
		if e != nil {
			return zero, e
		}
		e = errors.Join(file.Chmod(os.FileMode(f.Mode)), file.Sync(), file.Close())
		if e != nil {
			return zero, e
		}
	}
	if e = filepath.WalkDir(stage, func(p string, d fs.DirEntry, e error) error {
		if e != nil {
			return e
		}
		if d.IsDir() && p != stage {
			f, e := os.Open(p)
			if e != nil {
				return e
			}
			return errors.Join(f.Chmod(0555), f.Sync(), f.Close())
		}
		return nil
	}); e != nil {
		return zero, e
	}
	if e = ctx.Err(); e != nil {
		return zero, e
	}
	release, e := artifact.Acquire(s.root)
	if e != nil {
		return zero, e
	}
	defer release()
	d, e := artifact.OpenDirectory(s.root)
	if e != nil {
		return zero, e
	}
	defer d.Close()
	if e = artifact.Rename(d, ".build/"+id, "generations/"+id); e != nil {
		return zero, fmt.Errorf("publish rename: %w", e)
	}
	published, e := artifact.OpenDirectory(filepath.Join(s.root, "generations", id))
	if e != nil {
		return zero, e
	}
	e = errors.Join(published.Chmod(0555), published.Sync(), published.Close())
	if e != nil {
		return zero, e
	}
	if e = writeJSON(s.root, "current.json", g, 0644); e != nil {
		return zero, e
	}
	g.Root = filepath.Join(s.root, "generations", id)
	return g, nil
}

// Only unreferenced generation directories are made traversable for deletion;
// never chmod their files, which can still have immutable hardlinks elsewhere.
func removeTree(p string) error {
	e := filepath.WalkDir(p, func(path string, d fs.DirEntry, e error) error {
		if errors.Is(e, os.ErrNotExist) {
			return nil
		}
		if e != nil {
			return e
		}
		if d.IsDir() {
			return os.Chmod(path, 0755)
		}
		return nil
	})
	return errors.Join(e, os.RemoveAll(p))
}

func seedSDK(stage, executable string) error {
	source, e := artifact.OpenDirectory(filepath.Dir(executable))
	if e != nil {
		return e
	}
	defer source.Close()
	dest, e := artifact.OpenDirectory(stage)
	if e != nil {
		return e
	}
	defer dest.Close()
	for _, arch := range []string{"64", "32"} {
		name := "linux" + arch + "/steamclient.so"
		in, e := artifact.OpenRegular(source, name, syscall.O_RDONLY, 0)
		if e != nil {
			if arch == "32" && errors.Is(e, os.ErrNotExist) {
				continue
			}
			return fmt.Errorf("host SteamCMD SDK%s unavailable; bootstrap dedicated host SteamCMD first: %w", arch, e)
		}
		target := ".steam/sdk" + arch + "/steamclient.so"
		parent, _, e := artifact.ParentDirectory(dest, target, true)
		if e != nil {
			in.Close()
			return e
		}
		parent.Close()
		tmp := ".sdk-" + randomID()
		out, e := artifact.OpenRegular(dest, tmp, syscall.O_WRONLY|syscall.O_CREAT|syscall.O_EXCL, 0600)
		if e != nil {
			in.Close()
			return e
		}
		_, e = io.Copy(out, in)
		e = errors.Join(e, in.Close(), out.Sync(), out.Close())
		if e != nil {
			return e
		}
		if e = artifact.Rename(dest, tmp, target); e != nil {
			return e
		}
	}
	return nil
}
