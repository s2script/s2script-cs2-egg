// SPDX-License-Identifier: GPL-3.0-or-later
// Package processgroup provides Linux cleanup for caller-owned process groups.
package processgroup

import (
	"errors"
	"sync"
	"syscall"
)

var reapingOnce sync.Once
var reapingError error

// EnableReaping enables Linux child-subreaper mode for this process. This
// process-wide setting allows orphaned descendants to be reaped by their group
// owner. Call before spawning children; no generic wait/reaper is installed.
func EnableReaping() error {
	reapingOnce.Do(func() {
		_, _, e := syscall.Syscall6(syscall.SYS_PRCTL, 36, 1, 0, 0, 0, 0)
		if e != 0 {
			reapingError = e
		}
	})
	return reapingError
}

// KillAndReap kills and waits for descendants in a positive, caller-owned PGID.
// The caller must first Wait its direct child; that child's Wait remains solely
// its owner's responsibility. EnableReaping must precede that child's spawn.
// Never use this for a shared or unowned group; unrelated groups are not waited.
func KillAndReap(pgid int) error {
	if pgid <= 0 {
		return errors.New("process group ID must be positive")
	}
	if e := syscall.Kill(-pgid, syscall.SIGKILL); e != nil && !errors.Is(e, syscall.ESRCH) {
		return e
	}
	for {
		var status syscall.WaitStatus
		_, e := syscall.Wait4(-pgid, &status, 0, nil)
		if errors.Is(e, syscall.EINTR) {
			continue
		}
		if errors.Is(e, syscall.ECHILD) {
			return nil
		}
		if e != nil {
			return e
		}
	}
}
