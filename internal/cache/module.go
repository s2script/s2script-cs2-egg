// SPDX-License-Identifier: GPL-3.0-or-later
package cache

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/s2script/s2script-cs2-egg/internal/artifact"
	"github.com/s2script/s2script-cs2-egg/internal/framework"
	"github.com/s2script/s2script-cs2-egg/internal/nodejobs"
	node "github.com/s2script/s2script-cs2-egg/internal/nodetypes"
	"github.com/s2script/s2script-cs2-egg/internal/plugins"
	"github.com/s2script/s2script-cs2-egg/internal/protocol"
	"github.com/s2script/s2script-cs2-egg/internal/storeio"
	"io"
	"os"
	"path/filepath"
	"syscall"
)

type Policy struct {
	Pins map[string]string `json:"pins"`
}
type Module struct {
	Store    *Store
	Policy   Policy
	Resolver framework.Resolver
	Registry plugins.Registry
	jobs     *nodejobs.Pool
}
type record struct {
	Selection protocol.ArtifactSelection
	Size      int64
}

func (m *Module) Name() string { return "cache" }
func (m *Module) Start(ctx context.Context, h node.Host) error {
	if m.Store == nil {
		return errors.New("cache store missing")
	}
	var e error
	m.jobs, e = nodejobs.New(filepath.Join(m.Store.root, "jobs"), m.run)
	if e != nil {
		return e
	}
	return m.jobs.Start(ctx)
}
func (m *Module) Close(context.Context) error {
	if m.jobs != nil {
		m.jobs.Close()
	}
	return nil
}
func (m *Module) Health() node.Health    { return node.Health{Status: "healthy"} }
func (m *Module) Capabilities() []string { return []string{protocol.CapCache} }
func (m *Module) fetch(ctx context.Context, server string, b json.RawMessage) (any, error) {
	var selected protocol.ArtifactSelection
	if e := protocol.Decode(b, &selected); e != nil {
		return nil, e
	}
	raw, e := m.resolve(ctx, selected)
	if e != nil {
		return nil, e
	}
	path, e := m.Store.Get(ctx, raw, selected.SHA256)
	if e != nil {
		return nil, e
	}
	info, e := os.Stat(path)
	if e != nil {
		return nil, e
	}
	rec := record{selected, info.Size()}
	if e = storeio.Save(m.Store.root, "provenance/"+selected.SHA256+".json", struct {
		record
		URL string
	}{rec, raw}); e != nil {
		return nil, e
	}
	return rec, nil
}

type task struct {
	Selection protocol.ArtifactSelection
	Validate  bool
}

