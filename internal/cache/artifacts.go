// SPDX-License-Identifier: GPL-3.0-or-later
package cache

import (
	"context"
	"errors"
	"github.com/s2script/s2script-cs2-egg/internal/artifact"
	"github.com/s2script/s2script-cs2-egg/internal/storeio"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type Options struct {
	Root     string
	Client   *http.Client
	Origins  []string
	MaxBytes int64
}
type Store struct {
	root string
	o    Options
}

func New(o Options) (*Store, error) {
	if !filepath.IsAbs(o.Root) || filepath.Clean(o.Root) != o.Root || o.Root == "/" {
		return nil, errors.New("absolute cache root required")
	}
	if o.MaxBytes == 0 {
		o.MaxBytes = 512 << 20
	}
	if o.MaxBytes < 1 || o.MaxBytes > 2<<30 {
		return nil, errors.New("invalid artifact bound")
	}
	if len(o.Origins) == 0 {
		o.Origins = []string{"github.com", "release-assets.githubusercontent.com", "objects.githubusercontent.com", "www.s2script.com"}
	}
	for _, p := range []string{o.Root, filepath.Join(o.Root, "objects"), filepath.Join(o.Root, "locks")} {
		if e := os.MkdirAll(p, 0700); e != nil {
			return nil, e
		}
		d, e := artifact.OpenDirectory(p)
		if e != nil {
			return nil, e
		}
		d.Close()
	}
	return &Store{root: o.Root, o: o}, nil
}
func (s *Store) trust(raw string) error {
	u, e := url.Parse(raw)
	if e != nil || u.Scheme != "https" || u.User != nil || u.Fragment != "" || u.Host == "" {
		return errors.New("invalid public artifact origin")
	}
	for _, h := range s.o.Origins {
		if strings.EqualFold(u.Host, h) {
			return nil
		}
	}
	return errors.New("artifact origin is not allowed")
}
func (s *Store) Get(ctx context.Context, raw, digest string) (string, error) {
	if !storeio.Digest(digest) {
		return "", errors.New("SHA256 required")
	}
	if e := s.trust(raw); e != nil {
		return "", e
	}
	lock := filepath.Join(s.root, "locks", digest)
	if e := os.MkdirAll(lock, 0700); e != nil {
		return "", e
	}
	unlock, e := storeio.Lock(ctx, lock)
	if e != nil {
		return "", e
	}
	defer unlock()
	all, e := storeio.Lock(ctx, s.root)
	if e != nil {
		return "", e
	}
	defer all()
	rel := "objects/" + digest
	path := filepath.Join(s.root, rel)
	hash, _, e := storeio.FileHash(s.root, rel, s.o.MaxBytes)
	if e == nil {
		if hash != digest {
			return "", errors.New("cached artifact digest mismatch")
		}
		return path, nil
	}
	if !errors.Is(e, os.ErrNotExist) {
		return "", e
	}
	c := http.Client{Timeout: 5 * time.Minute}
	if s.o.Client != nil {
		c = *s.o.Client
	}
	c.Jar = nil
	c.CheckRedirect = func(r *http.Request, via []*http.Request) error { return s.trust(r.URL.String()) }
	if e = artifact.Fetch(ctx, &c, raw, digest, path, s.o.MaxBytes); e != nil {
		return "", e
	}
	if e = os.Chmod(path, 0444); e != nil {
		return "", e
	}
	return path, nil
}
