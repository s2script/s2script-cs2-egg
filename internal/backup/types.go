// SPDX-License-Identifier: GPL-3.0-or-later
// Package backup owns a fixed, private helper catalog. It never imports arbitrary archives.
package backup

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/s2script/s2script-cs2-egg/internal/artifact"
	"github.com/s2script/s2script-cs2-egg/internal/runtimelease"
	"github.com/s2script/s2script-cs2-egg/internal/storeio"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"
)

type Retention struct {
	Count   int   `json:"count"`
	AgeDays int   `json:"age_days"`
	Bytes   int64 `json:"bytes"`
}
type Policy struct {
	Root          string    `json:"root"`
	RootID        string    `json:"root_id"`
	ServerID      string    `json:"server_id"`
	Include       []string  `json:"include,omitempty"`
	Exclude       []string  `json:"exclude,omitempty"`
	MinFreeBytes  uint64    `json:"min_free_bytes"`
	MaxBytes      int64     `json:"max_bytes"`
	ArchiveBytes  int64     `json:"archive_bytes"`
	FileBytes     int64     `json:"file_bytes"`
	Files         int       `json:"files"`
	TotalBytes    int64     `json:"total_bytes"`
	RecoveryBytes int64     `json:"recovery_bytes"`
	Recoveries    int       `json:"recoveries"`
	Retention     Retention `json:"retention"`
}
type Options struct {
	OutputDirectory  string `json:"output_directory,omitempty"`
	Include, Exclude []string
	Retention        Retention
	MinFreeBytes     uint64
	Mode             string `json:"mode"`
}
type File struct {
	Path   string `json:"path"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
	Mode   uint32 `json:"mode"`
}
type Manifest struct {
	SchemaVersion              int `json:"schema_version"`
	BackupID, ServerID, RootID string
	CreatedAt                  time.Time
	Mode                       string
	Files                      []File
	Versions                   map[string]string
	ArchiveSHA256              string
	ArchiveBytes               int64
	Limitations                []string
	Absent                     []string `json:"absent,omitempty"`
}
type Identity struct {
	Present        bool
	Dev, Ino       uint64
	UID, GID, Mode uint32
	Size           int64
	MTime, CTime   int64
	Hash           string
}
type Target struct {
	Path    string
	Current Identity
}
type RestorePlan struct {
	ID, ServerID, RootID, BackupID, ArchiveSHA256, PlanDigest, RecoveryCopy string
	CreatedAt                                                               time.Time
	VolumeIdentity                                                          Identity
	Targets                                                                 []Target
	Parents                                                                 map[string]Identity
}
type Store struct {
	volume    string
	policy    Policy
	overrides []string
	versions  map[string]string
	free      func(string) (uint64, error)
}

func identifier(s string) bool {
	if len(s) < 1 || len(s) > 128 {
		return false
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_') {
			return false
		}
	}
	return true
}
func (p *Policy) Validate() error {
	if !filepath.IsAbs(p.Root) || filepath.Clean(p.Root) != p.Root || p.Root == "/" || !identifier(p.RootID) || !identifier(p.ServerID) {
		return errors.New("fixed backup root, root_id and server_id required")
	}
	if p.MinFreeBytes == 0 {
		p.MinFreeBytes = 4 << 30
	}
	if p.MinFreeBytes < 64<<20 || p.MinFreeBytes > 16<<30 {
		return errors.New("backup reserve must be 64 MiB..16 GiB")
	}
	for _, b := range []struct {
		p *int64
		d int64
	}{{&p.MaxBytes, 32 << 30}, {&p.ArchiveBytes, 16 << 30}, {&p.FileBytes, 8 << 30}, {&p.TotalBytes, 160 << 30}, {&p.RecoveryBytes, 64 << 30}, {&p.Retention.Bytes, 64 << 30}} {
		if *b.p == 0 {
			*b.p = b.d
		}
		if *b.p < 1 || *b.p > 4*b.d {
			return errors.New("backup byte bound invalid")
		}
	}
	for _, b := range []struct {
		p *int
		d int
	}{{&p.Files, 32768}, {&p.Recoveries, 5}, {&p.Retention.Count, 7}, {&p.Retention.AgeDays, 30}} {
		if *b.p == 0 {
			*b.p = b.d
		}
		if *b.p < 1 || *b.p > 4*b.d {
			return errors.New("backup count bound invalid")
		}
	}
	if p.FileBytes > p.MaxBytes || p.ArchiveBytes > p.TotalBytes || p.RecoveryBytes > p.TotalBytes || p.Retention.Bytes > p.TotalBytes {
		return errors.New("inconsistent backup limits")
	}
	if len(p.Include) > 128 || len(p.Exclude) > 128 {
		return errors.New("backup selection bound")
	}
	for _, v := range append(append([]string{}, p.Include...), p.Exclude...) {
		if !relative(v) {
			return errors.New("invalid backup selection")
		}
		if _, e := filepath.Match(v, ""); e != nil {
			return e
		}
	}
	return nil
}
func relative(p string) bool {
	return p != "" && p != "." && !filepath.IsAbs(p) && filepath.Clean(p) == p && !strings.HasPrefix(p, "../") && !strings.ContainsAny(p, "\\:\x00") && len(p) <= 2048
}
func reserved(p string) bool {
	for _, c := range strings.Split(p, "/") {
		if strings.HasPrefix(c, ".") || c == "cache" || c == "logs" || c == "crashes" || c == "dumps" || c == "backups" || c == "recovery" || strings.HasSuffix(c, ".sock") {
			return true
		}
	}
	return false
}
func matches(patterns []string, p string) bool {
	for _, v := range patterns {
		if p == v || strings.HasPrefix(p, v+"/") {
			return true
		}
		if ok, _ := filepath.Match(v, p); ok {
			return true
		}
	}
	return false
}
func (s *Store) allowed(p string) bool {
	if !relative(p) || reserved(p) {
		return false
	}
	defaults := []string{"egg", "s2script-plugins.json", "s2script-plugins.lock.json", "game/csgo/gameinfo.gi", "game/csgo/cfg", "game/csgo/addons", "game/csgo/data", "game/csgo/custom"}
	return (matches(defaults, p) || matches(s.policy.Include, p) || matches(s.overrides, p)) && !matches(s.policy.Exclude, p)
}
func (s *Store) descends(p string) bool {
	if reserved(p) {
		return false
	}
	if s.allowed(p) {
		return true
	}
	for _, v := range append([]string{"egg", "s2script-plugins.json", "s2script-plugins.lock.json", "game/csgo/gameinfo.gi", "game/csgo/cfg", "game/csgo/addons", "game/csgo/data", "game/csgo/custom"}, append(append([]string{}, s.policy.Include...), s.overrides...)...) {
		prefix := strings.Split(v, "*")[0]
		if strings.HasPrefix(prefix, p+"/") {
			return true
		}
	}
	return false
}
func diskFree(p string) (uint64, error) {
	var st syscall.Statfs_t
	e := syscall.Statfs(p, &st)
	if e != nil {
		return 0, e
	}
	return st.Bavail * uint64(st.Bsize), nil
}
func Open(volume string, p Policy) (*Store, error) {
	if e := p.Validate(); e != nil {
		return nil, e
	}
	if !filepath.IsAbs(volume) || filepath.Clean(volume) != volume || volume == p.Root || strings.HasPrefix(p.Root, volume+"/") || strings.HasPrefix(volume, p.Root+"/") {
		return nil, errors.New("backup root must be outside volume")
	}
	d, e := artifact.OpenDirectory(p.Root)
	if e != nil {
		return nil, e
	}
	info, e := d.Stat()
	d.Close()
	if e != nil {
		return nil, e
	}
	if info.Mode().Perm() != 0700 || info.Sys().(*syscall.Stat_t).Uid != uint32(os.Geteuid()) {
		return nil, errors.New("backup root must be helper-owned0700")
	}
	v, e := artifact.OpenDirectory(volume)
	if e != nil {
		return nil, e
	}
	vi, e := v.Stat()
	v.Close()
	if e != nil {
		return nil, e
	}
	st := vi.Sys().(*syscall.Stat_t)
	if st.Uid != uint32(os.Geteuid()) {
		return nil, errors.New("volume must be helper-owned")
	}
	s := &Store{volume: volume, policy: p, free: diskFree}
	unlock, e := storeio.Lock(context.Background(), p.Root)
	if e != nil {
		return nil, e
	}
	defer unlock()
	identity := struct {
		RootID, ServerID string
		Dev, Ino         uint64
	}{p.RootID, p.ServerID, uint64(st.Dev), st.Ino}
	var old = identity
	e = readJSON(p.Root, "identity.json", &old, 4096)
	if errors.Is(e, os.ErrNotExist) {
		e = storeio.Save(p.Root, "identity.json", identity)
	} else if e == nil && old != identity {
		e = errors.New("backup root belongs to another server or volume")
	}
	if e != nil {
		return nil, e
	}
	for _, n := range []string{"archives", "plans", "receipts"} {
		if e = os.Mkdir(filepath.Join(p.Root, n), 0700); e != nil && !errors.Is(e, os.ErrExist) {
			return nil, e
		}
		d, e := artifact.OpenDirectory(filepath.Join(p.Root, n))
		if e != nil {
			return nil, e
		}
		i, e := d.Stat()
		d.Close()
		if e != nil || i.Mode().Perm() != 0700 || i.Sys().(*syscall.Stat_t).Uid != uint32(os.Geteuid()) {
			return nil, errors.New("invalid backup catalog directory")
		}
	}
	if e = s.facts(); e != nil {
		return nil, e
	}
	return s, nil
}
func readJSON(root, p string, v any, limit int64) error {
	b, e := storeio.Read(root, p, limit)
	if e != nil {
		return e
	}
	d := json.NewDecoder(strings.NewReader(string(b)))
	d.DisallowUnknownFields()
	if e = d.Decode(v); e != nil {
		return e
	}
	if d.Decode(new(any)) != io.EOF {
		return errors.New("trailing metadata")
	}
	return nil
}
func (s *Store) locks(ctx context.Context, live bool) (func(), error) {
	var release func() error
	var e error
	if !live {
		release, e = runtimelease.Acquire(s.volume)
		if e != nil {
			return nil, e
		}
	}
	unlock, e := artifact.Acquire(s.volume)
	if e != nil {
		if release != nil {
			release()
		}
		return nil, e
	}
	done := func() {
		unlock()
		if release != nil {
			release()
		}
	}
	if live {
		for _, p := range []string{".artifact-txn", ".s2shared-pending.json"} {
			if _, e = os.Lstat(filepath.Join(s.volume, p)); !errors.Is(e, os.ErrNotExist) {
				done()
				return nil, errors.New("live backup refuses pending mutation")
			}
		}
	} else if e = artifact.Recover(s.volume); e != nil {
		done()
		return nil, e
	}
	cat, e := storeio.Lock(ctx, s.policy.Root)
	if e != nil {
		done()
		return nil, e
	}
	if e = s.facts(); e != nil {
		cat()
		done()
		return nil, e
	}
	if e = s.cleanStages(); e != nil {
		cat()
		done()
		return nil, e
	}
	return func() { cat(); done() }, nil
}
func (s *Store) budget(extra int64, volumeExtra int64) error {
	used, e := treeSize(s.policy.Root)
	if e != nil {
		return e
	}
	if extra < 0 || used > s.policy.TotalBytes-extra {
		return errors.New("backup total storage quota")
	}
	rootNeed, volumeNeed := extra, volumeExtra
	a, ae := os.Stat(s.policy.Root)
	b, be := os.Stat(s.volume)
	if ae != nil || be != nil {
		return errors.New("cannot identify backup filesystems")
	}
	if a.Sys().(*syscall.Stat_t).Dev == b.Sys().(*syscall.Stat_t).Dev {
		rootNeed += volumeExtra
		volumeNeed = rootNeed
	}
	for _, v := range []struct {
		p string
		n int64
	}{{s.policy.Root, rootNeed}, {s.volume, volumeNeed}} {
		if v.n < 0 {
			return errors.New("invalid disk budget")
		}
		n, e := s.free(v.p)
		if e != nil {
			return e
		}
		need := uint64(v.n) + s.policy.MinFreeBytes
		if n < need {
			return errors.New("insufficient free disk reserve")
		}
	}
	return nil
}
func treeSize(root string) (int64, error) {
	var n int64
	count := 0
	e := filepath.WalkDir(root, func(p string, d os.DirEntry, e error) error {
		if e != nil {
			return e
		}
		count++
		if count > 300000 {
			return errors.New("catalog entry quota")
		}
		i, e := d.Info()
		if e != nil {
			return e
		}
		if i.Mode()&os.ModeSymlink != 0 || !i.IsDir() && !i.Mode().IsRegular() {
			return errors.New("unexpected catalog entry")
		}
		if !i.IsDir() {
			if i.Size() < 0 || i.Size() > 1<<50 || n > 1<<50-i.Size() || i.Sys().(*syscall.Stat_t).Nlink != 1 {
				return errors.New("catalog size/link bound")
			}
			n += i.Size()
		}
		return nil
	})
	return n, e
}
func statIdentity(i os.FileInfo) Identity {
	st := i.Sys().(*syscall.Stat_t)
	return Identity{Present: true, Dev: uint64(st.Dev), Ino: st.Ino, UID: st.Uid, GID: st.Gid, Mode: uint32(i.Mode()), Size: i.Size(), MTime: st.Mtim.Sec*1e9 + st.Mtim.Nsec, CTime: st.Ctim.Sec*1e9 + st.Ctim.Nsec}
}
func (s *Store) manifest(id string) (Manifest, error) {
	var m Manifest
	if !identifier(id) {
		return m, errors.New("invalid catalog ID")
	}
	e := readJSON(s.policy.Root, "archives/"+id+"/manifest.json", &m, 16<<20)
	if e != nil {
		return m, e
	}
	if m.SchemaVersion != 1 || m.BackupID != id || m.ServerID != s.policy.ServerID || m.RootID != s.policy.RootID || !storeio.Digest(m.ArchiveSHA256) || m.ArchiveBytes < 0 || m.ArchiveBytes > s.policy.ArchiveBytes || len(m.Files) > s.policy.Files {
		return m, errors.New("invalid scoped manifest")
	}
	seen := map[string]bool{}
	var total int64
	for _, f := range m.Files {
		if !s.allowed(f.Path) || seen[f.Path] || f.Size < 0 || f.Size > s.policy.FileBytes || !storeio.Digest(f.SHA256) || f.Mode&^0777 != 0 {
			return m, errors.New("invalid manifest file")
		}
		seen[f.Path] = true
		total += f.Size
		if total > s.policy.MaxBytes {
			return m, errors.New("expanded archive quota")
		}
	}
	paths := make([]string, 0, len(seen))
	for p := range seen {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	for i := 1; i < len(paths); i++ {
		if strings.HasPrefix(paths[i], paths[i-1]+"/") {
			return m, errors.New("overlapping manifest")
		}
	}
	return m, nil
}
func syncDir(p string) error {
	f, e := artifact.OpenDirectory(p)
	if e != nil {
		return e
	}
	return errors.Join(f.Sync(), f.Close())
}
