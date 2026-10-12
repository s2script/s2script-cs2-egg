// SPDX-License-Identifier: GPL-3.0-or-later
package backup

import (
	"context"
	"encoding/json"
	"errors"
	node "github.com/s2script/s2script-cs2-egg/internal/nodetypes"
	"github.com/s2script/s2script-cs2-egg/internal/protocol"
	"github.com/s2script/s2script-cs2-egg/internal/storeio"
	"github.com/s2script/s2script-cs2-egg/internal/volumehelper"
	"net"
	"os"
	"path/filepath"
	"time"
)

type Request struct {
	Operation string `json:"operation"`
	ServerID  string `json:"server_id,omitempty"`
	RequestID string `json:"request_id,omitempty"`
	ID        string `json:"id,omitempty"`
	Confirm   string `json:"confirm,omitempty"`
	Mode      string `json:"mode,omitempty"`
	Offset    int    `json:"offset,omitempty"`
	Document  string `json:"document,omitempty"`
}
type Summary struct {
	ID, Mode, Digest string
	CreatedAt        time.Time
	Bytes            int64
	Files            int
}
type Reply struct {
	ID, State, Error string
	CompletedAt      time.Time
	Entries          []Summary `json:"entries,omitempty"`
	Data             []byte    `json:"data,omitempty"`
	Next             int       `json:"next,omitempty"`
}
type operationRecord struct {
	Input Request
	Reply Reply
}

