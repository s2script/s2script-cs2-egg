// SPDX-License-Identifier: GPL-3.0-or-later
package privateops

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/s2script/s2script-cs2-egg/internal/artifact"
	"github.com/s2script/s2script-cs2-egg/internal/cleanup"
	"github.com/s2script/s2script-cs2-egg/internal/storeio"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

type sourceIdentity struct {
	Dev, Inode   uint64
	Size         int64
	MTime, CTime syscall.Timespec
}
type dumpRecord struct {
	Input    string
	Source   *sourceIdentity
	Digest   string
	Size     int64
	Terminal bool
	Result   Reply
	Error    string
}

// The helper journal binds a request before reading/copying its source. It lives
// outside the volume and is never ordinary cleanup evidence.
func retainRequest(ctx context.Context, volume string, p Policy, r Request, query bool) (Reply, error) {
	if r.RequestID == "" || len(r.RequestID) > 128 {
		return Reply{}, errors.New("stable dump request_id required")
	}
	root := filepath.Join(p.Root, "requests")
	if e := os.MkdirAll(root, 0700); e != nil {
		return Reply{}, e
	}
	unlock, e := storeio.Lock(ctx, root)
	if e != nil {
		return Reply{}, e
	}
	defer unlock()
	canonical := r
	canonical.Operation = "dump-retain"
	b, _ := json.Marshal(struct {
		Volume  string
		Request Request
	}{volume, canonical})
	digest := storeio.Hash(b)
	name := dumpRecordName(volume, r.RequestID)
	var rec dumpRecord
	e = storeio.JSON(root, name, &rec)
	if e == nil {
		if rec.Input != digest {
			return Reply{}, errors.New("dump request identity conflict")
		}
	} else if !errors.Is(e, os.ErrNotExist) {
		return Reply{}, e
	} else {
		if query {
			return Reply{State: "absent"}, nil
		}
		files, e := os.ReadDir(root)
		if e != nil {
			return Reply{}, e
		}
		if len(files) >= 1024 {
			return Reply{}, errors.New("dump request evidence quota exhausted")
		}
		rec.Input = digest
		// Bind the first durable record to a concrete source before any copy occurs.
		source, sourceErr := snapshotSource(volume, p, r.ID)
		if sourceErr != nil {
			rec.Terminal = true
			rec.Error = "original dump source unavailable or invalid"
		} else {
			rec.Source = &source
		}
		if e = storeio.Save(root, name, rec); e != nil {
			return Reply{}, e
		}
	}
	save := func() error { return storeio.Save(root, name, rec) }
	if rec.Terminal {
		out := rec.Result
		if rec.Error != "" {
			out.State = "failed"
			out.Error = rec.Error
			return out, errors.New(rec.Error)
		}
		out.State = "complete"
		return out, nil
	}
	// A helper crash may follow seal but precede terminal journal replacement.
	// Recover the exact prepared content without consulting a replaced source.
	if rec.Digest != "" {
		entries, e := cleanup.Catalog(filepath.Join(p.Root, "dumps"))
		if e != nil && !errors.Is(e, os.ErrNotExist) {
			return Reply{}, e
		}
		for _, v := range entries {
			if v.Path == rec.Digest+".dmp" && v.Size == rec.Size {
				rec.Result = Reply{CompletedAt: time.Now().UTC(), Sensitive: true, ID: v.ID, Size: v.Size, Digest: rec.Digest}
				rec.Terminal = true
				if e = save(); e != nil {
					return Reply{}, e
				}
				out := rec.Result
				out.State = "complete"
				return out, nil
			}
		}
	}
	if query {
		return Reply{State: "pending"}, nil
	}
	out, workErr := retainDump(ctx, volume, p, r.ID, &rec, save)
	out.CompletedAt = time.Now().UTC()
	rec.Result = out
	rec.Terminal = true
	if workErr != nil {
		rec.Error = "dump retention failed; original request remains bound"
	}
	if e = save(); e != nil {
		return Reply{}, e
	}
	if workErr != nil {
		out.State = "failed"
		out.Error = rec.Error
		return out, errors.New(rec.Error)
	}
	out.State = "complete"
	return out, nil
}

func snapshotSource(volume string, p Policy, name string) (sourceIdentity, error) {
	var out sourceIdentity
	if !p.Dumps || name == "" || filepath.Base(name) != name || name == "." || name == ".." || (!strings.HasSuffix(name, ".dmp") && !strings.HasPrefix(name, "core")) {
		return out, errors.New("invalid dump source")
	}
	d, e := artifact.OpenDirectory(filepath.Join(volume, p.DumpSource))
	if e != nil {
		return out, e
	}
	defer d.Close()
	f, e := artifact.OpenRegular(d, name, syscall.O_RDONLY, 0)
	if e != nil {
		return out, e
	}
	defer f.Close()
	info, e := f.Stat()
	if e != nil {
		return out, e
	}
	st := info.Sys().(*syscall.Stat_t)
	if st.Nlink != 1 || st.Uid != uint32(os.Geteuid()) {
		return out, errors.New("unsafe dump source")
	}
	return sourceIdentity{Dev: uint64(st.Dev), Inode: st.Ino, Size: st.Size, MTime: st.Mtim, CTime: st.Ctim}, nil
}

func dumpRecordName(volume, request string) string {
	return storeio.Hash([]byte(volume+"\x00"+request)) + ".json"
}
