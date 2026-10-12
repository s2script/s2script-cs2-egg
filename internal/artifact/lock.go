// SPDX-License-Identifier: GPL-3.0-or-later
package artifact

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"sync"
	"syscall"
)

func randomName() string { var b [16]byte; _, _ = rand.Read(b[:]); return hex.EncodeToString(b[:]) }

// Acquire takes a nonblocking cross-process lock. Hold it across Recover,
// inspection, staging, and Apply; release before running consumers. Never delete
// or replace .artifact-lock: its stable inode is the lock identity.
func Acquire(root string) (func() error, error) {
	d, e := openDir(root)
	if e != nil {
		return nil, e
	}
	defer d.Close()
	f, e := openFile(d, ".artifact-lock", syscall.O_RDWR|syscall.O_CREAT, 0600)
	if e != nil {
		return nil, e
	}
	if e = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); e != nil {
		f.Close()
		return nil, errors.New("artifact update lock is held or unavailable")
	}
	var once sync.Once
	var result error
	return func() error {
		once.Do(func() { result = errors.Join(syscall.Flock(int(f.Fd()), syscall.LOCK_UN), f.Close()) })
		return result
	}, nil
}
