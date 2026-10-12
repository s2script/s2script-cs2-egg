// SPDX-License-Identifier: GPL-3.0-or-later
package server

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"net"
	"strings"
	"time"
	"unicode/utf8"
)

type Players struct {
	Known bool `json:"known"`
	Count int  `json:"count"`
}
type queryStatus struct {
	Map     string
	Players Players
	Version string
}

var errQuery = errors.New("invalid or unsupported app 730 query response")

// parseInfo accepts only a complete, unsplit Source A2S_INFO response. A2S
// proves the engine answers for a loaded map, not native/plugin load success.
func parseInfo(b []byte) (out queryStatus, err error) {
	fail := func() (queryStatus, error) { return queryStatus{}, errQuery }
	if len(b) < 6 || !bytes.Equal(b[:5], []byte{255, 255, 255, 255, 0x49}) || b[5] != 17 {
		return fail()
	}
	b = b[6:]
	str := func() (string, bool) {
		i := bytes.IndexByte(b, 0)
		if i < 0 || i > 1024 {
			return "", false
		}
		s := string(b[:i])
		b = b[i+1:]
		return s, utf8.ValidString(s) && !strings.ContainsAny(s, "\r\n")
	}
	if _, ok := str(); !ok {
		return fail()
	}
	m, ok := str()
	if !ok || strings.TrimSpace(m) == "" {
		return fail()
	}
	if _, ok = str(); !ok {
		return fail()
	}
	if _, ok = str(); !ok {
		return fail()
	}
	if len(b) < 9 {
		return fail()
	}
	app := binary.LittleEndian.Uint16(b)
	players, maxPlayers, bots := b[2], b[3], b[4]
	if app != 730 || b[5] != 'd' || b[6] != 'l' || players > maxPlayers || bots > players || b[7] > 1 || b[8] > 1 {
		return fail()
	}
	b = b[9:]
	version, ok := str()
	if !ok || version == "" {
		return fail()
	}
	take := func(n int) bool {
		if len(b) < n {
			return false
		}
		b = b[n:]
		return true
	}
	if len(b) > 0 {
		edf := b[0]
		b = b[1:]
		if edf & ^byte(0xf1) != 0 {
			return fail()
		}
		if edf&0x80 != 0 && !take(2) {
			return fail()
		}
		if edf&0x10 != 0 && !take(8) {
			return fail()
		}
		if edf&0x40 != 0 {
			if !take(2) {
				return fail()
			}
			if _, ok = str(); !ok {
				return fail()
			}
		}
		if edf&0x20 != 0 {
			if _, ok = str(); !ok {
				return fail()
			}
		}
		if edf&0x01 != 0 {
			if len(b) < 8 || binary.LittleEndian.Uint64(b[:8])&0xffffff != 730 {
				return fail()
			}
			b = b[8:]
		}
	}
	if len(b) != 0 {
		return fail()
	}
	return queryStatus{Map: m, Players: Players{Known: true, Count: int(players)}, Version: version}, nil
}

func query(ctx context.Context, address string) (queryStatus, error) {
	fail := func(e error) (queryStatus, error) { return queryStatus{}, e }
	c, e := (&net.Dialer{}).DialContext(ctx, "udp", address)
	if e != nil {
		return fail(e)
	}
	defer c.Close()
	deadline := time.Now().Add(time.Second)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	c.SetDeadline(deadline)
	stop := context.AfterFunc(ctx, func() { c.Close() })
	defer stop()
	request := []byte("\xff\xff\xff\xffTSource Engine Query\x00")
	for n := 0; n < 2; n++ {
		if _, e = c.Write(request); e != nil {
			return fail(e)
		}
		b := make([]byte, 4097)
		size, e := c.Read(b)
		if e != nil {
			return fail(e)
		}
		if size > 4096 {
			return fail(errQuery)
		}
		b = b[:size]
		if n == 0 && len(b) == 9 && bytes.Equal(b[:5], []byte{255, 255, 255, 255, 0x41}) {
			request = append(request, b[5:]...)
			continue
		}
		return parseInfo(b)
	}
	return fail(errQuery)
}
