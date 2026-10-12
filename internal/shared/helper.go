// SPDX-License-Identifier: GPL-3.0-or-later
package shared

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/s2script/s2script-cs2-egg/internal/artifact"
	node "github.com/s2script/s2script-cs2-egg/internal/nodetypes"
	"github.com/s2script/s2script-cs2-egg/internal/protocol"
	"github.com/s2script/s2script-cs2-egg/internal/runtimelease"
	"github.com/s2script/s2script-cs2-egg/internal/volumehelper"
	"github.com/s2script/s2script-cs2-egg/internal/workshop"
)

// HelperConfig is an operator-owned, fixed-volume contract. The service runs as
// RuntimeUID with AccessGID as a supplementary group, without any capabilities.
// The socket and configuration must be outside all container mounts.
type HelperConfig struct {
	WorkshopRoot  string   `json:"workshop_root,omitempty"`
	WorkshopMode  string   `json:"workshop_mode,omitempty"`
	WorkshopMount string   `json:"workshop_mount,omitempty"`
	WorkshopItems []string `json:"workshop_items,omitempty"`
	Volume        string   `json:"volume"`
	SharedRoot    string   `json:"shared_root"`
	Socket        string   `json:"socket"`
	NodeUID       uint32   `json:"node_uid"`
	RuntimeUID    uint32   `json:"runtime_uid"`
	AccessGID     int      `json:"access_gid"`
	Overrides     []string `json:"overrides"`
}

func LoadHelperConfig(path string) (HelperConfig, error) {
	var c HelperConfig
	d, e := artifact.OpenDirectory(filepath.Dir(path))
	if e != nil {
		return c, e
	}
	defer d.Close()
	f, e := artifact.OpenRegular(d, filepath.Base(path), syscall.O_RDONLY, 0)
	if e != nil {
		return c, e
	}
	info, e := f.Stat()
	f.Close()
	if e != nil || info.Sys().(*syscall.Stat_t).Uid != 0 || info.Mode().Perm()&022 != 0 {
		return c, errors.New("helper configuration must be operator/root-owned and not group/world writable")
	}
	if e = readJSON(filepath.Dir(path), filepath.Base(path), &c); e != nil {
		return c, e
	}
	if e = c.validate(); e != nil {
		return c, e
	}
	for p := filepath.Dir(path); p != "/"; p = filepath.Dir(p) {
		info, e := os.Stat(p)
		if e != nil || info.Sys().(*syscall.Stat_t).Uid != 0 || info.Mode().Perm()&022 != 0 {
			return c, errors.New("helper configuration ancestors must be operator-owned and not writable by runtime/daemon")
		}
	}
	return c, nil
}
func (c HelperConfig) validate() error {
	if c.Volume == "" || c.Socket == "" || c.SharedRoot == "" && c.WorkshopRoot == "" {
		return errors.New("fixed helper volume/socket/store required")
	}
	for _, p := range []string{c.Volume, c.SharedRoot, c.WorkshopRoot, c.Socket} {
		if p == "" {
			continue
		}
		if !filepath.IsAbs(p) || filepath.Clean(p) != p || p == "/" {
			return errors.New("helper paths must be fixed clean absolute paths")
		}
	}
	if c.RuntimeUID != uint32(os.Geteuid()) || c.RuntimeUID == 0 || c.NodeUID == c.RuntimeUID || c.AccessGID < 0 {
		return errors.New("helper needs actual non-root volume UID, distinct daemon UID and configured common GID")
	}
	overlap := func(a, b string) bool { return a == b || strings.HasPrefix(a, b+"/") || strings.HasPrefix(b, a+"/") }
	if c.SharedRoot != "" && (overlap(c.Volume, c.SharedRoot) || overlap(c.Socket, c.SharedRoot)) || overlap(c.Socket, c.Volume) || c.WorkshopRoot != "" && (overlap(c.Volume, c.WorkshopRoot) || overlap(c.Socket, c.WorkshopRoot)) {
		return errors.New("helper socket/store must be outside the private volume")
	}
	for _, p := range []string{c.Volume, c.SharedRoot, c.WorkshopRoot} {
		if p == "" {
			continue
		}
		d, e := artifact.OpenDirectory(p)
		if e != nil {
			return e
		}
		info, e := d.Stat()
		d.Close()
		if e != nil {
			return e
		}
		uid := info.Sys().(*syscall.Stat_t).Uid
		if p == c.Volume && uid != c.RuntimeUID || p != c.Volume && uid != c.NodeUID {
			return errors.New("helper volume/store ownership does not match configured UIDs")
		}
	}
	return nil
}
func RunHelper(ctx context.Context, c HelperConfig) error {
	if e := c.validate(); e != nil {
		return e
	}
	l, e := net.Listen("unix", c.Socket)
	if e != nil {
		return fmt.Errorf("helper socket unavailable (operator must resolve stale socket safely): %w", e)
	}
	defer l.Close()
	defer os.Remove(c.Socket)
	if e = os.Chown(c.Socket, -1, c.AccessGID); e != nil {
		return e
	}
	if e = os.Chmod(c.Socket, 0660); e != nil {
		return e
	}
	return ServeHelper(ctx, l, c)
}
func peerUID(conn net.Conn) (uint32, error) {
	u, ok := conn.(*net.UnixConn)
	if !ok {
		return 0, errors.New("Unix peer required")
	}
	raw, e := u.SyscallConn()
	if e != nil {
		return 0, e
	}
	var cred *syscall.Ucred
	var inner error
	e = raw.Control(func(fd uintptr) {
		cred, inner = syscall.GetsockoptUcred(int(fd), syscall.SOL_SOCKET, syscall.SO_PEERCRED)
	})
	if e != nil {
		return 0, e
	}
	if inner != nil {
		return 0, inner
	}
	return cred.Uid, nil
}

