// SPDX-License-Identifier: GPL-3.0-or-later
package server

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/s2script/s2script-cs2-egg/internal/runtimelease"
)

type Status struct {
	SampleAt           time.Time         `json:"sample_at,omitempty"`
	ReceivedAt         time.Time         `json:"received_at,omitempty"`
	ScheduledActionID  string            `json:"scheduled_action_id,omitempty"`
	OperatorVeto       bool              `json:"operator_veto,omitempty"`
	UpdatePending      string            `json:"update_pending,omitempty"`
	RunID              string            `json:"run_id"`
	State              string            `json:"state"`
	Ready              bool              `json:"ready"`
	Map                string            `json:"map,omitempty"`
	Players            Players           `json:"players"`
	Versions           map[string]string `json:"versions"`
	IntendedStopReason string            `json:"intended_stop_reason,omitempty"`
}

var ErrNotRunning = errors.New("managed runtime is not running")

type statusStore struct {
	mu     sync.Mutex
	status Status
}

func (s *statusStore) snapshot() Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	snapshot := s.status
	snapshot.Versions = map[string]string{}
	for k, v := range s.status.Versions {
		snapshot.Versions[k] = v
	}
	return snapshot
}
func (s *statusStore) update(f func(*Status)) { s.mu.Lock(); defer s.mu.Unlock(); f(&s.status) }
func (s *statusStore) serve(root string) (func(), error) {
	path := filepath.Join(root, ".s2egg-status.sock")
	if info, e := os.Lstat(path); e == nil {
		if info.Mode()&os.ModeSocket == 0 {
			return nil, errors.New("status path must be a socket")
		}
		if e = os.Remove(path); e != nil {
			return nil, e
		}
	} else if !errors.Is(e, os.ErrNotExist) {
		return nil, e
	}
	l, e := net.Listen("unix", path)
	if e != nil {
		return nil, e
	}
	if e = os.Chmod(path, 0600); e != nil {
		l.Close()
		return nil, e
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			c, e := l.Accept()
			if e != nil {
				return
			}
			c.SetWriteDeadline(time.Now().Add(time.Second))
			s.mu.Lock()
			json.NewEncoder(c).Encode(s.status)
			s.mu.Unlock()
			c.Close()
		}
	}()
	return func() { l.Close(); <-done }, nil
}

// Snapshot validates the live runtime lease before querying the owner socket.
// It never trusts a PID file or an on-disk snapshot as evidence of liveness.
func Snapshot(ctx context.Context, root string) (Status, error) {
	release, e := runtimelease.Acquire(root)
	if e == nil {
		release()
		return Status{}, ErrNotRunning
	}
	if !errors.Is(e, runtimelease.ErrHeld) {
		return Status{}, e
	}
	ctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	c, e := (&net.Dialer{}).DialContext(ctx, "unix", filepath.Join(root, ".s2egg-status.sock"))
	if e != nil {
		return Status{}, errors.New("runtime lease is held; status unavailable")
	}
	defer c.Close()
	d, _ := ctx.Deadline()
	c.SetReadDeadline(d)
	stop := context.AfterFunc(ctx, func() { c.Close() })
	defer stop()
	var s Status
	e = json.NewDecoder(io.LimitReader(c, 64<<10)).Decode(&s)
	if e != nil {
		return Status{}, errors.New("invalid live runtime status")
	}
	return s, nil
}

func serverIdentity(root, configured string) (string, error) {
	if configured != "" {
		return configured, nil
	}
	dir, e := os.OpenRoot(root)
	if e != nil {
		return "", e
	}
	defer dir.Close()
	if e = dir.MkdirAll("egg", 0700); e != nil {
		return "", e
	}
	const path = "egg/server-id"
	if info, e := dir.Lstat(path); e == nil {
		if !info.Mode().IsRegular() {
			return "", errors.New("server identity must be a regular file")
		}
		f, e := dir.Open(path)
		if e != nil {
			return "", e
		}
		defer f.Close()
		b, e := io.ReadAll(io.LimitReader(f, 33))
		if e != nil || len(b) != 32 {
			return "", errors.New("invalid saved server identity")
		}
		if _, e = hex.DecodeString(string(b)); e != nil {
			return "", errors.New("invalid saved server identity")
		}
		return string(b), nil
	} else if !errors.Is(e, os.ErrNotExist) {
		return "", e
	}
	id := randomID()
	f, e := dir.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if e != nil {
		return "", e
	}
	_, e = io.WriteString(f, id)
	e = errors.Join(e, f.Sync(), f.Close())
	if e != nil {
		return "", e
	}
	return id, nil
}
