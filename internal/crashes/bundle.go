// SPDX-License-Identifier: GPL-3.0-or-later
package crashes

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/s2script/s2script-cs2-egg/internal/cleanup"
	node "github.com/s2script/s2script-cs2-egg/internal/nodetypes"
	"github.com/s2script/s2script-cs2-egg/internal/redaction"
	"github.com/s2script/s2script-cs2-egg/internal/server"
	"github.com/s2script/s2script-cs2-egg/internal/storeio"
	"path/filepath"
	"sync"
	"time"
)

type Options struct {
	Bytes    int64 `json:"bytes"`
	Retained int   `json:"retained"`
}

func (o *Options) Validate() error {
	if o.Bytes == 0 {
		o.Bytes = 2 << 20
	}
	if o.Retained == 0 {
		o.Retained = 20
	}
	if o.Bytes < 1024 || o.Bytes > 8<<20 || o.Retained < 1 || o.Retained > 80 {
		return errors.New("invalid crash bundle bounds")
	}
	return nil
}

type Store struct {
	mu      sync.Mutex
	root    string
	options Options
	secrets []string
}

func New(root string, o Options, secrets []string) (*Store, error) {
	if e := o.Validate(); e != nil {
		return nil, e
	}
	return &Store{root: root, options: o, secrets: append([]string(nil), secrets...)}, nil
}

// Capture is the local-UID convenience API for already-redacted status/tail.
// Configured callers use New with their secret set and bounds; the node uses a
// Store outside volumes. Neither API accepts binary dumps.
func Capture(ctx context.Context, srv node.Server, status server.Status, tail []byte) (string, error) {
	s, e := New(filepath.Join(srv.Volume, "egg", "observability"), Options{}, nil)
	if e != nil {
		return "", e
	}
	return s.Capture(ctx, srv, status, tail)
}
func (s *Store) Capture(ctx context.Context, srv node.Server, status server.Status, tail []byte) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e := ctx.Err(); e != nil {
		return "", e
	}
	if srv.ID == "" || filepath.Base(srv.ID) != srv.ID || srv.ID == "." || srv.ID == ".." {
		return "", errors.New("invalid server identity")
	}
	root := filepath.Join(s.root, srv.ID, "crashes")
	if e := cleanup.Initialize(root, "crashes"); e != nil {
		return "", e
	}
	versions := map[string]string{}
	for _, k := range []string{"cs2", "steam_build", "s2script", "metamod", "mam"} {
		if v := status.Versions[k]; v != "" {
			versions[k] = redaction.Redact(v, s.secrets)
		}
	}
	clean := redaction.Redact(string(tail), s.secrets)
	if len(clean) > 256<<10 {
		clean = clean[len(clean)-(256<<10):]
	}
	bundle := struct {
		Server, Run string
		At          time.Time
		Versions    map[string]string
		Tail        string
		TailQuality string
	}{srv.ID, redaction.Redact(status.RunID, s.secrets), time.Now().UTC(), versions, clean, "bounded best effort; observation drops and terminal reordering are possible"}
	b, e := json.Marshal(bundle)
	if e != nil {
		return "", e
	}
	for int64(len(b)) > s.options.Bytes && len(bundle.Tail) > 0 {
		bundle.Tail = bundle.Tail[len(bundle.Tail)/2+1:]
		b, e = json.Marshal(bundle)
	}
	if e != nil || int64(len(b)) > s.options.Bytes {
		return "", errors.New("crash metadata exceeds bound")
	}
	name := storeio.ID() + ".json"
	if e = storeio.Write(root, name, b, 0600); e != nil {
		return "", e
	}
	if e = cleanup.Seal(root, name); e != nil {
		return "", e
	}
	p, e := cleanup.Plan(root, []cleanup.Rule{{Pattern: "*.json", MaxRetained: s.options.Retained}}, time.Now())
	if e != nil {
		return "", e
	}
	if _, e = cleanup.Apply(ctx, root, p); e != nil {
		return "", e
	}
	return filepath.Join(root, name), nil
}
