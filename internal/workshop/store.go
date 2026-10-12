// SPDX-License-Identifier: GPL-3.0-or-later
package workshop

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/s2script/s2script-cs2-egg/internal/artifact"
	"github.com/s2script/s2script-cs2-egg/internal/nodejobs"
	node "github.com/s2script/s2script-cs2-egg/internal/nodetypes"
	"github.com/s2script/s2script-cs2-egg/internal/storeio"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const MaxItemBytes int64 = 32 << 30
const MaxFiles = 512
const ChunkSize = 256 << 10

type File struct {
	Name   string `json:"name"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}
type Generation struct {
	AppID    int    `json:"app_id"`
	ItemID   string `json:"item_id"`
	Revision string `json:"revision"`
	Importer string `json:"importer"`
	Digest   string `json:"digest"`
	Updated  int64  `json:"updated"`
	Files    []File `json:"files"`
}

func (g Generation) Key() string { return g.ItemID + "/" + g.Revision }

type Outcome struct{ ItemID, Revision, Reason string }
type Result struct {
	Imported, Linked, Released, Skipped []Outcome
	BytesSaved                          int64
}
type Options struct {
	Root             string
	TrustedImporters []string
	Servers          []node.Server
	Items            []string
	Mode, Mount      string
	Details          func(context.Context, []string) (map[string]Detail, error)
}
type Store struct {
	root string
	o    Options
	jobs *nodejobs.Pool
}
type owner struct{ Server, Token, Request string }
type reference struct{ Server, Key, Nonce string }
type state struct {
	Version    int
	Owners     map[string]owner
	References map[string]reference
	Known      map[string]bool
}

func New(o Options) (*Store, error) {
	if !filepath.IsAbs(o.Root) || o.Root == "/" || filepath.Clean(o.Root) != o.Root {
		return nil, errors.New("fixed absolute workshop root required")
	}
	if len(o.Items) > 100 {
		return nil, errors.New("selected workshop item bound")
	}
	for _, id := range o.Items {
		if !number(id) {
			return nil, errors.New("invalid selected item")
		}
	}
	for _, id := range o.TrustedImporters {
		found := false
		for _, s := range o.Servers {
			found = found || s.ID == id
		}
		if !found {
			return nil, errors.New("trusted importer must be registered")
		}
	}
	if o.Details == nil {
		o.Details = SteamDetails
	}
	if o.Mode == "" {
		o.Mode = "copy"
	}
	if o.Mode != "copy" && o.Mode != "symlink" && o.Mode != "hardlink" {
		return nil, errors.New("invalid workshop delivery mode")
	}
	if o.Mount == "" {
		o.Mount = "/mnt/s2workshop"
	}
	for _, p := range []string{o.Root, filepath.Join(o.Root, "generations"), filepath.Join(o.Root, "uploads")} {
		mode := os.FileMode(0700)
		if p == o.Root || p == filepath.Join(o.Root, "generations") {
			mode = 0755
		}
		if e := os.MkdirAll(p, mode); e != nil {
			return nil, e
		}
		d, e := artifact.OpenDirectory(p)
		if e != nil {
			return nil, e
		}
		d.Close()
	}
	s := &Store{root: o.Root, o: o}
	var e error
	s.jobs, e = nodejobs.New(filepath.Join(o.Root, "jobs"), s.job)
	return s, e
}
func (s *Store) load() (state, error) {
	v := state{Version: 1, Owners: map[string]owner{}, References: map[string]reference{}, Known: map[string]bool{}}
	e := storeio.JSON(s.root, "state.json", &v)
	if errors.Is(e, os.ErrNotExist) {
		e = nil
	}
	if v.Version != 1 || v.Owners == nil || v.References == nil || v.Known == nil || len(v.Owners) > 4096 || len(v.References) > 8192 {
		return v, errors.New("invalid workshop state")
	}
	for k, o := range v.Owners {
		if !keyOK(k) || o.Server == "" || len(o.Token) != 32 || !storeio.Digest(o.Token+o.Token) {
			return v, errors.New("invalid workshop owner")
		}
	}
	for k, r := range v.References {
		if k == "" || !keyOK(r.Key) || r.Server == "" || len(r.Nonce) != 32 {
			return v, errors.New("invalid workshop reference")
		}
	}
	return v, e
}
func keyOK(k string) bool {
	p := strings.Split(k, "/")
	return len(p) == 2 && number(p[0]) && number(p[1])
}
func (s *Store) trusted(server string) bool {
	for _, id := range s.o.TrustedImporters {
		if id == server {
			return true
		}
	}
	return false
}
func (s *Store) selected(id string) bool {
	for _, x := range s.o.Items {
		if x == id {
			return true
		}
	}
	return false
}
func (s *Store) Begin(ctx context.Context, server, id, rev, request string) (string, error) {
	if !s.trusted(server) || !s.selected(id) {
		return "", errors.New("server is not an approved importer for selected item")
	}
	if !number(id) || !number(rev) || request == "" || len(request) > 128 {
		return "", errors.New("invalid import identity")
	}
	u, e := storeio.Lock(ctx, s.root)
	if e != nil {
		return "", e
	}
	defer u()
	v, e := s.load()
	if e != nil {
		return "", e
	}
	key := id + "/" + rev
	if o, ok := v.Owners[key]; ok {
		if o.Server != server {
			return "", errors.New("pending: another importer owns revision")
		}
		return o.Token, nil
	}
	if len(v.Owners) >= 4096 {
		return "", errors.New("owner quota reached")
	}
	o := owner{server, storeio.ID(), request}
	v.Owners[key] = o
	if e = storeio.Save(s.root, "state.json", v); e != nil {
		return "", e
	}
	return o.Token, nil
}
func safeFile(name string) bool {
	return name != "" && len(name) <= 255 && name != "." && name != ".." && !strings.ContainsAny(name, "/\\\x00") && name != "manifest.json"
}
func manifestDigest(g Generation) string { b, _ := json.Marshal(g.Files); return storeio.Hash(b) }
func (s *Store) Generation(id, rev string) (Generation, error) {
	var g Generation
	if !number(id) || !number(rev) {
		return g, errors.New("invalid generation identity")
	}
	e := storeio.JSON(s.root, "generations/"+id+"/"+rev+"/manifest.json", &g)
	if e != nil {
		return g, e
	}
	if g.AppID != 730 || g.ItemID != id || g.Revision != rev || g.Digest != manifestDigest(g) || len(g.Files) == 0 || len(g.Files) > MaxFiles {
		return g, errors.New("invalid generation manifest")
	}
	seen := map[string]bool{}
	var n int64
	for _, f := range g.Files {
		if !safeFile(f.Name) || seen[f.Name] || f.Size < 0 || !storeio.Digest(f.SHA256) {
			return g, errors.New("invalid generation file")
		}
		seen[f.Name] = true
		n += f.Size
		if n > MaxItemBytes {
			return g, errors.New("item exceeds bound")
		}
	}
	return g, nil
}
func (s *Store) pin(v *state, server, key string) error {
	if len(v.References) >= 8192 {
		if _, ok := v.References[server+":"+key]; !ok {
			return errors.New("reference quota")
		}
	}
	v.References[server+":"+key] = reference{server, key, storeio.ID()}
	// A pin cannot clear an explicitly unknown stopped-helper inventory. Only
	// a later complete helper reconstruction may authorize reference retirement.
	if _, observed := v.Known[server]; !observed {
		v.Known[server] = true
	}
	return nil
}
func (s *Store) Reserve(ctx context.Context, server, id, rev string) (Generation, error) {
	u, e := storeio.Lock(ctx, s.root)
	if e != nil {
		return Generation{}, e
	}
	defer u()
	g, e := s.Generation(id, rev)
	if e != nil {
		return g, e
	}
	v, e := s.load()
	if e == nil {
		e = s.pin(&v, server, g.Key())
	}
	if e == nil {
		e = storeio.Save(s.root, "state.json", v)
	}
	return g, e
}

// Import is a bounded embedded-fixture convenience. Production uses staged streaming.
func (s *Store) Import(ctx context.Context, server, id, rev string, files map[string][]byte) (Generation, error) {
	token, e := s.Begin(ctx, server, id, rev, storeio.ID())
	if e != nil {
		return Generation{}, e
	}
	stage, e := os.MkdirTemp(filepath.Join(s.root, "uploads"), "import-")
	if e != nil {
		return Generation{}, e
	}
	defer os.RemoveAll(stage)
	for n, b := range files {
		if !safeFile(n) {
			return Generation{}, errors.New("invalid item filename")
		}
		if e = storeio.Write(stage, n, b, 0600); e != nil {
			return Generation{}, e
		}
	}
	return s.publish(ctx, server, id, rev, token, stage)
}
func (s *Store) publish(ctx context.Context, server, id, rev, token, stage string) (Generation, error) {
	var g Generation
	if !s.trusted(server) {
		return g, errors.New("import authorization revoked")
	}
	details, e := s.o.Details(ctx, []string{id})
	if e != nil {
		return g, e
	}
	d, ok := details[id]
	if !ok || d.ID != id || d.AppID != 730 || d.Revision != rev {
		return g, errors.New("Steam revision identity mismatch")
	}
	entries, e := os.ReadDir(stage)
	if e != nil {
		return g, e
	}
	g = Generation{AppID: 730, ItemID: id, Revision: rev, Importer: server, Updated: d.Updated}
	var total int64
	for _, entry := range entries {
		if !safeFile(entry.Name()) || !entry.Type().IsRegular() {
			return g, errors.New("item contains nonregular/unsafe file")
		}
		hash, n, e := storeio.FileHash(stage, entry.Name(), MaxItemBytes)
		if e != nil {
			return g, e
		}
		total += n
		if total > MaxItemBytes || len(g.Files) >= MaxFiles {
			return g, errors.New("item bound exceeded")
		}
		g.Files = append(g.Files, File{entry.Name(), n, hash})
	}
	if d.Size > 0 && total != d.Size {
		return g, errors.New("Steam item size mismatch")
	}
	if e = validateVPK(stage, g.Files); e != nil {
		return g, e
	}
	g.Digest = manifestDigest(g)
	u, e := storeio.Lock(ctx, s.root)
	if e != nil {
		return g, e
	}
	defer u()
	v, e := s.load()
	if e != nil {
		return g, e
	}
	o := v.Owners[g.Key()]
	if !s.trusted(server) || o.Server != server || o.Token != token {
		return g, errors.New("stale importer fence")
	}
	if old, e := s.Generation(id, rev); e == nil {
		if old.Digest != g.Digest {
			return g, errors.New("revision bytes conflict")
		}
		g = old
	} else if !errors.Is(e, os.ErrNotExist) {
		return g, e
	} else {
		if e = storeio.Save(stage, "manifest.json", g); e != nil {
			return g, e
		}
		for _, f := range append(append([]File{}, g.Files...), File{Name: "manifest.json"}) {
			file, openErr := os.Open(filepath.Join(stage, f.Name))
			if openErr != nil {
				return g, openErr
			}
			e = errors.Join(file.Chmod(0444), file.Sync(), file.Close())
			if e != nil {
				return g, e
			}
		}
		parent := filepath.Join(s.root, "generations", id)
		if e = os.MkdirAll(parent, 0755); e != nil {
			return g, e
		}
		if e = os.Rename(stage, filepath.Join(parent, rev)); e != nil {
			return g, e
		}
		published, openErr := artifact.OpenDirectory(filepath.Join(parent, rev))
		if openErr != nil {
			return g, openErr
		}
		e = errors.Join(published.Chmod(0555), published.Sync(), published.Close())
		if e != nil {
			return g, e
		}
		dir, e := artifact.OpenDirectory(parent)
		if e != nil {
			return g, e
		}
		e = dir.Sync()
		dir.Close()
		if e != nil {
			return g, e
		}
	}
	if e = s.pin(&v, server, g.Key()); e != nil {
		return g, e
	}
	delete(v.Owners, g.Key())
	e = storeio.Save(s.root, "state.json", v)
	return g, e
}
func (s *Store) Prune(ctx context.Context) (Result, error) {
	var out Result
	u, e := storeio.Lock(ctx, s.root)
	if e != nil {
		return out, e
	}
	defer u()
	v, e := s.load()
	if e != nil {
		return out, e
	}
	for _, srv := range s.o.Servers {
		if !v.Known[srv.ID] {
			return out, errors.New("unknown server inventory prevents pruning")
		}
	}
	keep := map[string]bool{}
	for _, r := range v.References {
		keep[r.Key] = true
	}
	for k := range v.Owners {
		keep[k] = true
	}
	ids, e := os.ReadDir(filepath.Join(s.root, "generations"))
	if e != nil {
		return out, e
	}
	for _, id := range ids {
		if !number(id.Name()) || !id.IsDir() {
			return out, errors.New("unknown generation directory")
		}
		revs, e := os.ReadDir(filepath.Join(s.root, "generations", id.Name()))
		if e != nil {
			return out, e
		}
		sort.Slice(revs, func(i, j int) bool { return revs[i].Name() < revs[j].Name() })
		for _, r := range revs {
			g, e := s.Generation(id.Name(), r.Name())
			if e != nil {
				return out, e
			}
			if keep[g.Key()] {
				continue
			}
			path := filepath.Join(s.root, "generations", g.Key())
			if e = os.Chmod(path, 0755); e != nil {
				return out, e
			}
			if e = os.RemoveAll(path); e != nil {
				return out, e
			}
			out.Released = append(out.Released, Outcome{g.ItemID, g.Revision, "unreferenced"})
		}
	}
	return out, nil
}
func (s *Store) Status() (any, error) {
	v, e := s.load()
	if e != nil {
		return nil, e
	}
	return struct {
		Owners           int
		References       int
		KnownServers     map[string]bool
		TrustedImporters []string
	}{len(v.Owners), len(v.References), v.Known, s.o.TrustedImporters}, nil
}
