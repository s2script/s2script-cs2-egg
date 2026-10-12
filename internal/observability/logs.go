// SPDX-License-Identifier: GPL-3.0-or-later
package observability

import (
	"context"
	"errors"
	"github.com/s2script/s2script-cs2-egg/internal/artifact"
	"github.com/s2script/s2script-cs2-egg/internal/cleanup"
	"github.com/s2script/s2script-cs2-egg/internal/storeio"
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"syscall"
	"time"
)

type LogOptions struct {
	FileBytes int64 `json:"file_bytes"`
	Files     int   `json:"files"`
	TailBytes int   `json:"tail_bytes"`
}

func (o *LogOptions) Validate() error {
	if o.FileBytes == 0 {
		o.FileBytes = 16 << 20
	}
	if o.Files == 0 {
		o.Files = 6
	}
	if o.TailBytes == 0 {
		o.TailBytes = 256 << 10
	}
	if o.FileBytes < 64 || o.FileBytes > 64<<20 || o.Files < 2 || o.Files > 24 || o.TailBytes < 32 || o.TailBytes > 1<<20 {
		return errors.New("invalid log bounds")
	}
	return nil
}

var safeID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,63}$`)

type logFile struct {
	f    *os.File
	size int64
	tail []byte
	root string
}
type Logs struct {
	mu      sync.Mutex
	root    string
	options LogOptions
	secrets []string
	files   map[string]*logFile
	closed  bool
}

func NewLogs(root string, o LogOptions, secrets []string) (*Logs, error) {
	if e := o.Validate(); e != nil {
		return nil, e
	}
	return &Logs{root: root, options: o, secrets: append([]string(nil), secrets...), files: map[string]*logFile{}}, nil
}
func (l *Logs) file(id string) (*logFile, error) {
	if !safeID.MatchString(id) {
		return nil, errors.New("invalid registered server ID")
	}
	if f := l.files[id]; f != nil {
		return f, nil
	}
	root := filepath.Join(l.root, id, "logs")
	if e := cleanup.Initialize(root, "logs"); e != nil {
		return nil, e
	}
	d, e := artifact.OpenDirectory(root)
	if e != nil {
		return nil, e
	}
	defer d.Close()
	if old, e := artifact.OpenRegular(d, ".active", syscall.O_RDONLY, 0); e == nil {
		info, statErr := old.Stat()
		old.Close()
		if statErr != nil || info.Size() > l.options.FileBytes {
			return nil, errors.New("recovered active log exceeds configured bound")
		}
		name := storeio.ID() + ".log"
		if e = artifact.Rename(d, ".active", name); e != nil {
			return nil, e
		}
		if e = cleanup.Seal(root, name); e != nil {
			return nil, e
		}
	} else if !errors.Is(e, os.ErrNotExist) {
		return nil, e
	}
	f, e := artifact.OpenRegular(d, ".active", syscall.O_CREAT|syscall.O_EXCL|syscall.O_WRONLY, 0600)
	if e != nil {
		return nil, e
	}
	v := &logFile{f: f, root: root}
	l.files[id] = v
	return v, l.retain(v, l.options.Files-1)
}
func (l *Logs) retain(f *logFile, n int) error {
	p, e := cleanup.Plan(f.root, []cleanup.Rule{{Pattern: "*.log", MaxRetained: n}}, time.Now())
	if e != nil {
		return e
	}
	_, e = cleanup.Apply(context.Background(), f.root, p)
	return e
}
func (l *Logs) seal(f *logFile) error {
	if f.f == nil {
		return nil
	}
	e := errors.Join(f.f.Sync(), f.f.Close())
	f.f = nil
	if e != nil {
		return e
	}
	d, e := artifact.OpenDirectory(f.root)
	if e != nil {
		return e
	}
	defer d.Close()
	name := storeio.ID() + ".log"
	if e = artifact.Rename(d, ".active", name); e != nil {
		return e
	}
	return cleanup.Seal(f.root, name)
}
func (l *Logs) Append(id string, p []byte) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return errors.New("logs closed")
	}
	f, e := l.file(id)
	if e != nil {
		return e
	}
	b := []byte(Redact(string(p), l.secrets))
	if int64(len(b)) > l.options.FileBytes {
		b = b[:l.options.FileBytes]
	}
	if f.size+int64(len(b)) > l.options.FileBytes {
		if e = l.seal(f); e != nil {
			return e
		}
		d, e := artifact.OpenDirectory(f.root)
		if e != nil {
			return e
		}
		f.f, e = artifact.OpenRegular(d, ".active", syscall.O_CREAT|syscall.O_EXCL|syscall.O_WRONLY, 0600)
		d.Close()
		if e != nil {
			return e
		}
		f.size = 0
		if e = l.retain(f, l.options.Files-1); e != nil {
			return e
		}
	}
	n, e := f.f.Write(b)
	f.size += int64(n)
	f.tail = append(f.tail, b[:n]...)
	if len(f.tail) > l.options.TailBytes {
		f.tail = append([]byte(nil), f.tail[len(f.tail)-l.options.TailBytes:]...)
	}
	return e
}
func (l *Logs) Tail(id string) []byte {
	l.mu.Lock()
	defer l.mu.Unlock()
	if f := l.files[id]; f != nil {
		return append([]byte(nil), f.tail...)
	}
	return nil
}
func (l *Logs) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.closed = true
	var e error
	for _, f := range l.files {
		e = errors.Join(e, l.seal(f), l.retain(f, l.options.Files))
	}
	return e
}
