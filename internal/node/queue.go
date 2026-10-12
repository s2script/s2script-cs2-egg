// SPDX-License-Identifier: GPL-3.0-or-later
package node

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"github.com/s2script/s2script-cs2-egg/internal/alerts"
	"github.com/s2script/s2script-cs2-egg/internal/events"
	"github.com/s2script/s2script-cs2-egg/internal/server"
)

const maxStateBytes = 32 << 20
const maxEventBytes = 16 << 10

type deliveryRecord struct {
	Digest  string        `json:"digest"`
	Event   *events.Event `json:"event,omitempty"`
	Outcome string        `json:"outcome,omitempty"`
}
type deliveryState struct {
	Version int                       `json:"version"`
	NodeID  string                    `json:"node_id"`
	Owners  map[string]string         `json:"owners"`
	Records map[string]deliveryRecord `json:"records"`
}

func key(parts ...string) string {
	b, _ := json.Marshal(parts)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
func (c *Core) openState() error {
	if e := os.MkdirAll(c.cfg.StateDir, 0700); e != nil {
		return e
	}
	dir, e := openDirectory(c.cfg.StateDir)
	if e != nil {
		return e
	}
	c.stateDir = dir
	info, e := dir.Stat()
	if e != nil || info.Mode().Perm()&0077 != 0 {
		return errors.New("node state directory must be private (0700)")
	}
	fd, e := syscall.Openat(int(dir.Fd()), ".node-lock", syscall.O_RDWR|syscall.O_CREAT|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0600)
	if e != nil {
		return e
	}
	c.lock = os.NewFile(uintptr(fd), "node lock")
	if e = syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB); e != nil {
		return errors.New("node state is already in use")
	}
	path := c.statePath("delivery.json")
	if info, e := os.Lstat(path); e == nil && (!info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0) {
		return errors.New("delivery state must be a private regular file")
	}
	f, e := os.Open(path)
	if errors.Is(e, os.ErrNotExist) {
		return c.persist(c.state)
	}
	if e != nil {
		return e
	}
	defer f.Close()
	b, e := io.ReadAll(io.LimitReader(f, maxStateBytes+1))
	if e != nil || len(b) > maxStateBytes {
		return errors.New("invalid durable state size")
	}
	if json.Unmarshal(b, &c.state) != nil || c.state.Version != 1 || c.state.NodeID != c.cfg.ID || c.state.Owners == nil || c.state.Records == nil || len(c.state.Owners) > c.cfg.Limits.Runs || len(c.state.Records) > c.cfg.Limits.Records {
		return errors.New("invalid durable state")
	}
	pending := 0
	for _, r := range c.state.Records {
		if r.Event != nil {
			pending++
		}
	}
	if pending > c.cfg.Limits.Pending {
		return errors.New("durable pending limit exceeded")
	}
	return nil
}
func (c *Core) statePath(name string) string {
	return filepath.Join("/proc/self/fd", fmtFD(c.stateDir), name)
}
func fmtFD(f *os.File) string { return strconv.FormatUint(uint64(f.Fd()), 10) }
func (c *Core) persist(s deliveryState) error {
	b, e := json.Marshal(s)
	if e != nil || len(b) > maxStateBytes {
		return errors.New("durable state capacity exceeded")
	}
	path := c.statePath("delivery.tmp")
	fd, e := syscall.Openat(int(c.stateDir.Fd()), "delivery.tmp", syscall.O_WRONLY|syscall.O_CREAT|syscall.O_TRUNC|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0600)
	if e != nil {
		return e
	}
	f := os.NewFile(uintptr(fd), "delivery state")
	_, e = f.Write(b)
	e = errors.Join(e, f.Sync(), f.Close())
	if e != nil {
		return e
	}
	if e = os.Rename(path, c.statePath("delivery.json")); e != nil {
		return e
	}
	return c.stateDir.Sync()
}
func (c *Core) save(s deliveryState) error {
	if c.storageFailed {
		return errors.New("durable storage unavailable; restart required")
	}
	if e := c.persist(s); e != nil {
		c.storageFailed = true
		return errors.New("durable storage could not be acknowledged")
	}
	c.state = s
	return nil
}
func (c *Core) copyState() deliveryState {
	s := deliveryState{Version: 1, NodeID: c.cfg.ID, Owners: make(map[string]string, len(c.state.Owners)), Records: make(map[string]deliveryRecord, len(c.state.Records))}
	for k, v := range c.state.Owners {
		s.Owners[k] = v
	}
	for k, v := range c.state.Records {
		s.Records[k] = v
	}
	return s
}
func (c *Core) claim(id, run string) (string, error) {
	if run == "" || len(run) > 128 {
		return "", errors.New("invalid run identity")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.storageFailed {
		return "", errors.New("durable storage unavailable")
	}
	k := key(id, run)
	if owner, ok := c.state.Owners[k]; ok {
		return owner, nil
	}
	if len(c.state.Owners) >= c.cfg.Limits.Runs {
		return "", errors.New("run ownership capacity exceeded")
	}
	owner := "standalone"
	if c.delivery != nil {
		owner = "node"
	}
	s := c.copyState()
	s.Owners[k] = owner
	if e := c.save(s); e != nil {
		return "", e
	}
	c.statuses[id] = server.Status{RunID: run, State: "unknown", Versions: map[string]string{}}
	return owner, nil
}
func (c *Core) admit(id string, e events.Event) (string, error) {
	e.ServerID = id
	e.NodeID = c.cfg.ID
	if e.Version != 1 || e.ID == "" || len(e.ID) > 128 || e.RunID == "" || len(e.RunID) > 128 || e.CorrelationID != e.RunID || e.At.IsZero() {
		return "", errors.New("invalid event")
	}
	b, err := json.Marshal(e)
	if err != nil || len(b) > maxEventBytes {
		return "", errors.New("event exceeds 16 KiB")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.storageFailed {
		return "", errors.New("durable storage unavailable")
	}
	owner := c.state.Owners[key(id, e.RunID)]
	if owner == "" {
		return "", errors.New("run ownership is not negotiated")
	}
	if owner == "node" && c.delivery == nil {
		return "", errors.New("node delivery capability unavailable; ownership retained")
	}
	if owner != "node" || !c.delivery.Accepts(e) {
		return "filtered", nil
	}
	k := key(id, e.RunID, e.ID)
	digest := key(string(b))
	if old, ok := c.state.Records[k]; ok {
		if old.Digest != digest {
			return "", errors.New("event ID was reused with different content")
		}
		return "acknowledged", nil
	}
	pending := 0
	for _, r := range c.state.Records {
		if r.Event != nil {
			pending++
		}
	}
	if pending >= c.cfg.Limits.Pending || len(c.state.Records) >= c.cfg.Limits.Records {
		return "", errors.New("durable event capacity exceeded")
	}
	var copied events.Event
	_ = json.Unmarshal(b, &copied)
	s := c.copyState()
	s.Records[k] = deliveryRecord{Digest: digest, Event: &copied}
	if err = c.save(s); err != nil {
		return "", err
	}
	select {
	case c.wake <- struct{}{}:
	default:
	}
	return "acknowledged", nil
}
func (c *Core) deliveryWorker(ctx context.Context) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		c.deliverPending(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-c.wake:
		}
	}
}
func (c *Core) deliverPending(ctx context.Context) {
	if c.delivery == nil {
		return
	}
	c.mu.Lock()
	pending := map[string]deliveryRecord{}
	if !c.storageFailed {
		for k, r := range c.state.Records {
			if r.Event != nil {
				pending[k] = r
			}
		}
	}
	c.mu.Unlock()
	for k, r := range pending {
		if ctx.Err() != nil {
			return
		}
		request, cancel := context.WithTimeout(ctx, 30*time.Second)
		result, e := c.delivery.Deliver(request, *r.Event)
		cancel()
		if e != nil || (result != alerts.Delivered && result != alerts.Filtered) {
			continue
		}
		c.mu.Lock()
		s := c.copyState()
		r.Event = nil
		r.Outcome = string(result)
		s.Records[k] = r
		_ = c.save(s)
		c.mu.Unlock()
	}
}