func summary(m Manifest) Summary {
	return Summary{m.BackupID, m.Mode, m.ArchiveSHA256, m.CreatedAt, m.ArchiveBytes, len(m.Files)}
}
func Serve(ctx context.Context, c net.Conn, volume string, p Policy, first protocol.Message) error {
	session := volumehelper.Session{Conn: c, Family: "backup"}
	var req Request
	var reply Reply
	if e := protocol.Decode(first.Payload, &req); e != nil {
		reply.Error = "invalid backup request"
	} else {
		s, e := Open(volume, p)
		if e != nil {
			reply.Error = e.Error()
		} else {
			reply, e = s.Handle(ctx, req)
			if e != nil {
				reply.Error = e.Error()
			}
		}
	}
	return session.Write("result", reply)
}
func (s *Store) Handle(ctx context.Context, r Request) (Reply, error) {
	if r.ServerID != "" && r.ServerID != s.policy.ServerID {
		return Reply{}, errors.New("wrong configured backup server")
	}
	if len(r.ID) > 128 || len(r.RequestID) > 128 || len(r.Confirm) > 64 || r.Offset < 0 {
		return Reply{}, errors.New("backup request bounds")
	}
	switch r.Operation {
	case "list":
		list, e := s.List()
		if e != nil {
			return Reply{}, e
		}
		if r.Offset > len(list) {
			return Reply{}, errors.New("invalid list offset")
		}
		out := Reply{}
		end := min(r.Offset+64, len(list))
		for _, m := range list[r.Offset:end] {
			out.Entries = append(out.Entries, summary(m))
		}
		if end < len(list) {
			out.Next = end
		}
		return out, nil
	case "document":
		if !identifier(r.ID) {
			return Reply{}, errors.New("invalid document ID")
		}
		var rel string
		var bound int64
		switch r.Document {
		case "manifest":
			rel = "archives/" + r.ID + "/manifest.json"
			bound = 16 << 20
		case "plan":
			rel = "plans/" + r.ID + ".json"
			bound = 32 << 20
		default:
			return Reply{}, errors.New("invalid document kind")
		}
		b, e := storeio.Read(s.policy.Root, rel, bound)
		if e != nil {
			return Reply{}, e
		}
		if r.Offset > len(b) {
			return Reply{}, errors.New("invalid document offset")
		}
		end := min(r.Offset+64<<10, len(b))
		out := Reply{Data: b[r.Offset:end]}
		if end < len(b) {
			out.Next = end
		}
		return out, nil
	case "status":
		var v operationRecord
		if !storeio.Digest(r.ID) {
			return Reply{}, errors.New("invalid operation ID")
		}
		e := readJSON(s.policy.Root, "receipts/"+r.ID+".json", &v, 256<<10)
		if e != nil {
			return Reply{}, e
		}
		return s.recoverOperation(v), nil
	case "create", "preview", "restore", "recovery-release":
		return s.operation(ctx, r)
	}
	return Reply{}, errors.New("unknown typed backup operation")
}
func (s *Store) recoverOperation(v operationRecord) Reply {
	if v.Reply.State != "executing" {
		return v.Reply
	}
	if v.Input.Operation == "restore" {
		if saved, e := s.Status(v.Input.ID); e == nil && saved.State != "executing" {
			v.Reply.State = saved.State
			v.Reply.CompletedAt = saved.CompletedAt
			v.Reply.Error = saved.Error
			return v.Reply
		}
	}
	v.Reply.State = "uncertain"
	v.Reply.Error = "operation interrupted; inspect catalog and recovery; work is never replayed"
	return v.Reply
}
func (s *Store) operation(ctx context.Context, r Request) (Reply, error) {
	if !identifier(r.RequestID) {
		return Reply{}, errors.New("stable bounded request_id required")
	}
	key := storeio.Hash([]byte(s.policy.ServerID + "\x00" + r.RequestID))
	unlock, e := storeio.Lock(ctx, filepath.Join(s.policy.Root, "receipts"))
	if e != nil {
		return Reply{}, e
	}
	defer unlock()
	var rec operationRecord
	e = readJSON(s.policy.Root, "receipts/"+key+".json", &rec, 256<<10)
	if e == nil {
		b, _ := json.Marshal(rec.Input)
		want, _ := json.Marshal(r)
		if string(b) != string(want) {
			return Reply{}, errors.New("backup request identity conflict")
		}
		return s.recoverOperation(rec), nil
	}
	if !errors.Is(e, os.ErrNotExist) {
		return Reply{}, e
	}
	entries, e := os.ReadDir(filepath.Join(s.policy.Root, "receipts"))
	if e != nil {
		return Reply{}, e
	}
	if len(entries) >= 512 {
		return Reply{}, errors.New("helper operation receipt quota; preserve/rotate catalog with operator review")
	}
	rec = operationRecord{Input: r, Reply: Reply{ID: key, State: "executing"}}
	if e = storeio.Save(s.policy.Root, "receipts/"+key+".json", rec); e != nil {
		return Reply{}, e
	}
	switch r.Operation {
	case "create":
		var m Manifest
		m, e = s.Create(ctx, Options{Mode: r.Mode})
		if m.BackupID != "" && m.ArchiveSHA256 != "" {
			rec.Reply.Entries = []Summary{summary(m)}
		}
	case "preview":
		var p RestorePlan
		p, e = s.Preview(ctx, r.ID)
		if e == nil {
			rec.Reply.Entries = []Summary{{ID: p.ID, Digest: p.PlanDigest, CreatedAt: p.CreatedAt, Files: len(p.Targets)}}
		}
	case "restore":
		e = s.Restore(ctx, r.ID, r.Confirm)
	case "recovery-release":
		e = s.ReleaseRecovery(ctx, r.ID, r.Confirm)
	}
	rec.Reply.State = "complete"
	rec.Reply.CompletedAt = time.Now().UTC()
	if e != nil {
		rec.Reply.State = "failed"
		rec.Reply.Error = e.Error()
		if r.Operation == "restore" {
			saved, se := s.Status(r.ID)
			if se == nil && (saved.State == "uncertain" || saved.State == "executing") {
				rec.Reply.State = "uncertain"
			}
		}
	}
	if e = storeio.Save(s.policy.Root, "receipts/"+key+".json", rec); e != nil {
		return Reply{}, errors.New("operation outcome awaiting recovery; inspect status; do not replay")
	}
	return rec.Reply, nil
}
func Call(ctx context.Context, srv node.Server, r Request) (Reply, error) {
	if srv.Helper == nil {
		return Reply{}, errors.New("backup requires configured server-UID helper")
	}
	r.ServerID = srv.ID
	c, e := volumehelper.Dial(ctx, srv.Helper.Socket, srv.Helper.UID)
	if e != nil {
		return Reply{}, e
	}
	defer c.Close()
	stop := context.AfterFunc(ctx, func() { c.Close() })
	defer stop()
	session := volumehelper.Session{Conn: c, Family: "backup"}
	if e = session.Write("begin", r); e != nil {
		return Reply{}, e
	}
	var out Reply
	if e = session.Read("result", &out); e != nil {
		return out, e
	}
	if out.Error != "" {
		return out, errors.New(out.Error)
	}
	return out, nil
}
func document(ctx context.Context, srv node.Server, kind, id string) ([]byte, error) {
	var b []byte
	offset := 0
	for {
		r, e := Call(ctx, srv, Request{Operation: "document", ID: id, Document: kind, Offset: offset})
		if e != nil {
			return nil, e
		}
		b = append(b, r.Data...)
		if len(b) > 32<<20 {
			return nil, errors.New("backup document exceeds limit")
		}
		if r.Next == 0 {
			return b, nil
		}
		if r.Next <= offset || len(r.Data) == 0 {
			return nil, errors.New("invalid backup page")
		}
		offset = r.Next
	}
}
func Create(ctx context.Context, srv node.Server, o Options) (Manifest, error) {
	var m Manifest
	if o.OutputDirectory != "" || len(o.Include) > 0 || len(o.Exclude) > 0 || o.Retention != (Retention{}) || o.MinFreeBytes != 0 {
		return m, errors.New("configure selection/output/retention in fixed helper policy")
	}
	r, e := Call(ctx, srv, Request{Operation: "create", Mode: o.Mode, RequestID: storeio.ID()})
	if e != nil {
		return m, e
	}
	if len(r.Entries) != 1 {
		return m, errors.New("backup creation has no sealed result")
	}
	b, e := document(ctx, srv, "manifest", r.Entries[0].ID)
	if e != nil {
		return m, e
	}
	e = json.Unmarshal(b, &m)
	return m, e
}
func PreviewRestore(ctx context.Context, srv node.Server, archivePath string) (RestorePlan, error) {
	var p RestorePlan
	if !identifier(archivePath) {
		return p, errors.New("select a catalog backup ID, not an arbitrary archive path")
	}
	r, e := Call(ctx, srv, Request{Operation: "preview", ID: archivePath, RequestID: storeio.ID()})
	if e != nil {
		return p, e
	}
	if len(r.Entries) != 1 {
		return p, errors.New("no persisted restore preview")
	}
	b, e := document(ctx, srv, "plan", r.Entries[0].ID)
	if e != nil {
		return p, e
	}
	e = json.Unmarshal(b, &p)
	return p, e
}
func Restore(ctx context.Context, srv node.Server, p RestorePlan) error {
	_, e := Call(ctx, srv, Request{Operation: "restore", ID: p.ID, Confirm: p.PlanDigest, RequestID: storeio.ID()})
	return e
}
