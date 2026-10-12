// SPDX-License-Identifier: GPL-3.0-or-later
package workshop

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/s2script/s2script-cs2-egg/internal/artifact"
	node "github.com/s2script/s2script-cs2-egg/internal/nodetypes"
	"github.com/s2script/s2script-cs2-egg/internal/protocol"
	"github.com/s2script/s2script-cs2-egg/internal/runtimelease"
	"github.com/s2script/s2script-cs2-egg/internal/storeio"
	"github.com/s2script/s2script-cs2-egg/internal/volumehelper"
	"io"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"
)

type Exchange func(context.Context, protocol.WorkshopRequest) (protocol.WorkshopResult, error)
type LocalOptions struct {
	Root, Mode, Mount string
	Items             []string
	Wait              time.Duration
	HostRoot          string
	Link              func(context.Context, Generation, string) error
	GID               int
}
type localState struct {
	Version  int
	Items    map[string]Generation
	Requests map[string]string
}
type pending struct {
	Generation          Generation
	Mode, Stage, Backup string
	Mount               string
	ACF                 []byte
	ACFMode             uint32
	Phase               string
}

func await(ctx context.Context, call Exchange, r protocol.WorkshopRequest) (protocol.WorkshopResult, error) {
	out, e := call(ctx, r)
	for e == nil && (out.State == "running" || out.State == "pending" && out.Revision == "") {
		select {
		case <-ctx.Done():
			return out, ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
		out, e = call(ctx, protocol.WorkshopRequest{Operation: "status", Handle: out.Handle})
	}
	if e == nil && out.State == "failed" {
		e = errors.New(out.Error)
	}
	return out, e
}
func loadLocal(root string) (localState, error) {
	v := localState{Version: 1, Items: map[string]Generation{}, Requests: map[string]string{}}
	e := storeio.JSON(root, "egg/workshop-state.json", &v)
	if errors.Is(e, os.ErrNotExist) {
		e = nil
	}
	if v.Version != 1 || v.Items == nil || v.Requests == nil {
		return v, errors.New("invalid local workshop inventory")
	}
	return v, e
}

// PrepareLocked must be called only under runtime lease then artifact lock after Recover.
// It never reacquires either lock and never invokes the host private helper.
func PrepareLocked(ctx context.Context, o LocalOptions, call Exchange) (Result, error) {
	var out Result
	if o.Mode == "" {
		o.Mode = "copy"
	}
	if o.Mount == "" {
		o.Mount = "/mnt/s2workshop"
	}
	if o.Wait <= 0 {
		o.Wait = 30 * time.Second
	}
	if e := RecoverLocked(o.Root); e != nil {
		return out, e
	}
	local, e := loadLocal(o.Root)
	if e != nil {
		return out, e
	}
	raw, e := storeio.Read(o.Root, ACFPath, 4<<20)
	if errors.Is(e, os.ErrNotExist) {
		raw = []byte(`"AppWorkshop" { "appid" "730" }`)
		e = nil
	}
	if e != nil {
		return out, e
	}
	doc, e := ParseACF(raw)
	if e != nil {
		return out, e
	}
	for _, id := range o.Items {
		if !number(id) {
			return out, errors.New("invalid selected item")
		}
		request := local.Requests[id]
		if request == "" {
			request = storeio.ID()
			local.Requests[id] = request
			if e = storeio.Save(o.Root, "egg/workshop-state.json", local); e != nil {
				return out, e
			}
		}
		app := doc.Root.Child("AppWorkshop")
		present := app.Child("WorkshopItemsInstalled").Child(id) != nil
		rev, size, re := doc.Revision(id)
		if present && !number(app.Child("WorkshopItemsInstalled").Child(id).Value("manifest")) {
			out.Skipped = append(out.Skipped, Outcome{id, "", "legacy/unversioned stays local"})
			continue
		}
		if re == nil {
			files, e := snapshot(o, id, local.Items[id])
			if e != nil {
				return out, e
			}
			var total int64
			for _, f := range files {
				total += f.Size
			}
			if total != size {
				return out, errors.New("ACF item size differs from complete files")
			}
			sameGeneration := local.Items[id].Revision == rev && local.Items[id].Digest == manifestDigest(Generation{Files: files})
			if !sameGeneration {
				wire := make([]protocol.WorkshopFile, len(files))
				for i, f := range files {
					wire[i] = protocol.WorkshopFile(f)
				}
				r, e := await(ctx, call, protocol.WorkshopRequest{Operation: "begin", RequestID: request + "-import", ItemID: id, Revision: rev, Files: wire})
				if e != nil {
					return out, e
				}
				if r.State == "upload" {
					for _, f := range files {
						if e = uploadFile(ctx, o, local.Items[id], id, f, r.Token, call); e != nil {
							return out, e
						}
					}
					r, e = await(ctx, call, protocol.WorkshopRequest{Operation: "seal", RequestID: request + "-seal", ItemID: id, Revision: rev, Token: r.Token})
					if e != nil {
						return out, e
					}
					out.Imported = append(out.Imported, Outcome{id, rev, "trusted importer snapshot"})
				}
			}
		}
		waitCtx, waitCancel := context.WithTimeout(ctx, o.Wait)
		defer waitCancel()
		// Selection observes mutable node state. Keep import IDs durable, but never
		// replay a completed pending selection from a previous startup.
		selectionRun := storeio.ID()
		var selected protocol.WorkshopResult
		for attempt := 0; ; attempt++ {
			selected, e = await(waitCtx, call, protocol.WorkshopRequest{Operation: "select", RequestID: fmt.Sprintf("%s-select-%s-%d", request, selectionRun, attempt), ItemID: id})
			if e != nil {
				return out, fmt.Errorf("workshop pending item %s revision %s owner %s: selection budget/request failed: %w; inspect workshop status and complete trusted importer next startup or stopped sync", id, selected.Revision, selected.Owner, e)
			}
			if selected.State != "pending" {
				break
			}
			select {
			case <-waitCtx.Done():
				return out, fmt.Errorf("workshop pending item %s revision %s owner %s: %s: %w; complete trusted importer download then next startup or stopped sync", id, selected.Revision, selected.Owner, selected.Error, waitCtx.Err())
			case <-time.After(time.Second):
			}
		}
		waitCancel()
		if selected.State == "updater" {
			if g, ok := local.Items[id]; ok {
				if e = detachLocked(ctx, o, g, call); e != nil {
					return out, e
				}
				delete(local.Items, id)
			}
			out.Released = append(out.Released, Outcome{id, selected.Revision, "private updater; publish at next startup or stopped sync"})
		} else if selected.Generation != nil {
			g := fromWire(*selected.Generation)
			if e = attachLocked(ctx, o, doc, g, call); e != nil {
				return out, e
			}
			local.Items[id] = g
			if _, e = call(ctx, protocol.WorkshopRequest{Operation: "commit", ItemID: id, Revision: g.Revision}); e != nil {
				return out, e
			}
			out.Linked = append(out.Linked, Outcome{id, g.Revision, o.Mode})
			if o.Mode != "copy" {
				for _, f := range g.Files {
					out.BytesSaved += f.Size
				}
			}
		} else {
			return out, errors.New("unexpected workshop selection")
		}
		if _, e = call(ctx, protocol.WorkshopRequest{Operation: "finish", RequestID: request}); e != nil {
			return out, e
		}
		delete(local.Requests, id)
		if e = storeio.Save(o.Root, "egg/workshop-state.json", local); e != nil {
			return out, e
		}
	}
	return out, nil
}
func fromWire(g protocol.WorkshopGeneration) Generation {
	b, _ := json.Marshal(g)
	var v Generation
	_ = json.Unmarshal(b, &v)
	return v
}
func snapshot(o LocalOptions, id string, g Generation) ([]File, error) {
	root := filepath.Join(o.Root, ContentPath, id)
	d, e := artifact.OpenDirectory(root)
	if e != nil {
		return nil, e
	}
	defer d.Close()
	entries, e := d.ReadDir(MaxFiles + 1)
	if e != nil && e != io.EOF {
		return nil, e
	}
	if len(entries) > MaxFiles {
		return nil, errors.New("workshop file count bound")
	}
	var files []File
	for _, entry := range entries {
		if !safeFile(entry.Name()) {
			return nil, errors.New("unsafe workshop entry")
		}
		if entry.Type()&os.ModeSymlink != 0 {
			var found *File
			for _, f := range g.Files {
				if f.Name == entry.Name() {
					f := f
					found = &f
				}
			}
			target, e := os.Readlink(filepath.Join(root, entry.Name()))
			if e != nil || found == nil || target != filepath.Join(o.Mount, "generations", g.Key(), entry.Name()) {
				return nil, errors.New("unknown workshop symlink; left local")
			}
			files = append(files, *found)
			continue
		}
		hash, n, e := storeio.FileHash(root, entry.Name(), MaxItemBytes)
		if e != nil {
			return nil, e
		}
		files = append(files, File{entry.Name(), n, hash})
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Name < files[j].Name })
	return files, nil
}
func uploadFile(ctx context.Context, o LocalOptions, g Generation, id string, f File, token string, call Exchange) error {
	d, e := artifact.OpenDirectory(filepath.Join(o.Root, ContentPath, id))
	if e != nil {
		return e
	}
	defer d.Close()
	file, e := artifact.OpenRegular(d, f.Name, syscall.O_RDONLY, 0)
	if e != nil { // Known store references are transferred by exact manifest identity, never followed.
		if g.ItemID != id {
			return e
		}
		for offset := int64(0); offset < f.Size; {
			r, e := call(ctx, protocol.WorkshopRequest{Operation: "read", ItemID: id, Revision: g.Revision, File: f.Name, Offset: offset})
			if e != nil {
				return e
			}
			if len(r.Data) == 0 {
				return io.ErrUnexpectedEOF
			}
			if _, e = call(ctx, protocol.WorkshopRequest{Operation: "import-chunk", Token: token, File: f.Name, Offset: offset, Data: r.Data}); e != nil {
				return e
			}
			offset += int64(len(r.Data))
		}
		return nil
	}
	defer file.Close()
	for offset := int64(0); offset < f.Size || offset == 0 && f.Size == 0; {
		b := make([]byte, min(int64(ChunkSize), f.Size-offset))
		if _, e = io.ReadFull(file, b); e != nil {
			return e
		}
		if _, e = call(ctx, protocol.WorkshopRequest{Operation: "import-chunk", Token: token, File: f.Name, Offset: offset, Data: b}); e != nil {
			return e
		}
		offset += int64(len(b))
		if len(b) == 0 {
			break
		}
	}
	return nil
}
func stageGeneration(ctx context.Context, o LocalOptions, g Generation, mode string, call Exchange) (string, error) {
	stage := "egg/.workshop-stage-" + storeio.ID()
	if e := os.MkdirAll(filepath.Join(o.Root, stage), 0700); e != nil {
		return "", e
	}
	if mode == "symlink" {
		d, e := artifact.OpenDirectory(o.Mount)
		if e != nil {
			return "", e
		}
		var st syscall.Statfs_t
		e = syscall.Fstatfs(int(d.Fd()), &st)
		d.Close()
		if e != nil || st.Flags&syscall.MS_RDONLY == 0 {
			return "", errors.New("workshop mount must be read-only")
		}
	}
	if mode == "hardlink" {
		if o.Link == nil {
			return "", errors.New("new workshop hardlinks require stopped host sync")
		}
		if e := os.Chown(filepath.Join(o.Root, stage), -1, o.GID); e != nil {
			return "", e
		}
		if e := os.Chmod(filepath.Join(o.Root, stage), 0770); e != nil {
			return "", e
		}
		if e := o.Link(ctx, g, stage); e != nil {
			return "", e
		}
	} else {
		for _, f := range g.Files {
			dest := filepath.Join(o.Root, stage, f.Name)
			if mode == "symlink" {
				hash, n, e := storeio.FileHash(filepath.Join(o.Mount, "generations", g.Key()), f.Name, MaxItemBytes)
				if e != nil || hash != f.SHA256 || n != f.Size {
					return "", errors.New("mounted workshop bytes differ")
				}
				if e = os.Symlink(filepath.Join(o.Mount, "generations", g.Key(), f.Name), dest); e != nil {
					return "", e
				}
				continue
			}
			file, e := os.OpenFile(dest, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
			if e != nil {
				return "", e
			}
			for offset := int64(0); offset < f.Size; {
				r, readErr := call(ctx, protocol.WorkshopRequest{Operation: "read", ItemID: g.ItemID, Revision: g.Revision, File: f.Name, Offset: offset})
				if readErr != nil || len(r.Data) == 0 {
					file.Close()
					if readErr != nil {
						return "", readErr
					}
					return "", io.ErrUnexpectedEOF
				}
				if _, e = file.Write(r.Data); e != nil {
					file.Close()
					return "", e
				}
				offset += int64(len(r.Data))
			}
			e = errors.Join(file.Sync(), file.Close())
			if e != nil {
				return "", e
			}
		}
	}
	for _, f := range g.Files {
		if mode == "symlink" {
			continue
		}
		hash, n, e := storeio.FileHash(filepath.Join(o.Root, stage), f.Name, MaxItemBytes)
		if e != nil || hash != f.SHA256 || n != f.Size {
			return "", errors.New("staged workshop bytes differ")
		}
		if mode == "copy" {
			if e = os.Chmod(filepath.Join(o.Root, stage, f.Name), 0644); e != nil {
				return "", e
			}
		}
	}
	d, e := artifact.OpenDirectory(filepath.Join(o.Root, stage))
	if e != nil {
		return "", e
	}
	e = errors.Join(d.Sync(), d.Close())
	return stage, e
}
func attachLocked(ctx context.Context, o LocalOptions, doc *Document, g Generation, call Exchange) error {
	if o.Mode == "hardlink" && o.Link == nil {
		if e := verifyExistingHardlinks(ctx, o, doc, g, call); e != nil {
			return fmt.Errorf("new/damaged hardlink workshop generation requires stopped host sync: %w", e)
		}
		return nil
	}
	stage, e := stageGeneration(ctx, o, g, o.Mode, call)
	if e != nil {
		return e
	}
	var total int64
	for _, f := range g.Files {
		total += f.Size
	}
	doc.SetItem(g.ItemID, g.Revision, total, g.Updated)
	mode := uint32(0600)
	if info, e := os.Lstat(filepath.Join(o.Root, ACFPath)); e == nil {
		if !info.Mode().IsRegular() {
			return errors.New("private ACF must be regular")
		}
		mode = uint32(info.Mode().Perm())
	}
	p := pending{Generation: g, Mode: o.Mode, Mount: o.Mount, Stage: stage, Backup: "egg/.workshop-old-" + storeio.ID(), ACF: doc.Bytes(), ACFMode: mode, Phase: "prepared"}
	if e = storeio.Save(o.Root, "egg/workshop-pending.json", p); e != nil {
		return e
	}
	return RecoverLocked(o.Root)
}
func RecoverLocked(root string) error {
	var p pending
	e := storeio.JSON(root, "egg/workshop-pending.json", &p)
	if errors.Is(e, os.ErrNotExist) {
		return nil
	}
	if e != nil {
		return e
	}
	if !number(p.Generation.ItemID) || !number(p.Generation.Revision) || !strings.HasPrefix(p.Stage, "egg/.workshop-stage-") || len(p.Stage) != len("egg/.workshop-stage-")+32 || !strings.HasPrefix(p.Backup, "egg/.workshop-old-") || len(p.Backup) != len("egg/.workshop-old-")+32 {
		return errors.New("invalid workshop recovery journal")
	}
	for _, v := range []string{p.Stage, p.Backup} {
		if !storeio.Digest(v[len(v)-32:] + v[len(v)-32:]) {
			return errors.New("invalid journal stage identity")
		}
	}
	d, e := artifact.OpenDirectory(root)
	if e != nil {
		return e
	}
	defer d.Close()
	target := ContentPath + "/" + p.Generation.ItemID
	parent, _, e := artifact.ParentDirectory(d, target, true)
	if e != nil {
		return e
	}
	parent.Close()
	if _, e = os.Lstat(filepath.Join(root, p.Stage)); e == nil {
		if info, e := os.Lstat(filepath.Join(root, target)); e == nil {
			if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
				return errors.New("item root is not a private directory")
			}
			if _, e = os.Lstat(filepath.Join(root, p.Backup)); errors.Is(e, os.ErrNotExist) {
				if e = artifact.Rename(d, target, p.Backup); e != nil {
					return e
				}
			} else if e != nil {
				return e
			}
		} else if !errors.Is(e, os.ErrNotExist) {
			return e
		}
		if e = artifact.Rename(d, p.Stage, target); e != nil {
			return e
		}
	} else if !errors.Is(e, os.ErrNotExist) {
		return e
	}
	files, verifyErr := snapshot(LocalOptions{Root: root, Mount: p.Mount}, p.Generation.ItemID, p.Generation)
	if verifyErr != nil || manifestDigest(Generation{Files: files}) != p.Generation.Digest {
		return errors.New("workshop recovery target missing or changed")
	}
	if _, verifyErr = ParseACF(p.ACF); verifyErr != nil {
		return verifyErr
	}
	if e = storeio.Write(root, ACFPath, p.ACF, os.FileMode(p.ACFMode)); e != nil {
		return e
	}
	local, e := loadLocal(root)
	if e != nil {
		return e
	}
	local.Items[p.Generation.ItemID] = p.Generation
	if e = storeio.Save(root, "egg/workshop-state.json", local); e != nil {
		return e
	}
	if e = artifact.Remove(d, "egg/workshop-pending.json"); e != nil {
		return e
	}
	return os.RemoveAll(filepath.Join(root, p.Backup))
}
func detachLocked(ctx context.Context, o LocalOptions, g Generation, call Exchange) error {
	raw, e := storeio.Read(o.Root, ACFPath, 4<<20)
	if e != nil {
		return e
	}
	doc, e := ParseACF(raw)
	if e != nil {
		return e
	}
	o.Mode = "copy"
	return attachLocked(ctx, o, doc, g, call)
}

