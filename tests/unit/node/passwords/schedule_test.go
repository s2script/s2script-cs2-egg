// SPDX-License-Identifier: GPL-3.0-or-later

package passwords_test

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/K4ryuu/CS2-Egg-Go/internal/node/core"
	"github.com/K4ryuu/CS2-Egg-Go/internal/node/passwords"
)

func TestCronAssignmentDeadlines(t *testing.T) {
	for _, tc := range []struct{ name, interval, zone, from, want string }{
		{"daily UTC", "0 4 * * *", "", "2026-10-26T10:12:45Z", "2026-10-27T04:00:00Z"},
		{"boundary is exclusive", "0 4 * * *", "UTC", "2026-10-26T04:00:00Z", "2026-10-27T04:00:00Z"},
		{"steps", "*/15 * * * *", "UTC", "2026-10-26T10:07:45Z", "2026-10-26T10:15:00Z"},
		{"lists and ranges", "15,45 9-17 * * MON-FRI", "UTC", "2026-10-30T17:45:00Z", "2026-11-02T09:15:00Z"},
		{"named month", "0 0 1 JAN *", "UTC", "2026-10-26T00:00:00Z", "2027-01-01T00:00:00Z"},
		{"missing month day skipped", "0 0 31 * *", "UTC", "2026-01-31T00:00:00Z", "2026-03-31T00:00:00Z"},
		{"leap day", "0 0 29 2 *", "UTC", "2026-10-26T00:00:00Z", "2028-02-29T00:00:00Z"},
		{"restricted days use OR", "0 4 1 * MON", "UTC", "2026-10-26T05:00:00Z", "2026-11-01T04:00:00Z"},
		{"Chicago daylight time", "0 4 * * *", "America/Chicago", "2026-10-26T00:00:00Z", "2026-10-26T09:00:00Z"},
		{"Chicago standard time", "0 4 * * *", "America/Chicago", "2026-11-02T00:00:00Z", "2026-11-02T10:00:00Z"},
		{"spring nonexistent hour", "30 2 * * *", "America/Chicago", "2026-03-08T07:00:00Z", "2026-03-09T07:30:00Z"},
		{"fall repeated hour", "30 1 * * *", "America/Chicago", "2026-11-01T06:30:00Z", "2026-11-01T07:30:00Z"},
		{"legacy remains elapsed UTC", "daily", "America/Chicago", "2026-03-07T10:00:00Z", "2026-03-08T10:00:00Z"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := testConfig(t, "unique")
			cfg.Interval = tc.interval
			// Use JSON to exercise the same configuration path as existing installs.
			raw, _ := json.Marshal(map[string]string{"timezone": tc.zone})
			if err := json.Unmarshal(raw, &cfg); err != nil {
				t.Fatal(err)
			}
			s, err := passwords.Open(cfg)
			if err != nil {
				t.Fatal(err)
			}
			from, _ := time.Parse(time.RFC3339, tc.from)
			r, changed, err := s.Get("one", from)
			if err != nil || !changed {
				t.Fatalf("generation: changed=%v err=%v", changed, err)
			}
			if got := r.NextAt.Format(time.RFC3339); got != tc.want {
				t.Fatalf("deadline = %s, want %s", got, tc.want)
			}
		})
	}
}

func TestInvalidCronConfig(t *testing.T) {
	for _, interval := range []string{"", "0 4 * *", "0 0 4 * * *", "60 4 * * *", "0 24 * * *", "*/0 * * * *", "0 0 31 2 *", "@daily", "@every 1s", "CRON_TZ=UTC", "CRON_TZ=UTC 0 4 * * *", "0 4 * * * command"} {
		cfg := testConfig(t, "unique")
		cfg.Interval = interval
		if _, err := passwords.Open(cfg); err == nil {
			t.Errorf("accepted invalid schedule %q", interval)
		}
	}
	for _, zone := range []string{"Not/AZone", "UTC 0 0 * * *", "Local"} {
		cfg := testConfig(t, "unique")
		raw, _ := json.Marshal(map[string]string{"timezone": zone})
		if err := json.Unmarshal(raw, &cfg); err != nil {
			t.Fatal(err)
		}
		if _, err := passwords.Open(cfg); err == nil {
			t.Errorf("accepted invalid timezone %q", zone)
		}
	}
}

func TestCronKeepsSavedDeadlineAndCatchesUpOnce(t *testing.T) {
	for _, mode := range []string{"unique", "shared"} {
		t.Run(mode, func(t *testing.T) {
			cfg := testConfig(t, mode)
			cfg.Interval = "monthly"
			s, err := passwords.Open(cfg)
			if err != nil {
				t.Fatal(err)
			}
			now := time.Date(2026, 1, 31, 12, 0, 0, 0, time.UTC)
			old, _, err := s.Get("one", now)
			if err != nil {
				t.Fatal(err)
			}
			cfg.Interval = "0 4 * * *"
			s, err = passwords.Open(cfg)
			if err != nil {
				t.Fatal(err)
			}
			unchanged, changed, err := s.Get("one", old.NextAt.Add(-time.Second))
			if err != nil || changed || unchanged != old {
				t.Fatal("schedule edit changed the saved assignment")
			}
			late := time.Date(2026, 3, 3, 5, 0, 0, 0, time.UTC)
			r, changed, err := s.Get("one", late)
			if err != nil || !changed || r.Password == old.Password || r.NextAt.Format(time.RFC3339) != "2026-03-04T04:00:00Z" {
				t.Fatalf("catch-up failed: changed=%v next=%s err=%v", changed, r.NextAt, err)
			}
			s, err = passwords.Open(cfg)
			if err != nil {
				t.Fatal(err)
			}
			server := "one"
			if mode == "shared" {
				server = "two"
			}
			again, changed, err := s.Get(server, late.Add(time.Minute))
			if err != nil || changed || again != r || len(s.Pending()) != 1 {
				t.Fatal("restart or second server replayed missed rotations")
			}
		})
	}
}

func testConfig(t *testing.T, mode string) passwords.Config {
	t.Helper()
	cfg := passwords.Defaults()
	cfg.Enabled = true
	cfg.Mode = mode
	cfg.Dir = t.TempDir()
	var err error
	cfg.Wordlist, err = filepath.Abs("../../../../internal/node/passwords/testdata/eff_large_wordlist.txt")
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func TestStatusShowsCronTimezone(t *testing.T) {
	m := passwords.New(nil, nil)
	var out strings.Builder
	m.PrintStatus(&out, nil, core.Status{Module: map[string]json.RawMessage{
		"passwords": json.RawMessage(`{"mode":"unique","interval":"0 4 * * *","timezone":"America/Chicago","pending_notifications":1}`),
	}})
	if !strings.Contains(out.String(), "0 4 * * * (America/Chicago)") {
		t.Fatalf("status omitted schedule timezone: %s", out.String())
	}
}
