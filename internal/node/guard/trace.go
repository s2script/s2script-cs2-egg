// SPDX-License-Identifier: GPL-3.0-or-later

package guard

import (
	"net/netip"
	"time"

	"github.com/K4ryuu/CS2-Egg-Go/internal/node/core"
	"github.com/K4ryuu/CS2-Egg-Go/internal/proto"
)

// TraceSnapshot reads a single entry from an immutable cached sample. It has no
// firewall handle: asking for diagnostics cannot create a rule, trigger a new
// netlink dump, add a trusted player, or affect enforcement.
func TraceSnapshot(target string, sample RateSample, sampledAt, now time.Time, sampleSecs int) proto.ConnectionTraceSample {
	out := proto.ConnectionTraceSample{Status: "unavailable"}
	ip, err := netip.ParseAddr(target)
	if err != nil || ip.Zone() != "" {
		return out
	}
	ip = ip.Unmap()
	out.IP = ip.String()
	if sample == nil || sampledAt.IsZero() || now.Sub(sampledAt) > maxSampleAge(sampleSecs) || now.Before(sampledAt) {
		return out
	}
	out.Status = "sampled"
	out.SampledAt = sampledAt.UTC().Format(time.RFC3339Nano)
	counter, ok := sample[ip]
	out.Present, out.Packets, out.Bytes = ok, counter.Packets, counter.Bytes
	return out
}

func (m *Module) onTraceQuery(c *core.Core, container string, q *proto.ConnectionTraceQuery) {
	// Bound untrusted input before parsing; only this socket receives the reply.
	if len(q.IP) > 64 {
		return
	}
	m.mu.Lock()
	sample := m.sample
	if !m.sampleValid {
		sample = nil
	}
	out := TraceSnapshot(q.IP, sample, m.sampleAt, m.Now(), m.cfg.IdleSampleSecs)
	m.mu.Unlock()
	c.Send(container, &out)
}
