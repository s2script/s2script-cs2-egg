// SPDX-License-Identifier: GPL-3.0-or-later

// Package passwords owns node-managed rotating join passwords.
package passwords

import (
	"errors"
	"path/filepath"
	"time"
)

const DefaultWordlist = "/var/lib/cs2node/passwords/words.txt"

type Config struct {
	Enabled  bool   `json:"enabled"`
	Mode     string `json:"mode"`
	Interval string `json:"interval"`
	Dir      string `json:"dir"`
	Wordlist string `json:"wordlist"`
}

func Defaults() Config {
	return Config{Mode: "unique", Interval: "monthly", Dir: "/var/lib/cs2node/passwords", Wordlist: DefaultWordlist}
}

func (c Config) Check() error {
	if c.Mode != "shared" && c.Mode != "unique" {
		return errors.New("mode must be shared or unique")
	}
	if !filepath.IsAbs(c.Dir) || !filepath.IsAbs(c.Wordlist) {
		return errors.New("dir and wordlist must be absolute paths")
	}
	_, err := Next(time.Now(), c.Interval)
	return err
}

// Next uses UTC elapsed periods, with month-end clamping for calendar months.
func Next(from time.Time, interval string) (time.Time, error) {
	from = from.UTC()
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
	default:
		return time.Time{}, errors.New("interval must be daily, weekly, bi-weekly or monthly")
	}
}
