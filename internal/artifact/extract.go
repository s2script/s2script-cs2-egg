// SPDX-License-Identifier: GPL-3.0-or-later
package artifact

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"errors"
	"io"
	"math"
	"os"
	"strings"
	"syscall"
)

// tarFormatAllowance bounds all tar framing, metadata, and padding in addition
// to the caller's expanded file-content budget.
const tarFormatAllowance int64 = 16 << 20

// Extract writes regular files/directories from ZIP or tar.gz to a disposable
// staging directory. It rejects links, duplicate files, and special entries.
// limit bounds total expanded file bytes. Tar streams additionally have a fixed
// 16 MiB allowance for headers, metadata, and padding. Discard stage on any error.
func Extract(archive, stage string, limit int64) error {
	if limit <= 0 {
		return errors.New("invalid extraction limit")
	}
	root, e := openDir(stage)
	if e != nil {
		return e
	}
	defer root.Close()
	remaining := limit
	count := 0
	write := func(name string, mode os.FileMode, size int64, r io.Reader) error {
		count++
		if count > 100000 {
			return errors.New("too many archive entries")
		}
		name = strings.TrimSuffix(name, "/")
		for strings.HasPrefix(name, "./") {
			name = strings.TrimPrefix(name, "./")
		}
		if name == "." && mode.IsDir() {
			return nil
		}
		if e := validPath(name); e != nil {
			return e
		}
		if mode.IsDir() {
			return ensureParent(root, name+"/placeholder")
		}
		if !mode.IsRegular() || size < 0 || size > remaining {
			return errors.New("invalid or oversized archive entry")
		}
		if e := ensureParent(root, name); e != nil {
			return e
		}
		f, e := openFile(root, name, syscall.O_WRONLY|syscall.O_CREAT|syscall.O_EXCL, mode.Perm()&0777)
		if e != nil {
			return e
		}
		n, e := io.Copy(f, io.LimitReader(r, size+1))
		if e == nil && n != size {
			e = errors.New("archive size mismatch")
		}
		if e == nil {
			e = f.Chmod(mode.Perm() & 0777)
		}
		if e == nil {
			e = f.Sync()
		}
		remaining -= n
		return errors.Join(e, f.Close())
	}
	if z, e := zip.OpenReader(archive); e == nil {
		defer z.Close()
		for _, f := range z.File {
			if f.UncompressedSize64 > uint64(remaining) {
				return errors.New("archive exceeds expanded limit")
			}
			r, e := f.Open()
			if e != nil {
				return e
			}
			e = write(f.Name, f.Mode(), int64(f.UncompressedSize64), r)
			e = errors.Join(e, r.Close())
			if e != nil {
				return e
			}
		}
		return nil
	}
	f, e := os.Open(archive)
	if e != nil {
		return e
	}
	defer f.Close()
	gz, e := gzip.NewReader(f)
	if e != nil {
		return errors.New("unsupported or invalid archive")
	}
	defer gz.Close()
	if limit > math.MaxInt64-tarFormatAllowance-1 {
		return errors.New("extraction limit is too large")
	}
	// Next consumes PAX/GNU records internally, so bound its complete input,
	// not only entries it returns. The extra byte detects budget exhaustion.
	stream := &io.LimitedReader{R: gz, N: limit + tarFormatAllowance + 1}
	tr := tar.NewReader(stream)
	for {
		h, e := tr.Next()
		if stream.N == 0 {
			return errors.New("tar stream exceeds expanded limit and format allowance")
		}
		if e == io.EOF {
			// Include trailing padding/concatenated gzip members in the same
			// budget and reach gzip EOF to validate its checksum.
			_, e = io.Copy(io.Discard, stream)
			if stream.N == 0 {
				return errors.New("tar stream exceeds expanded limit and format allowance")
			}
			return e
		}
		if e != nil {
			return e
		}
		if h.Typeflag != tar.TypeReg && h.Typeflag != tar.TypeRegA && h.Typeflag != tar.TypeDir {
			return errors.New("archive links and special entries are forbidden")
		}
		if e = write(h.Name, h.FileInfo().Mode(), h.Size, tr); e != nil {
			return e
		}
	}
}
