// SPDX-License-Identifier: GPL-3.0-or-later
package config

import "errors"

type BackupNode struct {
	Enabled         bool   `json:"enabled"`
	Schedule        bool   `json:"schedule"`
	IntervalSeconds int    `json:"interval_seconds"`
	Mode            string `json:"mode"`
}

func (b *BackupNode) Validate() error {
	if b.IntervalSeconds == 0 {
		b.IntervalSeconds = 86400
	}
	if b.Mode == "" {
		b.Mode = "consistent"
	}
	if b.IntervalSeconds < 900 || b.IntervalSeconds > 2592000 || b.Mode != "consistent" && b.Mode != "live-best-effort" || b.Schedule && !b.Enabled {
		return errors.New("invalid backup schedule (15 minutes..30 days) or mode")
	}
	return nil
}
