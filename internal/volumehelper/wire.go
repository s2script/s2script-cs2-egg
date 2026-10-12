// SPDX-License-Identifier: GPL-3.0-or-later
// Package volumehelper contains only authenticated framing; operations stay closed and typed.
package volumehelper

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/s2script/s2script-cs2-egg/internal/protocol"
	"net"
	"syscall"
	"time"
)

func Peer(conn net.Conn) (uint32, error) {
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
func Dial(ctx context.Context, socket string, uid uint32) (net.Conn, error) {
	c, e := (&net.Dialer{}).DialContext(ctx, "unix", socket)
	if e != nil {
		return nil, e
	}
	got, e := Peer(c)
	if e != nil || got != uid {
		c.Close()
		return nil, errors.New("private helper UID mismatch")
	}
	c.SetDeadline(time.Now().Add(30 * time.Minute))
	return c, nil
}
func Write(c net.Conn, kind string, v any) error {
	b, e := json.Marshal(v)
	if e != nil {
		return e
	}
	return protocol.Write(c, protocol.Message{Version: protocol.Version, ID: "workshop", Type: kind, Payload: b})
}
func Read(c net.Conn) (protocol.Message, error) {
	m, e := protocol.Read(c)
	if e == nil && (m.Version != protocol.Version || m.ID != "workshop") {
		e = errors.New("invalid workshop helper framing")
	}
	return m, e
}

// Session preserves legacy workshop framing while new closed operations use a
// distinct fixed family. The caller authenticates Peer before constructing it.
type Session struct {
	Conn   net.Conn
	Family string
}

func (s Session) Write(kind string, v any) error {
	if s.Family != "operations" && s.Family != "backup" {
		return errors.New("unsupported helper family")
	}
	b, e := json.Marshal(v)
	if e != nil {
		return e
	}
	return protocol.Write(s.Conn, protocol.Message{Version: protocol.Version, ID: s.Family, Type: kind, Payload: b})
}
func (s Session) Read(kind string, v any) error {
	m, e := protocol.Read(s.Conn)
	if e != nil {
		return e
	}
	if m.Version != protocol.Version || m.ID != s.Family || m.Type != kind {
		return errors.New("invalid private helper phase")
	}
	return protocol.Decode(m.Payload, v)
}
