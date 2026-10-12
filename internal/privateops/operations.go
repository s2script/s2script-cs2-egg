// SPDX-License-Identifier: GPL-3.0-or-later
// Package privateops implements closed fixed-volume operations as the volume UID.
package privateops

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"github.com/s2script/s2script-cs2-egg/internal/artifact"
	"github.com/s2script/s2script-cs2-egg/internal/cleanup"
	"github.com/s2script/s2script-cs2-egg/internal/protocol"
	"github.com/s2script/s2script-cs2-egg/internal/runtimelease"
	"github.com/s2script/s2script-cs2-egg/internal/storeio"
	"github.com/s2script/s2script-cs2-egg/internal/volumehelper"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

type Policy struct {
	Enabled        bool   `json:"enabled"`
	Root           string `json:"root"`
	ServerID       string `json:"server_id"`
	DumpSource     string `json:"dump_source"`
	Dumps          bool   `json:"dumps"`
	DumpBytes      int64  `json:"dump_bytes"`
	DumpTotalBytes int64  `json:"dump_total_bytes"`
	DumpFiles      int    `json:"dump_files"`
}

func (p *Policy) Validate() error {
	if !p.Enabled {
		return errors.New("private operations disabled")
	}
	if !filepath.IsAbs(p.Root) || filepath.Clean(p.Root) != p.Root || p.Root == "/" {
		return errors.New("fixed private operations root required")
	}
	if p.DumpBytes == 0 {
		p.DumpBytes = 512 << 20
	}
	if p.DumpTotalBytes == 0 {
		p.DumpTotalBytes = 2 << 30
	}
	if p.DumpFiles == 0 {
		p.DumpFiles = 4
	}
	if p.DumpBytes < 1024 || p.DumpBytes > 2<<30 || p.DumpTotalBytes < p.DumpBytes || p.DumpTotalBytes > 8<<30 || p.DumpFiles < 1 || p.DumpFiles > 16 {
		return errors.New("invalid dump retention bounds")
	}
	if p.Dumps && (p.DumpSource == "" || filepath.IsAbs(p.DumpSource) || filepath.Clean(p.DumpSource) != p.DumpSource || strings.Contains(p.DumpSource, "..") || strings.ContainsAny(p.DumpSource, "\\\x00:")) {
		return errors.New("fixed contained dump source required")
	}
	return nil
}

type Request struct {
	RequestID  string `json:"request_id,omitempty"`
	Operation  string `json:"operation"`
	ID         string `json:"id,omitempty"`
	Namespace  string `json:"namespace,omitempty"`
	Nonce      string `json:"nonce,omitempty"`
	Offset     int64  `json:"offset,omitempty"`
	Limit      int    `json:"limit,omitempty"`
	Retain     int    `json:"retain,omitempty"`
	AgeSeconds int64  `json:"age_seconds,omitempty"`
}
type Reply struct {
	CompletedAt time.Time           `json:"completed_at,omitempty"`
	State       string              `json:"state,omitempty"`
	Error       string              `json:"error,omitempty"`
	Nonce       string              `json:"nonce,omitempty"`
	ID          string              `json:"id,omitempty"`
	Skipped     string              `json:"skipped,omitempty"`
	Sensitive   bool                `json:"sensitive,omitempty"`
	Size        int64               `json:"size,omitempty"`
	Digest      string              `json:"digest,omitempty"`
	Data        []byte              `json:"data,omitempty"`
	Entries     []cleanup.Candidate `json:"entries,omitempty"`
	Plan        *cleanup.Preview    `json:"plan,omitempty"`
	Result      *cleanup.Result     `json:"result,omitempty"`
}

func Serve(ctx context.Context, conn net.Conn, volume string, p Policy, first protocol.Message) error {
	wire := volumehelper.Session{Conn: conn, Family: "operations"}
	var r Request
	if first.Version != protocol.Version || first.ID != "operations" || first.Type != "begin" || protocol.Decode(first.Payload, &r) != nil {
		return errors.New("invalid operations begin")
	}
	if e := p.Validate(); e != nil {
		return wire.Write("result", Reply{Error: e.Error()})
	}
	if r.Operation == "finalize" {
		if len(r.Nonce) != 32 {
			return wire.Write("result", Reply{Error: "invalid finalization nonce"})
		}
		u, e := runtimelease.Acquire(volume)
		if e != nil {
			return wire.Write("result", Reply{Error: "managed runtime not confirmed stopped"})
		}
		defer u()
		a, e := artifact.Acquire(volume)
		if e != nil {
			return e
		}
		defer a()
		if e = wire.Write("result", Reply{Nonce: r.Nonce}); e != nil {
			return e
		}
		for {
			msg, e := protocol.Read(conn)
			if e != nil {
				return e
			}
			if msg.Version != protocol.Version || msg.ID != "operations" {
				return errors.New("invalid stopped phase")
			}
			switch msg.Type {
			case "check":
				if e = wire.Write("held", Reply{Nonce: r.Nonce}); e != nil {
					return e
				}
			case "release":
				return nil
			default:
				return errors.New("invalid stopped phase")
			}
		}
	}
	out, e := Execute(ctx, volume, p, r)
	if e != nil {
		if out.Error == "" {
			out.Error = "private operation refused: " + e.Error()
		}
	}
	return wire.Write("result", out)
}
func Call(ctx context.Context, socket string, uid uint32, r Request) (Reply, error) {
	var out Reply
	c, e := volumehelper.Dial(ctx, socket, uid)
	if e != nil {
		return out, e
	}
	defer c.Close()
	stop := context.AfterFunc(ctx, func() { c.Close() })
	defer stop()
	w := volumehelper.Session{Conn: c, Family: "operations"}
	if e = w.Write("begin", r); e == nil {
		e = w.Read("result", &out)
	}
	if e == nil && out.Error != "" {
		e = errors.New(out.Error)
	}
	return out, e
}

