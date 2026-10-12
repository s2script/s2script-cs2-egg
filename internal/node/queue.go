// SPDX-License-Identifier: GPL-3.0-or-later
package node

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/s2script/s2script-cs2-egg/internal/storeio"
	"io"
	"os"
	"path/filepath"
	"sort"
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
	Host          bool          `json:"host,omitempty"`
	RequestDigest string        `json:"request_digest,omitempty"`
	Digest        string        `json:"digest"`
	Event         *events.Event `json:"event,omitempty"`
	Outcome       string        `json:"outcome,omitempty"`
}
type ownerMeta struct {
	Server    string `json:"server"`
	Run       string `json:"run"`
	Revision  string `json:"revision"`
	Finalized bool   `json:"finalized,omitempty"`
}
type archiveRef struct {
	Name   string `json:"name"`
	Digest string `json:"digest"`
	Bytes  int64  `json:"bytes"`
}
type deliveryState struct {
	Meta     map[string]ownerMeta `json:"owner_meta,omitempty"`
	Archives []archiveRef         `json:"archives,omitempty"`

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
	c.lockHeld = true
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
	if json.Unmarshal(b, &c.state) != nil || c.state.Version != 2 || c.state.NodeID != c.cfg.ID || c.state.Owners == nil || c.state.Records == nil || len(c.state.Owners) > c.cfg.Limits.Runs || len(c.state.Records) > c.cfg.Limits.Records+2048 {
		return errors.New("invalid durable state")
	}
	pending := 0
	for _, r := range c.state.Records {
		if r.Event != nil {
			pending++
		}
	}
	if pending > c.cfg.Limits.Pending+256 {
		return errors.New("durable pending limit exceeded")
	}
	_, _, _, _, e = c.lookup("", "")
	return e
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
	s := deliveryState{Version: 2, NodeID: c.cfg.ID, Owners: map[string]string{}, Meta: map[string]ownerMeta{}, Records: map[string]deliveryRecord{}, Archives: append([]archiveRef(nil), c.state.Archives...)}
	for k, v := range c.state.Owners {
		s.Owners[k] = v
	}
	for k, v := range c.state.Meta {
		s.Meta[k] = v
	}
	for k, v := range c.state.Records {
		s.Records[k] = v
	}
	return s
}
func (c *Core) touch(k string) {
	if f := c.fences[k]; f != nil {
		f.valid = false
	}
}
func (c *Core) claim(id, run string) (string, error) {
	if run == "" || len(run) > 128 || !c.safeIdentifiers(id, run) {
		return "", errors.New("invalid run identity")
	}
	if _, ok := c.servers[id]; !ok {
		return "", errors.New("unknown server")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.storageFailed {
		return "", errors.New("durable storage unavailable")
	}
	k := key(id, run)
	owner, _, _, _, e := c.lookup(k, "")
	if e != nil {
		return "", e
	}
	c.touch(k)
	if owner != "" {
		return owner, nil
	}
	if len(c.state.Owners) >= c.cfg.Limits.Runs || len(c.state.Meta) >= c.cfg.Limits.Runs {
		return "", errors.New("run ownership capacity exceeded; archive completed evidence")
	}
	owner = "standalone"
	if c.delivery != nil {
		owner = "node"
	}
	s := c.copyState()
	s.Owners[k] = owner
	s.Meta[k] = ownerMeta{Server: id, Run: run, Revision: storeio.ID()}
	if e = c.save(s); e != nil {
		return "", e
	}
	c.statuses[id] = server.Status{RunID: run, State: "unknown", Versions: map[string]string{}}
	return owner, nil
}
func (c *Core) admit(id string, e events.Event) (string, error) {
	return c.admitSource(id, e, "container_report")
}
func (c *Core) admitSource(id string, e events.Event, source string) (string, error) {
	e.ServerID = id
	e.NodeID = c.cfg.ID
	if !c.safeIdentifiers(e.ServerID, e.NodeID, e.ID, e.RunID, e.CorrelationID) {
		return "", errors.New("secret-bearing event identifier refused")
	}
	if _, ok := c.servers[id]; !ok {
		return "", errors.New("unknown server")
	}
	if e.Version != 1 || e.Origin != "" || e.Producer != "" || e.ActionID != "" || e.RelatedRunID != "" || e.ID == "" || len(e.ID) > 128 || e.RunID == "" || len(e.RunID) > 128 || e.CorrelationID != e.RunID || e.At.IsZero() {
		return "", errors.New("invalid runtime event")
	}
	b, err := json.Marshal(e)
	if err != nil || len(b) > maxEventBytes {
		return "", errors.New("event exceeds 16 KiB")
	}
	digest := key(string(b))
	e = c.cleanEvent(e)
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.storageFailed {
		return "", errors.New("durable storage unavailable")
	}
	rk, k := key(id, e.RunID), key(id, e.RunID, e.ID)
	owner, meta, old, found, err := c.lookup(rk, k)
	if err != nil {
		return "", err
	}
	if owner == "" {
		return "", errors.New("run ownership is not negotiated")
	}
	c.touch(rk)
	if found {
		if old.Digest != digest {
			return "", errors.New("event ID was reused with different content")
		}
		c.broadcast(e, source, "durable_duplicate")
		return "acknowledged", nil
	}
	if meta.Finalized {
		return "", errors.New("run is finalized; novel events rejected")
	}
	replay, err := c.window(k, digest)
	if err != nil {
		return "", err
	}
	c.broadcast(e, source, replay)
	if owner == "node" && c.delivery == nil {
		return "", errors.New("node delivery capability unavailable; ownership retained")
	}
	if owner != "node" || !c.delivery.Accepts(e) {
		return "filtered", nil
	}
	return c.enqueue(k, deliveryRecord{Digest: digest, Event: &e})
}
func (c *Core) enqueue(k string, r deliveryRecord) (string, error) {
	count, pending := 0, 0
	for _, v := range c.state.Records {
		if v.Host == r.Host {
			count++
			if v.Event != nil {
				pending++
			}
		}
	}
	recordLimit, pendingLimit := c.cfg.Limits.Records, c.cfg.Limits.Pending
	if r.Host {
		recordLimit, pendingLimit = 2048, 256
	}
	if count >= recordLimit || pending >= pendingLimit {
		return "", errors.New("durable event capacity exceeded; archive completed evidence")
	}
	s := c.copyState()
	s.Records[k] = r
	if e := c.save(s); e != nil {
		return "", e
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
	runtimeKeys, hostKeys := []string{}, []string{}
	for k, r := range pending {
		if r.Host {
			hostKeys = append(hostKeys, k)
		} else {
			runtimeKeys = append(runtimeKeys, k)
		}
	}
	sort.Strings(runtimeKeys)
	sort.Strings(hostKeys)
	order := []string{}
	for i := 0; i < len(runtimeKeys) || i < len(hostKeys); i++ {
		if i < len(runtimeKeys) {
			order = append(order, runtimeKeys[i])
		}
		if i < len(hostKeys) {
			order = append(order, hostKeys[i])
		}
	}
	for _, k := range order {
		r := pending[k]
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
