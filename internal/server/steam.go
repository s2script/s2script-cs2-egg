// SPDX-License-Identifier: GPL-3.0-or-later
package server

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"

	"github.com/s2script/s2script-cs2-egg/internal/config"
	"github.com/s2script/s2script-cs2-egg/internal/steam"
)

const SteamSeed = "/opt/s2egg/steamcmd"

var bootstrapFiles = []string{"steamcmd.sh", "linux32/steamcmd", "linux32/steamerrorreporter", "linux32/libstdc++.so.6", "linux32/crashhandler.so"}

// safeRegular rejects symlinks and nonregular paths within an already-confined root.
func safeRegular(root *os.Root, path string) (bool, error) {
	s, e := root.Lstat(path)
	if errors.Is(e, os.ErrNotExist) {
		return false, nil
	}
	if e != nil {
		return false, e
	}
	if !s.Mode().IsRegular() {
		return false, errors.New("runtime file must be regular")
	}
	return s.Size() > 0, nil
}
func copyRuntimeFile(dest *os.Root, target string, source *os.Root, path string, mode os.FileMode) error {
	valid, e := safeRegular(source, path)
	if e != nil || !valid {
		return errors.New("required runtime source is missing or invalid")
	}
	f, e := source.Open(path)
	if e != nil {
		return e
	}
	defer f.Close()
	if e = dest.MkdirAll(filepath.Dir(target), 0700); e != nil {
		return e
	}
	if _, e = safeRegular(dest, target); e != nil {
		return e
	}
	temp := target + ".tmp-" + randomID()
	out, e := dest.OpenFile(temp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if e != nil {
		return e
	}
	defer dest.Remove(temp)
	n, e := io.Copy(out, io.LimitReader(f, (64<<20)+1))
	if e == nil && n > 64<<20 {
		e = errors.New("runtime file exceeds limit")
	}
	e = errors.Join(e, out.Sync(), out.Close())
	if e != nil {
		return e
	}
	return dest.Rename(temp, target)
}
func prepareSteam(ctx context.Context, cfg config.Server, seed string) (string, error) {
	root, e := os.OpenRoot(cfg.Root)
	if e != nil {
		return "", e
	}
	defer root.Close()
	for _, name := range bootstrapFiles {
		present, e := safeRegular(root, "steamcmd/"+name)
		if e != nil {
			return "", e
		}
		if present {
			continue
		}
		source, e := os.OpenRoot(seed)
		if e != nil {
			return "", errors.New("SteamCMD image seed unavailable at /opt/s2egg/steamcmd")
		}
		e = copyRuntimeFile(root, "steamcmd/"+name, source, name, 0755)
		source.Close()
		if e != nil {
			return "", e
		}
	}
	executable := filepath.Join(cfg.Root, "steamcmd/steamcmd.sh")
	present, e := safeRegular(root, "steamcmd/linux64/steamclient.so")
	if e != nil {
		return "", e
	}
	if !present {
		if e = steam.Bootstrap(ctx, executable, cfg.Root); e != nil {
			return "", e
		}
	}
	present, e = safeRegular(root, "steamcmd/linux64/steamclient.so")
	if e != nil || !present {
		return "", errors.New("SteamCMD initialization did not provide required SDK64 steamclient.so")
	}
	var build string
	if cfg.Steam.Update {
		build, e = steam.Update(ctx, executable, cfg.Root, cfg.Steam.Validate, nil)
	} else {
		build, e = steam.InstalledBuild(cfg.Root)
	}
	if e != nil {
		return "", e
	}
	for _, arch := range []string{"64", "32"} {
		source := "steamcmd/linux" + arch + "/steamclient.so"
		present, e := safeRegular(root, source)
		if e != nil {
			return "", e
		}
		if !present {
			if arch == "64" {
				return "", errors.New("SteamCMD update did not provide required SDK64 steamclient.so")
			}
			continue
		}
		if e = copyRuntimeFile(root, ".steam/sdk"+arch+"/steamclient.so", root, source, 0600); e != nil {
			return "", e
		}
	}
	return build, nil
}
