// SPDX-License-Identifier: GPL-3.0-or-later
package backup

import (
	"archive/tar"
	"bufio"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"github.com/s2script/s2script-cs2-egg/internal/artifact"
	"github.com/s2script/s2script-cs2-egg/internal/storeio"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
)

type selected struct {
	path     string
	identity Identity
}

func (s *Store) selectFiles(o Options) ([]selected, error) {
	root, e := artifact.OpenDirectory(s.volume)
	if e != nil {
		return nil, e
	}
	defer root.Close()
	var out []selected
	var bytes int64
	visited := 0
	var walk func(*os.File, string) error
	walk = func(d *os.File, prefix string) error {
		for {
			entries, e := d.ReadDir(128)
			if e != nil && e != io.EOF {
				return e
			}
			for _, v := range entries {
				visited++
				if strings.Count(prefix, "/") > 64 {
					return errors.New("backup path depth bound")
				}
				if visited > 300000 {
					return errors.New("selection scan quota")
				}
				p := strings.TrimPrefix(prefix+"/"+v.Name(), "/")
				if !s.descends(p) {
					continue
				}
				i, e := os.Lstat(fmt.Sprintf("/proc/self/fd/%d/%s", d.Fd(), v.Name()))
				if e != nil {
					return e
				}
				if i.IsDir() {
					child, _, e := artifact.ParentDirectory(root, p+"/placeholder", false)
					if e != nil {
						return e
					}
					e = walk(child, p)
					child.Close()
					if e != nil {
						return e
					}
					continue
				}
				if !s.allowed(p) || len(o.Include) > 0 && !matches(o.Include, p) || matches(o.Exclude, p) {
					continue
				}
				if !i.Mode().IsRegular() || i.Sys().(*syscall.Stat_t).Nlink != 1 {
					return errors.New("selected private file must be a regular single-link file")
				}
				if i.Size() > s.policy.FileBytes || i.Size() < 0 {
					return errors.New("backup file size quota")
				}
				bytes += i.Size()
				if bytes > s.policy.MaxBytes || len(out) >= s.policy.Files {
					return errors.New("backup selection quota")
				}
				out = append(out, selected{p, statIdentity(i)})
			}
			if e == io.EOF {
				return nil
			}
		}
	}
	e = walk(root, "")
	sort.Slice(out, func(i, j int) bool { return out[i].path < out[j].path })
	return out, e
}
func (s *Store) Create(ctx context.Context, o Options) (Manifest, error) {
	var zero Manifest
	if o.OutputDirectory != "" && o.OutputDirectory != s.policy.Root {
		return zero, errors.New("output must be the fixed helper catalog")
	}
	if o.Mode == "" {
		o.Mode = "consistent"
	}
	if o.Mode != "consistent" && o.Mode != "live-best-effort" {
		return zero, errors.New("invalid backup mode")
	}
	if o.MinFreeBytes != 0 && o.MinFreeBytes != s.policy.MinFreeBytes || o.Retention != (Retention{}) && o.Retention != s.policy.Retention {
		return zero, errors.New("retention/reserve changes require helper policy")
	}
	if len(o.Include) > 128 || len(o.Exclude) > 128 {
		return zero, errors.New("selection bound")
	}
	for _, v := range append(append([]string{}, o.Include...), o.Exclude...) {
		if !relative(v) {
			return zero, errors.New("invalid selection")
		}
	}
	done, e := s.locks(ctx, o.Mode == "live-best-effort")
	if e != nil {
		return zero, e
	}
	defer done()
	files, e := s.selectFiles(o)
	if e != nil {
		return zero, e
	}
	if len(files) == 0 {
		return zero, errors.New("no selected private files")
	}
	m, e := s.seal(ctx, "backup-"+storeio.ID(), o.Mode, files, nil)
	if e != nil {
		return zero, e
	}
	if e = s.prune(m.BackupID); e != nil {
		return m, e
	}
	return m, nil
}