type helperRequest struct {
	Generation  string `json:"generation"`
	Digest      string `json:"digest"`
	Mode        string `json:"mode"`
	Reservation string `json:"reservation"`
}
type helperReply struct {
	Dependencies []string `json:"dependencies,omitempty"`
	Stage        string   `json:"stage,omitempty"`
	Generation   string   `json:"generation,omitempty"`
	Error        string   `json:"error,omitempty"`
}

func helperWrite(c net.Conn, kind string, v any) error {
	b, e := json.Marshal(v)
	if e != nil {
		return e
	}
	return protocol.Write(c, protocol.Message{Version: protocol.Version, ID: "attach", Type: kind, Payload: b})
}
func helperRead(c net.Conn, kind string, v any) error {
	m, e := protocol.Read(c)
	if e != nil {
		return e
	}
	if m.Version != protocol.Version || m.ID != "attach" || m.Type != kind {
		return errors.New("invalid helper session phase")
	}
	return protocol.Decode(m.Payload, v)
}
func readReply(c net.Conn) (helperReply, error) {
	var r helperReply
	e := helperRead(c, "result", &r)
	if e == nil && r.Error != "" {
		e = errors.New(r.Error)
	}
	return r, e
}

// ServeHelper is the typed, fixed-volume session boundary. It is reusable by
// later backup/restore operations without adding generic command/path execution.
// RunHelper performs production provisioning validation before calling it.
func ServeHelper(ctx context.Context, l net.Listener, c HelperConfig) error {
	stop := context.AfterFunc(ctx, func() { l.Close() })
	defer stop()
	for {
		conn, e := l.Accept()
		if e != nil {
			if ctx.Err() != nil {
				return nil
			}
			return e
		}
		func() {
			defer conn.Close()
			uid, e := peerUID(conn)
			if e != nil || uid != c.NodeUID {
				return
			}
			session, cancel := context.WithTimeout(ctx, 30*time.Minute)
			defer cancel()
			stop := context.AfterFunc(session, func() { conn.Close() })
			defer stop()
			conn.SetDeadline(time.Now().Add(30 * time.Minute))
			if e = helperSession(session, conn, c); e != nil {
				_ = helperWrite(conn, "result", helperReply{Error: e.Error()})
			}
		}()
	}
}
func helperSession(ctx context.Context, conn net.Conn, c HelperConfig) error {
	var req helperRequest
	first, e := protocol.Read(conn)
	if e != nil {
		return e
	}
	if first.Version == protocol.Version && first.ID == "workshop" && first.Type == "begin-workshop" {
		if c.WorkshopRoot == "" {
			return volumehelper.Write(conn, "result", struct{ Error string }{"workshop helper not configured"})
		}
		var selection struct {
			Mode string `json:"mode"`
		}
		if protocol.Decode(first.Payload, &selection) != nil {
			return volumehelper.Write(conn, "result", struct{ Error string }{"invalid workshop selection"})
		}
		mode := c.WorkshopMode
		if mode == "" {
			mode = "copy"
		}
		if selection.Mode != mode {
			return volumehelper.Write(conn, "result", struct{ Error string }{"node and helper workshop modes differ"})
		}

		e = workshop.ServePrivate(ctx, conn, workshop.LocalOptions{Root: c.Volume, HostRoot: c.WorkshopRoot, Mode: c.WorkshopMode, Mount: c.WorkshopMount, Items: c.WorkshopItems, GID: c.AccessGID})
		if e != nil {
			return volumehelper.Write(conn, "result", struct{ Error string }{e.Error()})
		}
		return nil
	}
	if first.Version != protocol.Version || first.ID != "attach" || first.Type != "begin" || protocol.Decode(first.Payload, &req) != nil {
		return errors.New("invalid helper begin")
	}
	if c.SharedRoot == "" {
		return errors.New("shared helper not configured")
	}
	if req.Mode != "copy" && req.Mode != "symlink" && req.Mode != "hardlink" {
		return errors.New("unsupported attachment mode")
	}
	m, e := LoadManifest(c.SharedRoot, req.Generation, req.Digest)
	if e != nil {
		return e
	}
	release, e := runtimelease.Acquire(c.Volume)
	if e != nil {
		return fmt.Errorf("cannot acquire private stopped runtime lease; provision correct helper UID and stop managed game: %w", e)
	}
	defer release()
	unlock, e := artifact.Acquire(c.Volume)
	if e != nil {
		return e
	}
	defer unlock()
	if e = artifact.Recover(c.Volume); e != nil {
		return e
	}
	if p, e := ReadPending(c.Volume); e == nil && p.Generation.ID != req.Generation {
		return errors.New("helper must resume the pending generation first")
	} else if e != nil && !errors.Is(e, os.ErrNotExist) {
		return e
	}
	stage := ""
	if req.Mode == "hardlink" {
		stage = "egg/.s2shared-links-" + randomID()
		root, e := artifact.OpenDirectory(c.Volume)
		if e != nil {
			return e
		}
		defer root.Close()
		parent, name, e := artifact.ParentDirectory(root, stage, false)
		if e != nil {
			return e
		}
		defer parent.Close()
		if e = syscall.Mkdirat(int(parent.Fd()), name, 0700); e != nil {
			return e
		}
		defer os.RemoveAll(filepath.Join(c.Volume, stage))
		d, e := artifact.OpenDirectory(filepath.Join(c.Volume, stage))
		if e != nil {
			return e
		}
		e = errors.Join(d.Chown(-1, c.AccessGID), d.Chmod(0770), d.Sync(), d.Close())
		if e != nil {
			return fmt.Errorf("common stage GID not provisioned: %w", e)
		}
	}
	if e = helperWrite(conn, "result", helperReply{Stage: stage}); e != nil {
		return e
	}
	if e = helperRead(conn, "materialize", &struct{}{}); e != nil {
		return e
	}
	if e = materializeLocked(ctx, c.Volume, c.SharedRoot, m.Generation, req.Reservation, req.Mode, c.Overrides, stage, MountRoot); e != nil {
		return e
	}
	local, e := ReadLocal(c.Volume)
	if e != nil {
		return e
	}
	if e = helperWrite(conn, "result", helperReply{Generation: m.Generation.ID, Dependencies: local.Dependencies}); e != nil {
		return e
	} // Keep stopped locks through the daemon's durable reconciliation.
	return helperRead(conn, "release", &struct{}{})
}

