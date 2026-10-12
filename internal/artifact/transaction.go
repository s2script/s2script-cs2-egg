// SPDX-License-Identifier: GPL-3.0-or-later
package artifact

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
)

const (
	transactionDir = ".artifact-txn"
	// Journals must always fit the recovery reader before publication permits mutation.
	maxJournalBytes = 16 << 20
)

// Change replaces a relative regular file from Source, or removes it. CreateOnly
// leaves an existing regular file untouched. Source is a staged regular file.
type Change struct {
	Path             string
	Source           string
	Remove           bool
	CreateOnly       bool
	PreserveMetadata bool // private restore: retain permitted existing UID/GID/mode
}
type journalEntry struct {
	Path             string `json:"path"`
	HadOld           bool   `json:"had_old"`
	PreserveMetadata bool   `json:"preserve_metadata,omitempty"`
	Remove           bool   `json:"remove,omitempty"`
}
type journal struct {
	Version     int            `json:"version"`
	Entries     []journalEntry `json:"entries"`
	CreatedDirs []string       `json:"created_dirs,omitempty"`
}

func managedPath(p string) error {
	if e := validPath(p); e != nil {
		return e
	}
	if strings.HasPrefix(strings.Split(p, "/")[0], ".artifact-") {
		return errors.New("reserved artifact metadata path")
	}
	return nil
}
func existing(root *os.File, p string) (bool, error) {
	f, e := openFile(root, p, syscall.O_RDONLY, 0)
	if errors.Is(e, os.ErrNotExist) {
		return false, nil
	}
	if e != nil {
		return false, e
	}
	return true, f.Close()
}
func syncDir(root *os.File, p string) error {
	d, _, e := parent(root, p+"/placeholder", false)
	if e != nil {
		return e
	}
	return errors.Join(d.Sync(), d.Close())
}
func cleanTransaction(root *os.File) error {
	// Detach only after rollback is complete or the committed marker is durable.
	// A crash during garbage collection must not make a partial journal recoverable.
	garbage := ".artifact-gc-" + randomName()
	if e := rename(root, transactionDir, garbage); e != nil {
		return e
	}
	r, e := os.OpenRoot("/proc/self/fd/" + strconv.Itoa(int(root.Fd())))
	if e != nil {
		return e
	}
	defer r.Close()
	if e = r.RemoveAll(garbage); e != nil {
		return e
	}
	return root.Sync()
}
func writeJournal(root *os.File, j journal) error {
	b, e := json.Marshal(j)
	if e != nil {
		return e
	}
	if len(b) > maxJournalBytes {
		return errors.New("transaction journal too large")
	}
	f, e := openFile(root, transactionDir+"/journal.tmp", syscall.O_WRONLY|syscall.O_CREAT|syscall.O_EXCL, 0600)
	if e != nil {
		return e
	}
	_, e = f.Write(b)
	if e == nil {
		e = f.Sync()
	}
	e = errors.Join(e, f.Close())
	if e != nil {
		return e
	}
	return rename(root, transactionDir+"/journal.tmp", transactionDir+"/journal.json")
}

