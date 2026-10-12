// SPDX-License-Identifier: GPL-3.0-or-later
package server

import (
	"context"
	"encoding/binary"
	"net"
	"testing"
	"time"
)

func infoPacket() []byte {
	b := []byte{255, 255, 255, 255, 0x49, 17}
	b = append(b, []byte("Fixture\x00de_dust2\x00csgo\x00Counter-Strike 2\x00")...)
	b = binary.LittleEndian.AppendUint16(b, 730)
	b = append(b, 3, 32, 0, 'd', 'l', 0, 1)
	return append(b, []byte("1.0\x00")...)
}
func TestInfoRejectsMalformedAndWrongGame(t *testing.T) {
	good := infoPacket()
	if s, e := parseInfo(good); e != nil || s.Map != "de_dust2" || !s.Players.Known || s.Players.Count != 3 {
		t.Fatalf("%+v %v", s, e)
	}
	for n := 0; n < len(good); n++ {
		if _, e := parseInfo(good[:n]); e == nil {
			t.Fatalf("accepted truncation %d", n)
		}
	}
	for _, b := range [][]byte{append(append([]byte{}, good...), 0xff), append(append([]byte{}, good...), 0x02), []byte{254, 255, 255, 255, 0x49}, []byte("Server started")} {
		if _, e := parseInfo(b); e == nil {
			t.Fatal("accepted malformed response")
		}
	}
	wrong := append([]byte{}, good...)
	off := len(good) - len("1.0\x00") - 9
	binary.LittleEndian.PutUint16(wrong[off:], 440)
	if _, e := parseInfo(wrong); e == nil {
		t.Fatal("accepted other app")
	}
}
func TestQueryChallengeAndUnknownOnFailure(t *testing.T) {
	conn, e := net.ListenPacket("udp4", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	defer conn.Close()
	done := make(chan error, 1)
	go func() {
		b := make([]byte, 1500)
		n, addr, e := conn.ReadFrom(b)
		if e != nil {
			done <- e
			return
		}
		want := "\xff\xff\xff\xffTSource Engine Query\x00"
		if string(b[:n]) != want {
			done <- context.Canceled
			return
		}
		conn.WriteTo([]byte{255, 255, 255, 255, 0x41, 1, 2, 3, 4}, addr)
		n, addr, e = conn.ReadFrom(b)
		if e == nil && string(b[:n]) != want+"\x01\x02\x03\x04" {
			e = context.Canceled
		}
		if e == nil {
			_, e = conn.WriteTo(infoPacket(), addr)
		}
		done <- e
	}()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	s, e := query(ctx, conn.LocalAddr().String())
	if e != nil || !s.Players.Known {
		t.Fatalf("%+v %v", s, e)
	}
	if e := <-done; e != nil {
		t.Fatal(e)
	}
	conn.Close()
	s, e = query(ctx, conn.LocalAddr().String())
	if e == nil || s.Players.Known {
		t.Fatal("failed query reported known players")
	}
}

func TestInfoValidatesDedicatedLinuxAndOptionalFields(t *testing.T) {
	base := infoPacket()
	offset := len(base) - len("1.0\x00") - 9
	for _, index := range []int{offset + 5, offset + 6} {
		b := append([]byte{}, base...)
		b[index] = 'x'
		if _, e := parseInfo(b); e == nil {
			t.Fatal("accepted unknown server/environment layout")
		}
	}
	b := append(append([]byte{}, base...), 0xf1)
	b = binary.LittleEndian.AppendUint16(b, 27015)
	b = binary.LittleEndian.AppendUint64(b, 123)
	b = binary.LittleEndian.AppendUint16(b, 27020)
	b = append(b, []byte("TV\x00tags\x00")...)
	b = binary.LittleEndian.AppendUint64(b, 730)
	if _, e := parseInfo(b); e != nil {
		t.Fatal(e)
	}
	b[len(b)-8] = 0
	if _, e := parseInfo(b); e == nil {
		t.Fatal("accepted conflicting GameID")
	}
}