type cappedWriter struct {
	ctx   context.Context
	w     io.Writer
	left  int64
	check func() error
	n     int64
}

func (w *cappedWriter) Write(b []byte) (int, error) {
	if e := w.ctx.Err(); e != nil {
		return 0, e
	}
	if int64(len(b)) > w.left {
		return 0, errors.New("archive storage quota")
	}
	if w.n == 0 || w.n/(32<<20) != (w.n+int64(len(b)))/(32<<20) {
		if e := w.check(); e != nil {
			return 0, e
		}
	}
	n, e := w.w.Write(b)
	w.left -= int64(n)
	w.n += int64(n)
	return n, e
}
func (s *Store) seal(ctx context.Context, id, mode string, files []selected, absent []string) (Manifest, error) {
	m := Manifest{SchemaVersion: 1, BackupID: id, ServerID: s.policy.ServerID, RootID: s.policy.RootID, CreatedAt: time.Now().UTC(), Mode: mode, Versions: s.versions, Absent: absent}
	if mode == "live-best-effort" {
		m.Limitations = []string{"Live files checked individually; not an application-consistent database snapshot."}
	}
	var estimate int64 = 1 << 20
	for _, f := range files {
		estimate += f.identity.Size + 8192
	}
	estimate += estimate / 100
	estimate = min(estimate, s.policy.ArchiveBytes)
	if e := s.budget(estimate, 0); e != nil {
		return m, e
	}
	stage, e := s.newStage(".stage-")
	if e != nil {
		return m, e
	}
	defer os.RemoveAll(stage)
	out, e := os.OpenFile(filepath.Join(stage, "archive.tar.gz"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if e != nil {
		return m, e
	}
	h := sha256.New()
	w := &cappedWriter{ctx: ctx, w: io.MultiWriter(out, h), left: min(estimate, s.policy.ArchiveBytes), check: func() error { return s.budget(0, 0) }}
	gz := gzip.NewWriter(w)
	tw := tar.NewWriter(gz)
	root, e := artifact.OpenDirectory(s.volume)
	if e != nil {
		out.Close()
		return m, e
	}
	defer root.Close()
	for _, v := range files {
		if e = ctx.Err(); e != nil {
			break
		}
		f, er := artifact.OpenRegular(root, v.path, syscall.O_RDONLY, 0)
		if er != nil {
			e = er
			break
		}
		i, er := f.Stat()
		if er != nil || statIdentity(i) != v.identity {
			f.Close()
			e = errors.New("selected file changed before archive")
			break
		}
		if er = tw.WriteHeader(&tar.Header{Name: v.path, Mode: 0600, Size: i.Size(), Typeflag: tar.TypeReg, Format: tar.FormatPAX}); er != nil {
			f.Close()
			e = er
			break
		}
		fh := sha256.New()
		n, er := io.CopyN(io.MultiWriter(tw, fh), f, i.Size())
		after, statErr := f.Stat()
		f.Close()
		if er != nil || statErr != nil || n != i.Size() || statIdentity(after) != v.identity {
			e = errors.New("selected file changed during archive")
			break
		}
		m.Files = append(m.Files, File{v.path, n, hex.EncodeToString(fh.Sum(nil)), uint32(i.Mode().Perm())})
	}
	e = errors.Join(e, tw.Close(), gz.Close())
	if e == nil {
		e = out.Sync()
	}
	e = errors.Join(e, out.Close())
	if e != nil {
		return m, e
	}
	m.ArchiveSHA256 = hex.EncodeToString(h.Sum(nil))
	m.ArchiveBytes = w.n
	if strings.HasPrefix(id, "backup-") && m.ArchiveBytes > s.policy.Retention.Bytes {
		return m, errors.New("backup exceeds retention byte budget; previous backups retained")
	}
	if e = storeio.Save(stage, "manifest.json", m); e != nil {
		return m, e
	}
	if e = syncDir(stage); e != nil {
		return m, e
	}
	dest := filepath.Join(s.policy.Root, "archives", id)
	if _, e = os.Lstat(dest); !errors.Is(e, os.ErrNotExist) {
		return m, errors.New("catalog ID already exists")
	}
	if e = os.Rename(stage, dest); e != nil {
		return m, e
	}
	e = syncDir(filepath.Dir(dest))
	return m, e
}

// verify fully checks bytes and members before any volume mutation. Extraction,
// when requested, writes only private flat numbered files in a new staging root.
func (s *Store) verify(ctx context.Context, m Manifest, stage string) ([]artifact.Change, error) {
	root := filepath.Join(s.policy.Root, "archives", m.BackupID)
	d, e := artifact.OpenDirectory(root)
	if e != nil {
		return nil, e
	}
	defer d.Close()
	f, e := artifact.OpenRegular(d, "archive.tar.gz", syscall.O_RDONLY, 0)
	if e != nil {
		return nil, e
	}
	defer f.Close()
	before, e := f.Stat()
	if e != nil {
		return nil, e
	}
	compressedHash := sha256.New()
	compressed := &countReader{r: io.TeeReader(io.LimitReader(f, s.policy.ArchiveBytes+1), compressedHash)}
	br := bufio.NewReader(compressed)
	gz, e := gzip.NewReader(br)
	if e != nil {
		return nil, e
	}
	defer gz.Close()
	gz.Multistream(false)
	limited := &io.LimitedReader{R: gz, N: s.policy.MaxBytes + int64(s.policy.Files)*8192 + 1024}
	tr := tar.NewReader(limited)
	var changes []artifact.Change
	for i, want := range m.Files {
		if e = ctx.Err(); e != nil {
			return nil, e
		}
		hd, e := tr.Next()
		if e != nil {
			return nil, e
		}
		if hd.Typeflag != tar.TypeReg || hd.Name != want.Path || hd.Size != want.Size || hd.Linkname != "" || len(hd.PAXRecords) > 2 {
			return nil, errors.New("archive/manifest members differ")
		}
		var dest *os.File
		writer := io.Writer(io.Discard)
		source := ""
		if stage != "" {
			source = filepath.Join(stage, failName(i))
			dest, e = os.OpenFile(source, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
			if e != nil {
				return nil, e
			}
			writer = &cappedWriter{ctx: ctx, w: dest, left: want.Size, check: func() error { return s.budget(0, 0) }}
		}
		fh := sha256.New()
		n, e := io.Copy(io.MultiWriter(writer, fh), tr)
		if dest != nil {
			if e == nil {
				e = dest.Sync()
			}
			e = errors.Join(e, dest.Close())
		}
		if e != nil {
			return nil, e
		}
		if n != want.Size || hex.EncodeToString(fh.Sum(nil)) != want.SHA256 {
			return nil, errors.New("archive member checksum mismatch")
		}
		changes = append(changes, artifact.Change{Path: want.Path, Source: source, PreserveMetadata: true})
	}
	if _, e = tr.Next(); e != io.EOF {
		return nil, errors.New("unexpected archive member")
	}
	b, e := io.ReadAll(io.LimitReader(limited, 1025))
	if e != nil || len(b) != 0 || limited.N <= 0 {
		return nil, errors.New("trailing expanded archive data")
	}
	if _, e = br.ReadByte(); e != io.EOF {
		return nil, errors.New("trailing compressed archive data")
	}
	if compressed.n != m.ArchiveBytes || hex.EncodeToString(compressedHash.Sum(nil)) != m.ArchiveSHA256 {
		return nil, errors.New("archive checksum mismatch")
	}
	after, e := f.Stat()
	if e != nil || statIdentity(before) != statIdentity(after) {
		return nil, errors.New("archive changed during verification")
	}
	return changes, nil
}
func failName(i int) string { return strconv.Itoa(i) }

type countReader struct {
	r io.Reader
	n int64
}

func (r *countReader) Read(b []byte) (int, error) { n, e := r.r.Read(b); r.n += int64(n); return n, e }
