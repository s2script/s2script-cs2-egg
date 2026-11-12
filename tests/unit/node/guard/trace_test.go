// SPDX-License-Identifier: GPL-3.0-or-later

package guard_test

import (
	"testing"
	"time"

	"github.com/K4ryuu/CS2-Egg-Go/internal/node/guard"
)

func TestCachedTraceEvidenceNeverTreatsAbsenceAsNoTraffic(t *testing.T) {
	now := time.Date(2026, 10, 22, 18, 0, 0, 0, time.UTC)
	sample := guard.RateSample{ip("203.0.113.7"): {Packets: 12, Bytes: 600}}
	for _, tc := range []struct {
		name, target string
		at           time.Time
		data         guard.RateSample
		status       string
		present      bool
	}{
		{"present", "203.0.113.7", now, sample, "sampled", true},
		{"absent", "203.0.113.8", now, sample, "sampled", false},
		{"stale", "203.0.113.7", now.Add(-time.Minute), sample, "unavailable", false},
		{"no sample", "203.0.113.7", now, nil, "unavailable", false},
		{"invalid", "203.0.113.7; flush", now, sample, "unavailable", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := guard.TraceSnapshot(tc.target, tc.data, tc.at, now, 5)
			if s.Status != tc.status || s.Present != tc.present {
				t.Fatalf("snapshot: %+v", s)
			}
			if tc.present && (s.Packets != 12 || s.Bytes != 600 || s.SampledAt != now.Format(time.RFC3339Nano)) {
				t.Fatalf("evidence: %+v", s)
			}
		})
	}
}