// ErrStoppedUncertain means the helper was lost after commit entry. The caller
// must retain any committed fence and original owner; never roll back/elect anew.
var ErrStoppedUncertain = errors.New("stopped authority or commit outcome uncertain; inspect permanent fence and helper health before retry")

func Stopped(ctx context.Context, socket string, uid uint32, nonce string, finish func() error) error {
	c, e := volumehelper.Dial(ctx, socket, uid)
	if e != nil {
		return e
	}
	defer c.Close()
	var phase sync.Mutex
	committing := false
	stop := context.AfterFunc(ctx, func() {
		phase.Lock()
		defer phase.Unlock()
		if !committing {
			c.Close()
		}
	})
	defer stop()
	w := volumehelper.Session{Conn: c, Family: "operations"}
	if e = w.Write("begin", Request{Operation: "finalize", Nonce: nonce}); e != nil {
		return e
	}
	var out Reply
	if e = w.Read("result", &out); e != nil {
		return e
	}
	if out.Error != "" || out.Nonce != nonce {
		return errors.New("stopped helper proof unavailable")
	}
	check := func() error {
		if e := w.Write("check", struct{}{}); e != nil {
			return e
		}
		var held Reply
		if e := w.Read("held", &held); e != nil {
			return e
		}
		if held.Nonce != nonce {
			return errors.New("stopped nonce mismatch")
		}
		return nil
	}
	if e = check(); e != nil {
		return e
	}
	phase.Lock()
	if e = ctx.Err(); e != nil {
		phase.Unlock()
		return e
	}
	committing = true
	phase.Unlock()
	// Caller cancellation cannot release authority once commit has entered. A
	// helper crash is still a distributed uncertainty, not an atomic transaction.
	commitErr := finish()
	if e = check(); e != nil {
		return errors.Join(ErrStoppedUncertain, commitErr)
	}
	if e = w.Write("release", struct{}{}); e != nil {
		return errors.Join(ErrStoppedUncertain, commitErr)
	}
	return commitErr
}