// Apply requires a caller-held Acquire lock and stopped consumers. It retains
// previous files until a durable commit marker is written. Per-file renames are
// NOT an atomic multi-file update. Recover is mandatory before consumers run
// after interruption. On ordinary failure Apply attempts rollback immediately.
func Apply(ctx context.Context, rootPath string, changes []Change) (err error) {
	root, e := openDir(rootPath)
	if e != nil {
		return e
	}
	defer root.Close()
	if e = recoverRoot(root); e != nil {
		return e
	}
	if e = ctx.Err(); e != nil {
		return e
	}
	seen := map[string]bool{}
	j := journal{Version: 1}
	selected := []Change{}
	// Validate the complete set before changing any managed file.
	for _, c := range changes {
		if e = managedPath(c.Path); e != nil {
			return e
		}
		if seen[c.Path] || c.Remove && c.CreateOnly {
			return errors.New("duplicate or conflicting change")
		}
		seen[c.Path] = true
		old, e := existing(root, c.Path)
		if e != nil {
			return e
		}
		if c.CreateOnly && old {
			continue
		}
		if !c.Remove {
			d, e := openDir(filepath.Dir(c.Source))
			if e != nil {
				return e
			}
			f, e := openFile(d, filepath.Base(c.Source), syscall.O_RDONLY, 0)
			d.Close()
			if e != nil {
				return e
			}
			if e = f.Close(); e != nil {
				return e
			}
		}
		if c.PreserveMetadata {
			if c.Remove {
				return errors.New("private restoration is replace-only")
			}
			if old {
				f, e := openFile(root, c.Path, syscall.O_RDONLY, 0)
				if e != nil {
					return e
				}
				i, e := f.Stat()
				f.Close()
				if e != nil {
					return e
				}
				if e = permittedMetadata(i); e != nil {
					return e
				}
			}
			dirs, e := missingParents(root, c.Path)
			if e != nil {
				return e
			}
			for _, p := range dirs {
				found := false
				for _, old := range j.CreatedDirs {
					if old == p {
						found = true
					}
				}
				if !found {
					j.CreatedDirs = append(j.CreatedDirs, p)
				}
			}
		}
		selected = append(selected, c)
		j.Entries = append(j.Entries, journalEntry{Path: c.Path, HadOld: old, Remove: c.Remove, PreserveMetadata: c.PreserveMetadata})
	}
	paths := make([]string, 0, len(seen))
	for p := range seen {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	for i := 1; i < len(paths); i++ {
		if strings.HasPrefix(paths[i], paths[i-1]+"/") {
			return errors.New("overlapping artifact paths")
		}
	}
	if len(selected) == 0 {
		return nil
	}
	if e = syscall.Mkdirat(int(root.Fd()), transactionDir, 0700); e != nil {
		return e
	}
	if e = root.Sync(); e != nil {
		return e
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, recoverRoot(root))
		}
	}()
	for i, c := range selected {
		if e = ctx.Err(); e != nil {
			return e
		}
		if j.Entries[i].HadOld {
			f, e := openFile(root, c.Path, syscall.O_RDONLY, 0)
			if e != nil {
				return e
			}
			e = copyPrivate(root, f, fmt.Sprintf("%s/old/%d", transactionDir, i), c.PreserveMetadata)
			e = errors.Join(e, f.Close())
			if e != nil {
				return e
			}
		}
		if !c.Remove {
			d, e := openDir(filepath.Dir(c.Source))
			if e != nil {
				return e
			}
			f, e := openFile(d, filepath.Base(c.Source), syscall.O_RDONLY, 0)
			d.Close()
			if e != nil {
				return e
			}
			e = copyFile(root, f, fmt.Sprintf("%s/new/%d", transactionDir, i))
			e = errors.Join(e, f.Close())
			if e != nil {
				return e
			}
			if c.PreserveMetadata {
				n, e := openFile(root, fmt.Sprintf("%s/new/%d", transactionDir, i), syscall.O_RDWR, 0)
				if e != nil {
					return e
				}
				if j.Entries[i].HadOld {
					old, e := openFile(root, c.Path, syscall.O_RDONLY, 0)
					if e != nil {
						n.Close()
						return e
					}
					info, e := old.Stat()
					old.Close()
					if e != nil {
						n.Close()
						return e
					}
					e = setMetadata(n, info)
					if e != nil {
						n.Close()
						return e
					}
				} else {
					e = n.Chmod(0600)
				}
				e = errors.Join(e, n.Sync(), n.Close())
				if e != nil {
					return e
				}
			}
		}
	}
	for _, p := range []string{"old", "new"} {
		e = syncDir(root, transactionDir+"/"+p)
		if e != nil && !errors.Is(e, os.ErrNotExist) {
			return e
		}
	}
	if e = writeJournal(root, j); e != nil {
		return e
	}
	for _, p := range j.CreatedDirs {
		if e = makePrivateDir(root, p); e != nil {
			return e
		}
	}
	for i, c := range selected {
		if e = ctx.Err(); e != nil {
			return e
		}
		if c.Remove {
			e = remove(root, c.Path)
		} else {
			e = rename(root, fmt.Sprintf("%s/new/%d", transactionDir, i), c.Path)
		}
		if e != nil {
			return e
		}
	}
	f, e := openFile(root, transactionDir+"/committed", syscall.O_WRONLY|syscall.O_CREAT|syscall.O_EXCL, 0600)
	if e != nil {
		return e
	}
	e = errors.Join(f.Sync(), f.Close())
	if e != nil {
		return e
	}
	if e = syncDir(root, transactionDir); e != nil {
		return e
	}
	return cleanTransaction(root)
}