// Attach is the administrative entrypoint, never called from GameProvider.
// Protocol status cannot authorize it: the server-UID helper owns the locks.
func (s *Store) Attach(ctx context.Context, srv node.Server, generationID, mode string) error {
	ep, ok := s.options.Helpers[srv.ID]
	if !ok || ep.Socket == "" {
		return errors.New("administrative attachment requires an explicitly provisioned per-server UID helper socket")
	}
	m, e := LoadManifest(s.root, generationID, "")
	if e != nil {
		return e
	}
	ref, e := s.reserve(ctx, srv.ID, "admin-"+randomID(), randomID(), generationID)
	if e != nil {
		return e
	}
	// Capture before begin: a buffered receipt may arrive after helper death and
	// a newer run acquiring the lease. Such a run must never lose its references.
	fence, e := s.captureReconciliation(srv.ID, ref.ID)
	if e != nil {
		return e
	}
	conn, e := (&net.Dialer{}).DialContext(ctx, "unix", ep.Socket)
	if e != nil {
		return fmt.Errorf("private helper unavailable; provision its service/socket: %w", e)
	}
	defer conn.Close()
	uid, e := peerUID(conn)
	if e != nil || uid != ep.UID {
		return errors.New("helper peer UID does not match provisioned runtime UID")
	}
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	conn.SetDeadline(time.Now().Add(30 * time.Minute))
	if e = helperWrite(conn, "begin", helperRequest{Generation: generationID, Digest: m.Generation.Digest, Mode: mode, Reservation: ref.ID}); e != nil {
		return e
	}
	r, e := readReply(conn)
	if e != nil {
		return e
	}
	if mode == "hardlink" {
		if e = s.linkStage(srv, ep, m, r.Stage); e != nil {
			return e
		}
	} else if r.Stage != "" {
		return errors.New("unexpected helper stage")
	}
	if e = helperWrite(conn, "materialize", struct{}{}); e != nil {
		return e
	}
	r, e = readReply(conn)
	if e != nil {
		return e
	}
	if r.Generation != generationID {
		return errors.New("helper installed a different generation")
	}
	if e = s.Commit(ctx, srv.ID, ref.ID); e != nil {
		return e
	}
	unlock, e := artifact.Acquire(s.root)
	if e != nil {
		return e
	}
	e = s.reconcile(fence, r.Dependencies)
	unlock()
	if e != nil {
		return e
	}
	return helperWrite(conn, "release", struct{}{})
}
func (s *Store) linkStage(srv node.Server, ep HelperEndpoint, m Manifest, stage string) error {
	prefix := "egg/.s2shared-links-"
	if !strings.HasPrefix(stage, prefix) || !validID.MatchString(strings.TrimPrefix(stage, prefix)) {
		return errors.New("invalid helper stage")
	}
	d, e := artifact.OpenDirectory(filepath.Join(srv.Volume, stage))
	if e != nil {
		return e
	}
	defer d.Close()
	info, e := d.Stat()
	if e != nil {
		return e
	}
	stat := info.Sys().(*syscall.Stat_t)
	if stat.Uid != ep.UID || stat.Gid != uint32(ep.GID) || info.Mode().Perm() != 0770 {
		return errors.New("helper stage owner/group/mode differs from provisioning")
	}
	source, e := artifact.OpenDirectory(m.Generation.Root)
	if e != nil {
		return e
	}
	defer source.Close()
	for i, f := range m.Files {
		if !sharedVPK(f.Path) {
			continue
		}
		p, n, e := artifact.ParentDirectory(source, f.Path, false)
		if e != nil {
			return e
		}
		file, e := artifact.OpenRegular(source, f.Path, syscall.O_RDONLY, 0)
		if e != nil {
			p.Close()
			return e
		}
		info, e := file.Stat()
		file.Close()
		if e != nil {
			p.Close()
			return e
		}
		st := info.Sys().(*syscall.Stat_t)
		if st.Uid != uint32(os.Geteuid()) || st.Uid == ep.UID || info.Mode().Perm()&0222 != 0 {
			p.Close()
			return errors.New("hardlink source must be read-only, daemon-owned and owned by a different UID than the runtime")
		}
		e = linkImmutable(p, n, d, i)
		p.Close()
		if e != nil {
			return fmt.Errorf("hardlink unavailable (EXDEV across mounts or protected_hardlinks/permissions); select explicit copy or symlink mode: %w", e)
		}
	}
	return d.Sync()
}

func linkImmutable(parent *os.File, name string, stage *os.File, index int) error {
	return os.Link(fmt.Sprintf("/proc/self/fd/%d/%s", parent.Fd(), name), fmt.Sprintf("/proc/self/fd/%d/%d", stage.Fd(), index))
}
