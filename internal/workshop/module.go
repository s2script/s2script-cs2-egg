// SPDX-License-Identifier: GPL-3.0-or-later
package workshop

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/s2script/s2script-cs2-egg/internal/artifact"
	"github.com/s2script/s2script-cs2-egg/internal/nodejobs"
	node "github.com/s2script/s2script-cs2-egg/internal/nodetypes"
	"github.com/s2script/s2script-cs2-egg/internal/protocol"
	"github.com/s2script/s2script-cs2-egg/internal/storeio"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"syscall"
)

type upload struct {
	Server, Item, Revision, Token, Phase string
	Files                                []File
}
type Module struct{ Store *Store }

func (m *Module) Name() string                                 { return "workshop" }
func (m *Module) Start(ctx context.Context, h node.Host) error { return m.Store.Start(ctx) }
func (m *Module) Close(context.Context) error                  { m.Store.jobs.Close(); return nil }
func (m *Module) Health() node.Health                          { return node.Health{Status: "healthy"} }
func (m *Module) Capabilities() []string                       { return []string{protocol.CapWorkshop} }
func (m *Module) Handle(ctx context.Context, srv node.Server, cap string, msg protocol.Message) (any, error) {
	var r protocol.WorkshopRequest
	if cap != protocol.CapWorkshop || protocol.Decode(msg.Payload, &r) != nil {
		return nil, errors.New("invalid workshop request")
	}
	return m.Store.Handle(ctx, srv.ID, r)
}
func (s *Store) Start(ctx context.Context) error { return s.jobs.Start(ctx) }
func (s *Store) job(ctx context.Context, server string, b json.RawMessage) (any, error) {
	var r protocol.WorkshopRequest
	if e := protocol.Decode(b, &r); e != nil {
		return nil, e
	}
	switch r.Operation {
	case "select":
		return s.selectItem(ctx, server, r)
	case "begin":
		return s.beginUpload(ctx, server, r)
	case "seal":
		return s.seal(ctx, server, r)
	case "reserve":
		g, e := s.Reserve(ctx, server, r.ItemID, r.Revision)
		return protocol.WorkshopResult{State: "ready", Generation: wireGeneration(g)}, e
	}
	return nil, errors.New("unsupported worker operation")
}
func wireGeneration(g Generation) *protocol.WorkshopGeneration {
	b, _ := json.Marshal(g)
	var w protocol.WorkshopGeneration
	_ = json.Unmarshal(b, &w)
	return &w
}
func (s *Store) Handle(ctx context.Context, server string, r protocol.WorkshopRequest) (protocol.WorkshopResult, error) {
	var out protocol.WorkshopResult
	known := false
	for _, srv := range s.o.Servers {
		known = known || srv.ID == server
	}
	if !known {
		return out, errors.New("unknown server")
	}
	switch r.Operation {
	case "link-info":
		return s.linkInfo(ctx, server, r)
	case "finish":
		return protocol.WorkshopResult{State: "finished"}, s.jobs.ReleasePrefix(server, r.RequestID)
	case "select", "begin", "seal", "reserve":
		if !s.selected(r.ItemID) {
			return out, errors.New("item not selected by node policy")
		}
		j, e := s.jobs.Submit(server, r.RequestID, r)
		return jobResult(j), e
	case "status":
		j, e := s.jobs.Get(server, r.Handle)
		return jobResult(j), e
	case "import-chunk":
		return out, s.chunk(ctx, server, r)
	case "read":
		return s.read(ctx, server, r)
	case "commit": // A boot receipt only renews a pin; it never releases old references.
		g, e := s.Reserve(ctx, server, r.ItemID, r.Revision)
		return protocol.WorkshopResult{State: "ready", Generation: wireGeneration(g)}, e
	}
	return out, errors.New("unsupported workshop operation")
}
func jobResult(j nodejobs.Job) protocol.WorkshopResult {
	r := protocol.WorkshopResult{State: j.State, Handle: j.ID, Error: j.Error}
	if j.State == "ready" {
		if json.Unmarshal(j.Output, &r) != nil {
			return protocol.WorkshopResult{State: "failed", Error: "invalid persisted result"}
		}
		r.Handle = j.ID
	}
	return r
}
func (s *Store) selectItem(ctx context.Context, server string, r protocol.WorkshopRequest) (protocol.WorkshopResult, error) {
	d, e := s.o.Details(ctx, []string{r.ItemID})
	if e != nil {
		return protocol.WorkshopResult{}, e
	}
	detail, ok := d[r.ItemID]
	if !ok || detail.AppID != 730 || detail.ID != r.ItemID || !number(detail.Revision) {
		return protocol.WorkshopResult{}, errors.New("invalid Steam selection")
	}
	g, e := s.Reserve(ctx, server, r.ItemID, detail.Revision)
	if e == nil {
		return protocol.WorkshopResult{State: "ready", Generation: wireGeneration(g)}, nil
	}
	if !errors.Is(e, os.ErrNotExist) {
		return protocol.WorkshopResult{}, e
	}
	if s.trusted(server) {
		token, e := s.Begin(ctx, server, r.ItemID, detail.Revision, r.RequestID)
		if e == nil {
			return protocol.WorkshopResult{State: "updater", Token: token, Revision: detail.Revision, Owner: server}, nil
		}
	}
	u, e := storeio.Lock(ctx, s.root)
	if e != nil {
		return protocol.WorkshopResult{}, e
	}
	defer u()
	v, e := s.load()
	if e != nil {
		return protocol.WorkshopResult{}, e
	}
	reason := "waiting_for_trusted_importer"
	if len(s.o.TrustedImporters) == 0 {
		reason = "no_trusted_importer"
	}
	return protocol.WorkshopResult{State: "pending", Revision: detail.Revision, Owner: v.Owners[r.ItemID+"/"+detail.Revision].Server, Error: reason}, nil
}
func (s *Store) beginUpload(ctx context.Context, server string, r protocol.WorkshopRequest) (protocol.WorkshopResult, error) {
	if !s.trusted(server) {
		return protocol.WorkshopResult{State: "consumer"}, nil
	}
	if len(r.Files) == 0 || len(r.Files) > MaxFiles {
		return protocol.WorkshopResult{}, errors.New("invalid item file count")
	}
	var total int64
	seen := map[string]bool{}
	files := make([]File, 0, len(r.Files))
	for _, f := range r.Files {
		if !safeFile(f.Name) || seen[f.Name] || f.Size < 0 || f.Size > MaxItemBytes || !storeio.Digest(f.SHA256) {
			return protocol.WorkshopResult{}, errors.New("invalid upload file")
		}
		seen[f.Name] = true
		total += f.Size
		files = append(files, File(f))
	}
	if total > MaxItemBytes {
		return protocol.WorkshopResult{}, errors.New("upload too large")
	}
	d, e := s.o.Details(ctx, []string{r.ItemID})
	if e != nil {
		return protocol.WorkshopResult{}, e
	}
	if detail, ok := d[r.ItemID]; ok && detail.ID == r.ItemID && detail.AppID == 730 && detail.Revision != r.Revision {
		return protocol.WorkshopResult{State: "stale"}, nil
	}
	if detail, ok := d[r.ItemID]; !ok || detail.ID != r.ItemID || detail.AppID != 730 || detail.Revision != r.Revision || detail.Size > 0 && detail.Size != total {
		return protocol.WorkshopResult{}, errors.New("upload does not match current Steam metadata")
	}
	token, e := s.Begin(ctx, server, r.ItemID, r.Revision, r.RequestID)
	if e != nil {
		return protocol.WorkshopResult{}, e
	}
	u, e := storeio.Lock(ctx, s.root)
	if e != nil {
		return protocol.WorkshopResult{}, e
	}
	defer u()
	v := upload{server, r.ItemID, r.Revision, token, "receiving", files}
	var old upload
	e = storeio.JSON(s.root, "uploads/"+token+".json", &old)
	if e == nil {
		if !reflect.DeepEqual(old.Files, files) {
			return protocol.WorkshopResult{}, errors.New("upload snapshot conflict")
		}
		return protocol.WorkshopResult{State: "upload", Token: token}, nil
	}
	if !errors.Is(e, os.ErrNotExist) {
		return protocol.WorkshopResult{}, e
	}
	if e = os.MkdirAll(filepath.Join(s.root, "uploads", token), 0700); e != nil {
		return protocol.WorkshopResult{}, e
	}
	e = storeio.Save(s.root, "uploads/"+token+".json", v)
	return protocol.WorkshopResult{State: "upload", Token: token}, e
}
func (s *Store) upload(server, token string) (upload, error) {
	var v upload
	if len(token) != 32 || !storeio.Digest(token+token) {
		return v, errors.New("invalid upload token")
	}
	if !s.trusted(server) {
		return v, errors.New("import authorization revoked")
	}
	e := storeio.JSON(s.root, "uploads/"+token+".json", &v)
	if e != nil {
		return v, e
	}
	if v.Server != server || v.Token != token {
		return v, errors.New("wrong upload owner")
	}
	return v, nil
}
func (s *Store) chunk(ctx context.Context, server string, r protocol.WorkshopRequest) error {
	if len(r.Data) > ChunkSize || r.Offset < 0 {
		return errors.New("invalid upload chunk")
	}
	u, e := storeio.Lock(ctx, s.root)
	if e != nil {
		return e
	}
	defer u()
	v, e := s.upload(server, r.Token)
	if e != nil {
		return e
	}
	var selected *File
	for _, f := range v.Files {
		if f.Name == r.File {
			f := f
			selected = &f
		}
	}
	if selected == nil || r.Offset > selected.Size || int64(len(r.Data)) > selected.Size-r.Offset {
		return errors.New("chunk outside declared file")
	}
	root := filepath.Join(s.root, "uploads", r.Token)
	flags := syscall.O_RDWR | syscall.O_CREAT
	if v.Phase != "receiving" {
		// A lost seal reply leaves the durable begin job returning upload. Accept
		// only exact retries against the completed immutable generation; never
		// reopen or modify the sealed staging directory.
		g, err := s.Generation(v.Item, v.Revision)
		if err != nil || !reflect.DeepEqual(g.Files, v.Files) {
			return errors.New("upload sealed without matching published generation")
		}
		root = filepath.Join(s.root, "generations", g.Key())
		flags = syscall.O_RDONLY
	}
	d, e := artifact.OpenDirectory(root)
	if e != nil {
		return e
	}
	defer d.Close()
	f, e := artifact.OpenRegular(d, r.File, flags, 0600)
	if e != nil {
		return e
	}
	defer f.Close()
	info, e := f.Stat()
	if e != nil {
		return e
	}
	if v.Phase != "receiving" || info.Size() > r.Offset {
		b := make([]byte, len(r.Data))
		_, e = f.ReadAt(b, r.Offset)
		if e != nil || string(b) != string(r.Data) {
			return errors.New("ambiguous/replayed chunk differs")
		}
		return nil
	}
	if info.Size() != r.Offset {
		return errors.New("nonsequential upload chunk")
	}
	if _, e = f.WriteAt(r.Data, r.Offset); e != nil {
		return e
	}
	return f.Sync()
}
func (s *Store) seal(ctx context.Context, server string, r protocol.WorkshopRequest) (protocol.WorkshopResult, error) {
	u, e := storeio.Lock(ctx, s.root)
	if e != nil {
		return protocol.WorkshopResult{}, e
	}
	v, e := s.upload(server, r.Token)
	if e == nil {
		v.Phase = "sealed"
		e = storeio.Save(s.root, "uploads/"+r.Token+".json", v)
	}
	u()
	if e != nil {
		return protocol.WorkshopResult{}, e
	}
	if v.Item != r.ItemID || v.Revision != r.Revision {
		return protocol.WorkshopResult{}, errors.New("seal identity mismatch")
	}
	stage := filepath.Join(s.root, "uploads", r.Token)
	if g, e := s.Generation(v.Item, v.Revision); e == nil {
		if !reflect.DeepEqual(g.Files, v.Files) {
			return protocol.WorkshopResult{}, errors.New("generation/upload conflict")
		}
		g, e = s.Reserve(ctx, server, v.Item, v.Revision)
		return protocol.WorkshopResult{State: "ready", Generation: wireGeneration(g)}, e
	}
	for _, f := range v.Files {
		hash, n, e := storeio.FileHash(stage, f.Name, MaxItemBytes)
		if e != nil || n != f.Size || hash != f.SHA256 {
			return protocol.WorkshopResult{}, errors.New("uploaded content incomplete or changed")
		}
	}
	g, e := s.publish(ctx, server, v.Item, v.Revision, v.Token, stage)
	return protocol.WorkshopResult{State: "ready", Generation: wireGeneration(g)}, e
}
func (s *Store) read(ctx context.Context, server string, r protocol.WorkshopRequest) (protocol.WorkshopResult, error) {
	var out protocol.WorkshopResult
	u, e := storeio.Lock(ctx, s.root)
	if e != nil {
		return out, e
	}
	defer u()
	v, e := s.load()
	if e != nil {
		return out, e
	}
	if _, ok := v.References[server+":"+r.ItemID+"/"+r.Revision]; !ok {
		return out, errors.New("generation not reserved")
	}
	g, e := s.Generation(r.ItemID, r.Revision)
	if e != nil {
		return out, e
	}
	var found *File
	for _, f := range g.Files {
		if f.Name == r.File {
			f := f
			found = &f
		}
	}
	if found == nil || r.Offset < 0 || r.Offset > found.Size {
		return out, errors.New("invalid generation read")
	}
	d, e := artifact.OpenDirectory(filepath.Join(s.root, "generations", g.Key()))
	if e != nil {
		return out, e
	}
	defer d.Close()
	f, e := artifact.OpenRegular(d, r.File, syscall.O_RDONLY, 0)
	if e != nil {
		return out, e
	}
	defer f.Close()
	b := make([]byte, min(int64(ChunkSize), found.Size-r.Offset))
	_, e = io.ReadFull(io.NewSectionReader(f, r.Offset, int64(len(b))), b)
	return protocol.WorkshopResult{State: "ready", Data: b}, e
}