// Recover requires the same caller-held Acquire lock as Apply. Any error blocks
// consumers: metadata is retained for diagnosis/retry, never silently discarded.
func Recover(rootPath string) error {
	root, e := openDir(rootPath)
	if e != nil {
		return e
	}
	defer root.Close()
	return recoverRoot(root)
}
func recoverRoot(root *os.File) error {
	d, _, e := parent(root, transactionDir+"/placeholder", false)
	if errors.Is(e, os.ErrNotExist) {
		return nil
	}
	if e != nil {
		return e
	}
	d.Close()
	committed, e := existing(root, transactionDir+"/committed")
	if e != nil {
		return e
	}
	if committed {
		return cleanTransaction(root)
	}
	f, e := openFile(root, transactionDir+"/journal.json", syscall.O_RDONLY, 0)
	if errors.Is(e, os.ErrNotExist) {
		return cleanTransaction(root)
	}
	if e != nil {
		return e
	}
	b, e := io.ReadAll(io.LimitReader(f, maxJournalBytes+1))
	e = errors.Join(e, f.Close())
	if e != nil {
		return e
	}
	if len(b) > maxJournalBytes {
		return errors.New("transaction journal too large")
	}
	var j journal
	if e = json.Unmarshal(b, &j); e != nil {
		return errors.New("invalid transaction journal")
	}
	if j.Version != 1 {
		return errors.New("unsupported transaction journal")
	}
	seen := map[string]bool{}
	for i, c := range j.Entries {
		if e = managedPath(c.Path); e != nil {
			return e
		}
		if seen[c.Path] {
			return errors.New("duplicate journal path")
		}
		seen[c.Path] = true
		if _, e = existing(root, c.Path); e != nil {
			return e
		}
		if c.HadOld {
			ok, e := existing(root, fmt.Sprintf("%s/old/%d", transactionDir, i))
			if e != nil {
				return e
			}
			if !ok {
				return errors.New("missing rollback file")
			}
		}
	}
	for i, c := range j.Entries {
		if c.HadOld {
			src, e := openFile(root, fmt.Sprintf("%s/old/%d", transactionDir, i), syscall.O_RDONLY, 0)
			if e != nil {
				return e
			}
			temp := fmt.Sprintf("%s/restore-%d", transactionDir, i)
			if e = remove(root, temp); e != nil {
				src.Close()
				return e
			}
			e = copyPrivate(root, src, temp, c.PreserveMetadata)
			e = errors.Join(e, src.Close())
			if e != nil {
				return e
			}
			if e = rename(root, temp, c.Path); e != nil {
				return e
			}
		} else {
			if e = remove(root, c.Path); e != nil {
				return e
			}
		}
	}
	for i := len(j.CreatedDirs) - 1; i >= 0; i-- {
		if e = removePrivateDir(root, j.CreatedDirs[i]); e != nil {
			return e
		}
	}
	return cleanTransaction(root)
}
