// SPDX-License-Identifier: GPL-3.0-or-later
package protocol

import (
	"context"
	"encoding/binary"
	"errors"
	"net"
	"testing"
	"time"
)

func TestUnknownProtocolRejected(t *testing.T) {
	a, b := net.Pipe()
	defer b.Close()
	go ServeConn(context.Background(), a, func(context.Context, Message) (any, error) {
		t.Error("unknown version reached handler")
		return nil, nil
	})
	b.SetDeadline(time.Now().Add(time.Second))
	if e := Write(b, Message{Version: 99, ID: "x", Type: "hello", Payload: []byte(`{}`)}); e != nil {
		t.Fatal(e)
	}
	if _, e := Read(b); e == nil {
		t.Fatal("unknown protocol accepted")
	}
}
func TestOversizeFrameClosed(t *testing.T) {
	a, b := net.Pipe()
	defer b.Close()
	go ServeConn(context.Background(), a, func(context.Context, Message) (any, error) { t.Error("oversize reached handler"); return nil, nil })
	b.SetDeadline(time.Now().Add(time.Second))
	var header [4]byte
	binary.BigEndian.PutUint32(header[:], MaxFrame+1)
	if _, e := b.Write(header[:]); e != nil {
		t.Fatal(e)
	}
	if _, e := Read(b); e == nil {
		t.Fatal("oversized frame accepted")
	} else {
		var timeout net.Error
		if errors.As(e, &timeout) && timeout.Timeout() {
			t.Fatal("oversize frame was not closed before deadline")
		}
	}
}
func TestRequestCancellation(t *testing.T) {
	a, b := net.Pipe()
	defer b.Close()
	c := NewClient(a)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	started := time.Now()
	if e := c.Call(ctx, "hello", Hello{}, new(Welcome)); e == nil {
		t.Fatal("stalled peer succeeded")
	}
	if time.Since(started) > time.Second {
		t.Fatal("request ignored cancellation")
	}
}
func TestQueuedRequestDeadline(t *testing.T) {
	a, b := net.Pipe()
	defer b.Close()
	client := NewClient(a)
	defer client.Close()
	firstDone := make(chan struct{})
	firstCtx, cancelFirst := context.WithCancel(context.Background())
	defer cancelFirst()
	go func() { defer close(firstDone); client.Call(firstCtx, "hello", Hello{}, new(Welcome)) }()
	time.Sleep(10 * time.Millisecond)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- client.Call(ctx, "hello", Hello{}, new(Welcome)) }()
	select {
	case e := <-done:
		if e == nil {
			t.Fatal("queued request succeeded")
		}
	case <-time.After(100 * time.Millisecond):
		cancelFirst()
		<-firstDone
		t.Fatal("queued caller ignored its deadline")
	}
	cancelFirst()
	<-firstDone
}
