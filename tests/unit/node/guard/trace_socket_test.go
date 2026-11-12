// SPDX-License-Identifier: GPL-3.0-or-later

package guard_test

import (
	"testing"
	"time"

	"github.com/K4ryuu/CS2-Egg-Go/internal/node/guard"
	"github.com/K4ryuu/CS2-Egg-Go/internal/proto"
)

func TestConnectionTraceQueryDoesNotTouchFirewall(t *testing.T) {
	r := start(t, func(c *guard.Config) { c.IdleSampleSecs = 60 })
	defer r.cancel()
	defer r.client.Close()
	r.fw.mu.Lock()
	reads, applied := r.fw.sampleReads, r.fw.applied
	r.fw.mu.Unlock()
	if err := r.client.Send(&proto.ConnectionTraceQuery{IP: "203.0.113.7"}); err != nil {
		t.Fatal(err)
	}
	result := make(chan *proto.ConnectionTraceSample, 1)
	go func() {
		for {
			msg, err := r.client.Next()
			if err != nil {
				result <- nil
				return
			}
			if s, ok := msg.(*proto.ConnectionTraceSample); ok {
				result <- s
				return
			}
		}
	}()
	select {
	case s := <-result:
		if s == nil || s.IP != "203.0.113.7" || s.Status != "unavailable" {
			t.Fatalf("snapshot before first tick: %+v", s)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no diagnostic reply")
	}
	r.fw.mu.Lock()
	defer r.fw.mu.Unlock()
	if r.fw.sampleReads != reads || r.fw.applied != applied || len(r.fw.blocks) != 0 || len(r.fw.players) != 0 {
		t.Fatal("trace query reached firewall/enforcement")
	}
}
