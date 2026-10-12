// SPDX-License-Identifier: GPL-3.0-or-later
package backup

import (
	"errors"
	"github.com/s2script/s2script-cs2-egg/internal/storeio"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

type stageOwner struct{ RootID, ServerID string }

func (s *Store) newStage(prefix string) (string, error) {
	p, e := os.MkdirTemp(s.policy.Root, prefix)
	if e != nil {
		return "", e
	}
	if e = storeio.Save(p, "owner.json", stageOwner{s.policy.RootID, s.policy.ServerID}); e != nil {
		os.RemoveAll(p)
		return "", e
	}
	return p, nil
}

// Called only while runtime/artifact/catalog exclusion proves no managed writer
// can still own a stage. Unknown or modified directories remain for diagnosis.
func (s *Store) cleanStages() error {
	entries, e := os.ReadDir(s.policy.Root)
	if e != nil {
		return e
	}
	for _, v := range entries {
		if !strings.HasPrefix(v.Name(), ".stage-") {
			continue
		}
		i, e := v.Info()
		if e != nil || !i.IsDir() || i.Mode().Perm() != 0700 || i.Sys().(*syscall.Stat_t).Uid != uint32(os.Geteuid()) {
			return errors.New("unrecognized backup staging directory")
		}
		p := filepath.Join(s.policy.Root, v.Name())
		var owner stageOwner
		if e = readJSON(p, "owner.json", &owner, 4096); e != nil || owner != (stageOwner{s.policy.RootID, s.policy.ServerID}) {
			return errors.New("unsealed stage lacks scoped ownership receipt; operator inspection required")
		}
		files, e := os.ReadDir(p)
		if e != nil {
			return e
		}
		if len(files) > s.policy.Files+4 {
			return errors.New("staging entry quota")
		}
		for _, f := range files {
			_, numeric := strconv.Atoi(f.Name())
			if f.Name() != "owner.json" && f.Name() != "archive.tar.gz" && f.Name() != "manifest.json" && numeric != nil {
				return errors.New("unexpected stage member")
			}
			i, e := f.Info()
			if e != nil || !i.Mode().IsRegular() || i.Mode().Perm() != 0600 || i.Sys().(*syscall.Stat_t).Nlink != 1 || i.Sys().(*syscall.Stat_t).Uid != uint32(os.Geteuid()) {
				return errors.New("staging member identity changed")
			}
		}
		if e = os.RemoveAll(p); e != nil {
			return e
		}
	}
	return syncDir(s.policy.Root)
}