func (m *Module) run(ctx context.Context, server string, b json.RawMessage) (any, error) {
	var t task
	if e := json.Unmarshal(b, &t); e != nil {
		return nil, e
	}
	if t.Validate {
		_, e := m.resolve(ctx, t.Selection)
		return record{Selection: t.Selection}, e
	}
	data, _ := json.Marshal(t.Selection)
	return m.fetch(ctx, server, data)
}
func (m *Module) resolve(ctx context.Context, selected protocol.ArtifactSelection) (string, error) {
	key := selected.Source
	if key == "registry" {
		key += "/" + selected.Name
	}
	if pin := m.Policy.Pins[key]; pin != "" && pin != selected.Version {
		return "", fmt.Errorf("local pin %s conflicts with fleet pin %s for %s", selected.Version, pin, key)
	}
	raw := ""
	if selected.Source == "registry" {
		plan, e := m.Registry.Plan(ctx, selected.Name, selected.Version, nil)
		if e != nil {
			return "", e
		}
		for _, p := range plan.Install {
			if p.Name == selected.Name && p.Version == selected.Version && p.SHA256 == selected.SHA256 {
				raw = p.URL
			}
		}
		if raw == "" {
			return "", errors.New("registry selection differs from verified plan")
		}
	} else {
		r, e := m.Resolver.ResolveArtifact(ctx, selected.Source, selected.Version)
		if e != nil {
			return "", e
		}
		if r.SHA256 != selected.SHA256 || r.SourceRevision != selected.SourceRevision || r.Integrity != selected.Integrity {
			return "", errors.New("framework selection differs from official immutable release")
		}
		raw = r.URL
	}
	return raw, nil
}
func (m *Module) Handle(ctx context.Context, srv node.Server, cap string, msg protocol.Message) (any, error) {
	var r protocol.CacheRequest
	if cap != protocol.CapCache || protocol.Decode(msg.Payload, &r) != nil {
		return nil, errors.New("invalid cache operation")
	}
	return m.Request(ctx, srv.ID, r)
}
func (m *Module) Request(ctx context.Context, server string, r protocol.CacheRequest) (protocol.CacheResult, error) {
	var out protocol.CacheResult
	if m.jobs == nil {
		return out, errors.New("cache workers unavailable")
	}
	if r.Operation == "submit" || r.Operation == "validate" {
		j, e := m.jobs.Submit(server, r.RequestID, task{r.Artifact, r.Operation == "validate"})
		return protocol.CacheResult{State: j.State, Handle: j.ID, Error: j.Error}, e
	}
	if r.Operation == "release" {
		return protocol.CacheResult{State: "released"}, m.jobs.Release(server, r.Handle)
	}
	j, e := m.jobs.Get(server, r.Handle)
	if e != nil {
		return out, e
	}
	out = protocol.CacheResult{State: j.State, Handle: j.ID, Error: j.Error}
	if j.State != "ready" {
		return out, nil
	}
	var rec record
	if e = json.Unmarshal(j.Output, &rec); e != nil {
		return out, e
	}
	out.SHA256 = rec.Selection.SHA256
	out.Size = rec.Size
	if r.Operation == "status" {
		return out, nil
	}
	if r.Operation != "read" || r.Offset < 0 || r.Offset > rec.Size {
		return out, errors.New("invalid cache read")
	}
	d, e := artifact.OpenDirectory(m.Store.root)
	if e != nil {
		return out, e
	}
	defer d.Close()
	f, e := artifact.OpenRegular(d, "objects/"+out.SHA256, syscall.O_RDONLY, 0)
	if e != nil {
		return out, e
	}
	defer f.Close()
	out.Data = make([]byte, min(int64(256<<10), rec.Size-r.Offset))
	_, e = io.ReadFull(io.NewSectionReader(f, r.Offset, int64(len(out.Data))), out.Data)
	return out, e
}

// Prune conservatively retains every object while any durable handle exists.
func (s *Store) Prune(ctx context.Context) ([]string, error) {
	unlock, e := storeio.Lock(ctx, s.root)
	if e != nil {
		return nil, e
	}
	defer unlock()
	jobs, e := os.ReadDir(filepath.Join(s.root, "jobs"))
	if e != nil && !errors.Is(e, os.ErrNotExist) {
		return nil, e
	}
	if len(jobs) > 0 {
		return nil, errors.New("release all cache handles before pruning")
	}
	files, e := os.ReadDir(filepath.Join(s.root, "objects"))
	if e != nil {
		return nil, e
	}
	var removed []string
	for _, f := range files {
		if !storeio.Digest(f.Name()) {
			return removed, errors.New("invalid cache object")
		}
		e = os.Remove(filepath.Join(s.root, "objects", f.Name()))
		if e != nil {
			return removed, e
		}
		removed = append(removed, f.Name())
	}
	return removed, nil
}

// Sync warms a policy-selected exact artifact from the host CLI without exporting paths.
func (m *Module) Sync(ctx context.Context, source, name, version string) (any, error) {
	a := protocol.ArtifactSelection{Source: source, Name: name, Version: version}
	if source == "registry" {
		p, e := m.Registry.Plan(ctx, name, version, nil)
		if e != nil {
			return nil, e
		}
		for _, v := range p.Install {
			if v.Name == name && v.Version == version {
				a.SHA256 = v.SHA256
			}
		}
		if a.SHA256 == "" {
			return nil, errors.New("exact registry artifact absent")
		}
	} else {
		r, e := m.Resolver.ResolveArtifact(ctx, source, version)
		if e != nil {
			return nil, e
		}
		a.Version = r.Version
		a.SHA256 = r.SHA256
		a.SourceRevision = r.SourceRevision
		a.Integrity = r.Integrity
	}
	b, _ := json.Marshal(a)
	return m.fetch(ctx, "administrator", b)
}