func namespace(volume string, p Policy, name string) (string, error) {
	switch name {
	case "dumps":
		return filepath.Join(p.Root, "dumps"), nil
	case "logs", "crashes":
		if p.ServerID == "" || filepath.Base(p.ServerID) != p.ServerID || p.ServerID == "." || p.ServerID == ".." {
			return "", errors.New("fixed standalone server identity not configured")
		}
		return filepath.Join(volume, "egg", "observability", p.ServerID, name), nil
	}
	return "", errors.New("unsupported private namespace")
}
func Execute(ctx context.Context, volume string, p Policy, r Request) (Reply, error) {
	out := Reply{}
	if e := p.Validate(); e != nil {
		return out, e
	}
	if r.Operation == "dump-retain" || r.Operation == "cleanup-apply" {
		u, e := runtimelease.Acquire(volume)
		if e != nil {
			return out, errors.New("managed runtime not confirmed stopped")
		}
		defer u()
		a, e := artifact.Acquire(volume)
		if e != nil {
			return out, e
		}
		defer a()
	}
	if r.Operation == "dump-retain" {
		return retainRequest(ctx, volume, p, r, false)
	}
	if r.Operation == "dump-result" {
		return retainRequest(ctx, volume, p, r, true)
	}
	if r.Namespace == "" {
		r.Namespace = "dumps"
	}
	root, e := namespace(volume, p, r.Namespace)
	if e != nil {
		return out, e
	}
	switch r.Operation {
	case "list":
		entries, e := cleanup.Catalog(root)
		if e != nil {
			return out, e
		}
		if r.Offset < 0 || r.Limit < 0 || r.Limit > 128 {
			return out, errors.New("invalid list bounds")
		}
		limit := r.Limit
		if limit == 0 {
			limit = 128
		}
		start := int(r.Offset)
		if start > len(entries) {
			start = len(entries)
		}
		end := start + limit
		if end > len(entries) {
			end = len(entries)
		}
		out.Entries = entries[start:end]
		out.Sensitive = r.Namespace == "dumps"
		return out, nil
	case "read":
		return ReadArtifact(root, r, r.Namespace == "dumps")
	case "cleanup-result":
		result, done, e := cleanup.PlanResult(root, r.ID)
		out.Result = &result
		out.State = "pending"
		if done {
			out.State = "complete"
		}
		return out, e
	case "cleanup-plan":
		if r.Retain < 0 || r.Retain > 4096 || r.AgeSeconds < 0 || r.AgeSeconds > 315360000 {
			return out, errors.New("invalid retention bounds")
		}
		plan, e := cleanup.PreviewPlan(ctx, root, []cleanup.Rule{{Pattern: "*", MaxRetained: r.Retain, Age: time.Duration(r.AgeSeconds) * time.Second}})
		out.Plan = &plan
		return out, e
	case "cleanup-apply":
		result, e := cleanup.ApplyPlan(ctx, root, r.ID)
		out.Result = &result
		return out, e
	}
	return out, errors.New("unsupported private operation")
}
func ReadArtifact(root string, r Request, sensitive bool) (Reply, error) {
	out := Reply{Sensitive: sensitive}
	if r.Offset < 0 || r.Limit < 1 || r.Limit > 64<<10 {
		return out, errors.New("read bounds require 1..65536 bytes")
	}
	entries, e := cleanup.Catalog(root)
	if e != nil {
		return out, e
	}
	var found *cleanup.Candidate
	for _, v := range entries {
		if v.ID == r.ID {
			v := v
			found = &v
			break
		}
	}
	if found == nil {
		return out, errors.New("unknown sealed artifact ID")
	}
	d, e := artifact.OpenDirectory(root)
	if e != nil {
		return out, e
	}
	defer d.Close()
	f, e := artifact.OpenRegular(d, found.Path, syscall.O_RDONLY, 0)
	if e != nil {
		return out, e
	}
	defer f.Close()
	info, e := f.Stat()
	if e != nil {
		return out, e
	}
	st := info.Sys().(*syscall.Stat_t)
	if st.Ino != found.Inode || uint64(st.Dev) != found.Device || st.Size != found.Size || st.Mtim != found.MTime || st.Ctim != found.CTime || st.Nlink != 1 {
		return out, errors.New("sealed artifact changed")
	}
	if r.Offset > st.Size {
		return out, errors.New("offset exceeds artifact")
	}
	b := make([]byte, r.Limit)
	n, e := f.ReadAt(b, r.Offset)
	if e != nil && e != io.EOF {
		return out, e
	}
	out.ID = r.ID
	out.Size = st.Size
	out.Data = b[:n]
	if sensitive {
		out.Digest = strings.TrimSuffix(found.Path, ".dmp")
	}
	return out, nil
}
func retainDump(ctx context.Context, volume string, p Policy, name string, txn *dumpRecord, persist func() error) (Reply, error) {
	out := Reply{Sensitive: true}
	if !p.Dumps {
		return out, errors.New("binary dump retention is disabled")
	}
	if name == "" || filepath.Base(name) != name || name == "." || name == ".." || (!strings.HasSuffix(name, ".dmp") && !strings.HasPrefix(name, "core")) {
		return out, errors.New("invalid configured-source dump name")
	}
	source, e := artifact.OpenDirectory(filepath.Join(volume, p.DumpSource))
	if e != nil {
		return out, e
	}
	defer source.Close()
	in, e := artifact.OpenRegular(source, name, syscall.O_RDONLY, 0)
	if e != nil {
		return out, e
	}
	defer in.Close()
	before, e := in.Stat()
	if e != nil {
		return out, e
	}
	st := before.Sys().(*syscall.Stat_t)
	if st.Nlink != 1 || st.Uid != uint32(os.Geteuid()) {
		return out, errors.New("dump must be private single-link owned file")
	}
	fingerprint := sourceIdentity{Dev: uint64(st.Dev), Inode: st.Ino, Size: st.Size, MTime: st.Mtim, CTime: st.Ctim}
	if txn.Source == nil {
		txn.Source = &fingerprint
		if e = persist(); e != nil {
			return out, e
		}
	} else if *txn.Source != fingerprint {
		return out, errors.New("original dump source changed; request will not be rebound")
	}
	if before.Size() > p.DumpBytes {
		out.Skipped = "oversized dump; not truncated"
		return out, nil
	}
	root := filepath.Join(p.Root, "dumps")
	if e = cleanup.Initialize(root, "dumps"); e != nil {
		return out, e
	}
	entries, e := cleanup.Catalog(root)
	if e != nil {
		return out, e
	}
	// Hash before reserving space so a durable retry succeeds even at quota.
	// Read twice from the pinned descriptor; verify the content again while copying.
	expected := sha256.New()
	buffer := make([]byte, 64<<10)
	var hashed int64
	for {
		if e = ctx.Err(); e != nil {
			return out, e
		}
		n, re := in.Read(buffer)
		hashed += int64(n)
		if hashed > p.DumpBytes {
			return Reply{Sensitive: true, Skipped: "oversized dump; not truncated"}, nil
		}
		expected.Write(buffer[:n])
		if re == io.EOF {
			break
		}
		if re != nil {
			return out, re
		}
	}
	expectedDigest := hex.EncodeToString(expected.Sum(nil))
	if txn.Digest == "" {
		txn.Digest = expectedDigest
		txn.Size = hashed
		if e = persist(); e != nil {
			return out, e
		}
	} else if txn.Digest != expectedDigest || txn.Size != hashed {
		return out, errors.New("original dump content changed")
	}

	for _, v := range entries {
		if v.Path == expectedDigest+".dmp" && v.Size == hashed {
			return Reply{Sensitive: true, ID: v.ID, Size: v.Size, Digest: expectedDigest}, nil
		}
	}
	if _, e = in.Seek(0, io.SeekStart); e != nil {
		return out, e
	}
	// Interrupted copies and unpublished completed files consume disk quota too.
	// They are never age-deleted: inspection/recovery is a separate operator action.
	files, e := os.ReadDir(root)
	if e != nil {
		return out, e
	}
	var total int64
	count := 0
	for _, v := range files {
		if !strings.HasPrefix(v.Name(), ".dump-") && !strings.HasSuffix(v.Name(), ".dmp") {
			continue
		}
		info, e := v.Info()
		if e != nil {
			return out, e
		}
		if !info.Mode().IsRegular() {
			return out, errors.New("invalid retained dump entry")
		}
		total += info.Size()
		count++
	}
	if count >= p.DumpFiles || total+before.Size() > p.DumpTotalBytes {
		return out, errors.New("sensitive dump retention exhausted; preview cleanup or inspect unpublished files")
	}
	d, e := artifact.OpenDirectory(root)
	if e != nil {
		return out, e
	}
	defer d.Close()
	tmp := ".dump-" + storeio.ID()
	f, e := artifact.OpenRegular(d, tmp, syscall.O_WRONLY|syscall.O_CREAT|syscall.O_EXCL, 0600)
	if e != nil {
		return out, e
	}
	defer artifact.Remove(d, tmp)
	h := sha256.New()
	buf := make([]byte, 64<<10)
	var written int64
	for {
		if e = ctx.Err(); e != nil {
			f.Close()
			return out, e
		}
		n, re := in.Read(buf)
		if n > 0 {
			written += int64(n)
			if written > p.DumpBytes {
				f.Close()
				return Reply{Sensitive: true, Skipped: "oversized dump; not truncated"}, nil
			}
			h.Write(buf[:n])
			if _, e = f.Write(buf[:n]); e != nil {
				f.Close()
				return out, e
			}
		}
		if re == io.EOF {
			break
		}
		if re != nil {
			f.Close()
			return out, re
		}
	}
	after, e := in.Stat()
	if e != nil || after.Sys().(*syscall.Stat_t).Ino != st.Ino || after.Sys().(*syscall.Stat_t).Dev != st.Dev || after.Sys().(*syscall.Stat_t).Size != st.Size || after.Sys().(*syscall.Stat_t).Mtim != st.Mtim || after.Sys().(*syscall.Stat_t).Ctim != st.Ctim || written != before.Size() {
		f.Close()
		return out, errors.New("dump changed during capture")
	}
	if e = errors.Join(f.Sync(), f.Close()); e != nil {
		return out, e
	}
	digest := hex.EncodeToString(h.Sum(nil))
	if digest != expectedDigest {
		return out, errors.New("dump content changed during capture")
	}
	dest := digest + ".dmp"
	if previous, e := artifact.OpenRegular(d, dest, syscall.O_RDONLY, 0); e == nil {
		previous.Close()
		return out, errors.New("unpublished destination requires inspected recovery")
	} else if errors.Is(e, os.ErrNotExist) {
		if e = artifact.Rename(d, tmp, dest); e != nil {
			return out, e
		}
	} else {
		return out, e
	}
	if e = cleanup.Seal(root, dest); e != nil {
		return out, e
	}
	entries, e = cleanup.Catalog(root)
	for _, v := range entries {
		if v.Path == dest {
			out.ID = v.ID
		}
	}
	out.Size = written
	out.Digest = digest
	return out, e
}
