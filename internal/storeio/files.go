// SPDX-License-Identifier: GPL-3.0-or-later
// Package storeio provides bounded, durable metadata for node-owned stores.
package storeio

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/s2script/s2script-cs2-egg/internal/artifact"
	"io"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

func ID() string {
	var b [16]byte
	if _, e := rand.Read(b[:]); e != nil {
		panic(e)
	}
	return hex.EncodeToString(b[:])
}
func Hash(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }
func Digest(s string) bool {
	b, e := hex.DecodeString(s)
	return e == nil && len(b) == 32 && hex.EncodeToString(b) == s
}
func Read(root, path string, limit int64) ([]byte, error) {
	d, e := artifact.OpenDirectory(root)
	if e != nil {
		return nil, e
	}
	defer d.Close()
	f, e := artifact.OpenRegular(d, path, syscall.O_RDONLY, 0)
	if e != nil {
		return nil, e
	}
	defer f.Close()
	b, e := io.ReadAll(io.LimitReader(f, limit+1))
	if int64(len(b)) > limit {
		return nil, errors.New("store data exceeds bound")
	}
	return b, e
}
func JSON(root, path string, v any) error {
	b, e := Read(root, path, 16<<20)
	if e != nil {
		return e
	}
	return json.Unmarshal(b, v)
}
func Write(root, path string, b []byte, mode os.FileMode) error {
	d, e := artifact.OpenDirectory(root)
	if e != nil {
		return e
	}
	defer d.Close()
	p, _, e := artifact.ParentDirectory(d, path, true)
	if e != nil {
		return e
	}
	p.Close()
	var old *syscall.Stat_t
	if previous, openErr := artifact.OpenRegular(d, path, syscall.O_RDONLY, 0); openErr == nil {
		info, statErr := previous.Stat()
		previous.Close()
		if statErr != nil {
			return statErr
		}
		st := *info.Sys().(*syscall.Stat_t)
		old = &st
	} else if !errors.Is(openErr, os.ErrNotExist) {
		return openErr
	}
	tmp := filepath.ToSlash(filepath.Join(filepath.Dir(path), ".tmp-"+ID()))
	f, e := artifact.OpenRegular(d, tmp, syscall.O_WRONLY|syscall.O_CREAT|syscall.O_EXCL, mode)
	if e != nil {
		return e
	}
	defer artifact.Remove(d, tmp)
	if old != nil {
		if e = f.Chown(int(old.Uid), int(old.Gid)); e != nil {
			f.Close()
			return e
		}
	}
	_, e = f.Write(b)
	if e == nil {
		e = f.Sync()
	}
	e = errors.Join(e, f.Close())
	if e != nil {
		return e
	}
	return artifact.Rename(d, tmp, path)
}
func Save(root, path string, v any) error {
	b, e := json.Marshal(v)
	if e != nil {
		return e
	}
	if len(b) > 16<<20 {
		return errors.New("metadata quota exceeded")
	}
	return Write(root, path, b, 0600)
}
func Lock(ctx context.Context, root string) (func(), error) {
	d, e := artifact.OpenDirectory(root)
	if e != nil {
		return nil, e
	}
	defer d.Close()
	f, e := artifact.OpenRegular(d, ".store-lock", syscall.O_RDWR|syscall.O_CREAT, 0600)
	if e != nil {
		return nil, e
	}
	for {
		e = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if e == nil {
			return func() { f.Close() }, nil
		}
		if e != syscall.EWOULDBLOCK && e != syscall.EAGAIN {
			f.Close()
			return nil, e
		}
		select {
		case <-ctx.Done():
			f.Close()
			return nil, ctx.Err()
		case <-time.After(10 * time.Millisecond):
		}
	}
}
func FileHash(root, path string, limit int64) (string, int64, error) {
	d, e := artifact.OpenDirectory(root)
	if e != nil {
		return "", 0, e
	}
	defer d.Close()
	f, e := artifact.OpenRegular(d, path, syscall.O_RDONLY, 0)
	if e != nil {
		return "", 0, e
	}
	defer f.Close()
	h := sha256.New()
	n, e := io.Copy(h, io.LimitReader(f, limit+1))
	if n > limit {
		return "", n, errors.New("file exceeds limit")
	}
	return hex.EncodeToString(h.Sum(nil)), n, e
}
