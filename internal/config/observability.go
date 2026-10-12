// SPDX-License-Identifier: GPL-3.0-or-later
package config

import (
	"errors"
	"net"
	"strconv"
)

type Observability struct {
	Enabled        bool     `json:"enabled"`
	PrivateMetrics string   `json:"privateMetrics,omitempty"`
	LogBytes       int64    `json:"log_bytes"`
	LogFiles       int      `json:"log_files"`
	TailBytes      int      `json:"tail_bytes"`
	CrashBytes     int64    `json:"crash_bytes"`
	CrashFiles     int      `json:"crash_files"`
	DisplayExclude []string `json:"display_exclude,omitempty"`
}

func (o *Observability) Validate() error {
	if o.LogBytes == 0 {
		o.LogBytes = 16 << 20
	}
	if o.LogFiles == 0 {
		o.LogFiles = 6
	}
	if o.TailBytes == 0 {
		o.TailBytes = 256 << 10
	}
	if o.CrashBytes == 0 {
		o.CrashBytes = 2 << 20
	}
	if o.CrashFiles == 0 {
		o.CrashFiles = 20
	}
	if o.LogBytes < 64 || o.LogBytes > 64<<20 || o.LogFiles < 2 || o.LogFiles > 24 || o.TailBytes < 32 || o.TailBytes > 1<<20 || o.CrashBytes < 1024 || o.CrashBytes > 8<<20 || o.CrashFiles < 1 || o.CrashFiles > 80 {
		return errors.New("invalid observability bounds")
	}
	if len(o.DisplayExclude) > 32 {
		return errors.New("too many display filters")
	}
	for _, v := range o.DisplayExclude {
		if v == "" || len(v) > 256 {
			return errors.New("invalid display filter")
		}
	}
	if o.PrivateMetrics != "" {
		h, p, e := net.SplitHostPort(o.PrivateMetrics)
		n, pe := strconv.Atoi(p)
		ip := net.ParseIP(h)
		if e != nil || pe != nil || n < 1 || n > 65535 || ip == nil || !ip.IsLoopback() {
			return errors.New("privateMetrics requires literal loopback IP and port")
		}
		if !o.Enabled {
			return errors.New("privateMetrics requires observability enabled")
		}
	}
	return nil
}
