// SPDX-License-Identifier: GPL-3.0-or-later

// Package passwords owns node-managed rotating join passwords.
package passwords

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"
	_ "time/tzdata" // Keep named zones available in standalone node binaries.

	"github.com/robfig/cron/v3"
)

const DefaultWordlist = "/var/lib/cs2node/passwords/words.txt"

type Config struct {
	Enabled  bool   `json:"enabled"`
	Mode     string `json:"mode"`
	Interval string `json:"interval"`
	Timezone string `json:"timezone"`
	Dir      string `json:"dir"`
	Wordlist string `json:"wordlist"`
}

func Defaults() Config {
	return Config{Mode: "unique", Interval: "0 0 1 * *", Timezone: "UTC", Dir: "/var/lib/cs2node/passwords", Wordlist: DefaultWordlist}
}

func (c Config) Check() error {
	if c.Mode != "shared" && c.Mode != "unique" {
		return errors.New("mode must be shared or unique")
	}
	if !filepath.IsAbs(c.Dir) || !filepath.IsAbs(c.Wordlist) {
		return errors.New("dir and wordlist must be absolute paths")
	}
	_, err := c.Next(time.Now())
	return err
}

// Next returns the next cron occurrence in UTC, or the end of a legacy elapsed
// period. Config.Next supports an explicit timezone for cron expressions.
func Next(from time.Time, interval string) (time.Time, error) {
	return (Config{Interval: interval}).Next(from)
}

var cronParser = cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)

func (c Config) timezone() string {
	if zone := strings.TrimSpace(c.Timezone); zone != "" {
		return zone
	}
	return "UTC"
}

func legacyInterval(interval string) bool {
	switch strings.TrimSpace(interval) {
	case "daily", "weekly", "bi-weekly", "monthly":
		return true
	}
	return false
}

func (c Config) scheduleLabel() string {
	interval := strings.TrimSpace(c.Interval)
	if legacyInterval(interval) {
		return interval
	}
	return interval + " (" + c.timezone() + ")"
}

// Next calculates a strictly future deadline, stored in UTC. Legacy intervals
// keep their original UTC elapsed semantics, independent of the cron timezone.
func (c Config) Next(from time.Time) (time.Time, error) {
	zone := c.timezone()
	loc, err := time.LoadLocation(zone)
	if err != nil || zone == "Local" {
		return time.Time{}, errors.New("timezone must be UTC or an IANA name such as America/Chicago")
	}
	from = from.UTC()
	interval := strings.TrimSpace(c.Interval)
	switch interval {
	case "daily":
		return from.Add(24 * time.Hour), nil
	case "weekly":
		return from.Add(7 * 24 * time.Hour), nil
	case "bi-weekly":
		return from.Add(14 * 24 * time.Hour), nil
	case "monthly":
		first := time.Date(from.Year(), from.Month()+1, 1, from.Hour(), from.Minute(), from.Second(), from.Nanosecond(), time.UTC)
		last := first.AddDate(0, 1, -1).Day()
		return first.AddDate(0, 0, min(from.Day(), last)-1), nil
	}
	// Enforce five fields before parsing; timezone prefixes and descriptors are
	// deliberately excluded so there is only one place to configure the zone.
	if len(strings.Fields(interval)) != 5 || strings.HasPrefix(interval, "TZ=") || strings.HasPrefix(interval, "CRON_TZ=") {
		return time.Time{}, errors.New("interval must be five cron fields (minute hour day month weekday), or daily, weekly, bi-weekly, monthly")
	}
	schedule, err := cronParser.Parse(interval)
	if err != nil {
		return time.Time{}, fmt.Errorf("invalid password cron schedule: %w", err)
	}
	next := schedule.Next(from.In(loc))
	if next.IsZero() {
		return time.Time{}, errors.New("password cron schedule has no occurrence in the next five years")
	}
	return next.UTC(), nil
}