// Sync never opens private volume data in the daemon. The authenticated helper owns stopped locks.
func (s *Store) Sync(ctx context.Context, srv node.Server) (Result, error) {
	var out Result
	if srv.Helper == nil {
		return out, errors.New("workshop sync requires configured actual volume-UID helper")
	}
	u, e := storeio.Lock(ctx, s.root)
	if e != nil {
		return out, e
	}
	v, e := s.load()
	u()
	if e != nil {
		return out, e
	}
	fence := v.References
	conn, e := volumehelper.Dial(ctx, srv.Helper.Socket, srv.Helper.UID)
	if e != nil {
		return out, e
	}
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	if e = volumehelper.Write(conn, "begin-workshop", struct {
		Mode string `json:"mode"`
	}{s.o.Mode}); e != nil {
		return out, e
	}
	for {
		m, e := volumehelper.Read(conn)
		if e != nil {
			return out, e
		}
		switch m.Type {
		case "locked":
			if !s.trusted(srv.ID) {
				unlock, err := storeio.Lock(ctx, s.root)
				if err != nil {
					return out, err
				}
				current, err := s.load()
				if err == nil {
					for key, old := range v.Owners {
						if old.Server == srv.ID && current.Owners[key] == old {
							delete(current.Owners, key)
						}
					}
					err = storeio.Save(s.root, "state.json", current)
				}
				unlock()
				if err != nil {
					return out, err
				}
			}
		case "request":
			var r protocol.WorkshopRequest
			if e = protocol.Decode(m.Payload, &r); e != nil {
				return out, e
			}
			result, e := s.Handle(ctx, srv.ID, r)
			if e != nil {
				result = protocol.WorkshopResult{State: "failed", Error: e.Error()}
			}
			if e = volumehelper.Write(conn, "reply", result); e != nil {
				return out, e
			}
		case "links":
			var r struct {
				Generation Generation
				Stage      string
			}
			if e = protocol.Decode(m.Payload, &r); e != nil {
				return out, e
			}
			e = s.linkStage(srv, r.Generation, r.Stage)
			reply := protocol.WorkshopResult{State: "ready"}
			if e != nil {
				reply.State = "failed"
				reply.Error = e.Error()
			}
			if e = volumehelper.Write(conn, "reply", reply); e != nil {
				return out, e
			}
		case "result":
			var receipt struct {
				Result         Result
				Items          map[string]Generation
				InventoryKnown bool
				Error          string
			}
			if e = protocol.Decode(m.Payload, &receipt); e != nil {
				return out, e
			}
			if receipt.Error != "" {
				return out, errors.New(receipt.Error)
			}
			u, e = storeio.Lock(ctx, s.root)
			if e != nil {
				return out, e
			}
			latest, e := s.load()
			if e == nil {
				for k, old := range fence {
					if receipt.InventoryKnown && old.Server == srv.ID && latest.References[k] == old {
						keep := false
						for _, g := range receipt.Items {
							keep = keep || g.Key() == old.Key
						}
						if !keep {
							delete(latest.References, k)
						}
					}
				}
				latest.Known[srv.ID] = receipt.InventoryKnown
				e = storeio.Save(s.root, "state.json", latest)
			}
			u()
			if e != nil {
				return out, e
			}
			return receipt.Result, volumehelper.Write(conn, "release", struct{}{})
		default:
			return out, errors.New("unexpected helper operation")
		}
	}
}
func ServePrivate(ctx context.Context, conn net.Conn, o LocalOptions) error {
	release, e := runtimelease.Acquire(o.Root)
	if e != nil {
		return e
	}
	defer release()
	unlock, e := artifact.Acquire(o.Root)
	if e != nil {
		return e
	}
	defer unlock()
	if e = artifact.Recover(o.Root); e != nil {
		return e
	}
	if e = volumehelper.Write(conn, "locked", struct{}{}); e != nil {
		return e
	}
	exchange := func(ctx context.Context, r protocol.WorkshopRequest) (protocol.WorkshopResult, error) {
		var out protocol.WorkshopResult
		if e := volumehelper.Write(conn, "request", r); e != nil {
			return out, e
		}
		m, e := volumehelper.Read(conn)
		if e != nil {
			return out, e
		}
		if m.Type != "reply" {
			return out, errors.New("invalid private reply")
		}
		if e = protocol.Decode(m.Payload, &out); e != nil {
			return out, e
		}
		if out.State == "failed" {
			return out, errors.New(out.Error)
		}
		return out, nil
	}
	o.Link = func(ctx context.Context, g Generation, stage string) error {
		if e := volumehelper.Write(conn, "links", struct {
			Generation Generation
			Stage      string
		}{g, stage}); e != nil {
			return e
		}
		m, e := volumehelper.Read(conn)
		if e != nil {
			return e
		}
		var out protocol.WorkshopResult
		if m.Type != "reply" || protocol.Decode(m.Payload, &out) != nil {
			return errors.New("invalid link receipt")
		}
		if out.State != "ready" {
			return errors.New(out.Error)
		}
		for _, f := range g.Files {
			a, e := os.Stat(filepath.Join(o.Root, stage, f.Name))
			if e != nil {
				return e
			}
			b, e := os.Stat(filepath.Join(o.HostRoot, "generations", g.Key(), f.Name))
			if e != nil || !os.SameFile(a, b) {
				return errors.New("helper link inode mismatch")
			}
		}
		return nil
	}
	result, e := PrepareLocked(ctx, o, exchange)
	if e != nil {
		return e
	}
	local, e := loadLocal(o.Root)
	if e != nil {
		return e
	}
	// A missing/truncated inventory is not evidence that backing links are gone.
	// Prove coverage of actual content while these stopped locks remain held.
	known := inventoryKnown(ctx, o, local, exchange)
	if !known {
		result.Skipped = append(result.Skipped, Outcome{Reason: "unknown local inventory; existing pins retained; reconcile surviving workshop links under stopped sync"})
	}
	if e = volumehelper.Write(conn, "result", struct {
		Result         Result
		Items          map[string]Generation
		InventoryKnown bool
		Error          string
	}{result, local.Items, known, ""}); e != nil {
		return e
	}
	m, e := volumehelper.Read(conn)
	if e == nil && m.Type != "release" {
		e = errors.New("missing private release")
	}
	return e
}
func (s *Store) linkStage(srv node.Server, g Generation, stage string) error {
	if !strings.HasPrefix(stage, "egg/.workshop-stage-") || len(stage) != len("egg/.workshop-stage-")+32 || !storeio.Digest(stage[len(stage)-32:]+stage[len(stage)-32:]) {
		return errors.New("invalid fixed helper stage")
	}
	known, e := s.Generation(g.ItemID, g.Revision)
	if e != nil || known.Digest != g.Digest {
		return errors.New("unrecognized link generation")
	}
	d, e := artifact.OpenDirectory(filepath.Join(srv.Volume, stage))
	if e != nil {
		return e
	}
	defer d.Close()
	info, e := d.Stat()
	if e != nil || info.Sys().(*syscall.Stat_t).Uid != srv.Helper.UID || int(info.Sys().(*syscall.Stat_t).Gid) != srv.Helper.GID || info.Mode().Perm() != 0770 {
		return errors.New("helper staging ownership/mode mismatch")
	}
	source, e := artifact.OpenDirectory(filepath.Join(s.root, "generations", known.Key()))
	if e != nil {
		return e
	}
	defer source.Close()
	for _, f := range known.Files {
		src, e := artifact.OpenRegular(source, f.Name, syscall.O_RDONLY, 0)
		if e != nil {
			return e
		}
		info, statErr := src.Stat()
		if statErr != nil {
			src.Close()
			return statErr
		}
		st := info.Sys().(*syscall.Stat_t)
		if st.Uid != uint32(os.Geteuid()) || st.Uid == srv.Helper.UID || info.Mode().Perm()&0222 != 0 {
			src.Close()
			return errors.New("hardlink needs readonly daemon-owned source and distinct runtime UID")
		}
		e = os.Link(fmt.Sprintf("/proc/self/fd/%d/%s", source.Fd(), f.Name), fmt.Sprintf("/proc/self/fd/%d/%s", d.Fd(), f.Name))
		src.Close()
		if e != nil {
			return e
		}
	}
	return d.Sync()
}
