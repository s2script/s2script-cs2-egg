// SPDX-License-Identifier: GPL-3.0-or-later

package startup

import (
	"debug/elf"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"
)

// Environment prepares the game launch environment and its descendants. Compatibility is
// enabled when S2Script's installed ELF requests an executable stack, which
// glibc 2.41 no longer enables on dlopen. Publisher binaries stay unmodified.
// The bool reports this requirement; an explicit conflicting tunable is an error.
func Environment(root string, inherited []string) ([]string, bool, error) {
	dir, err := os.OpenRoot(root)
	if err != nil {
		return nil, false, err
	}
	defer dir.Close()
	file, err := dir.Open("game/csgo/addons/s2script/bin/linuxsteamrt64/s2script.so")
	if errors.Is(err, fs.ErrNotExist) {
		return inherited, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("inspect S2Script stack requirement: %w", err)
	}
	defer file.Close()
	shim, err := elf.NewFile(file)
	if err != nil {
		return nil, false, fmt.Errorf("inspect S2Script ELF: %w", err)
	}
	// On x86-64 a missing GNU_STACK header also implies an executable stack.
	needsExec := true
	for _, prog := range shim.Progs {
		if prog.Type == elf.PT_GNU_STACK {
			needsExec = prog.Flags&elf.PF_X != 0
			break
		}
	}
	if !needsExec {
		return inherited, false, nil
	}
	index := -1
	value := ""
	for i, entry := range inherited {
		if tunables, ok := strings.CutPrefix(entry, "GLIBC_TUNABLES="); ok {
			index, value = i, tunables
		}
	}
	alreadySet := false
	for _, entry := range strings.Split(value, ":") {
		key, setting, _ := strings.Cut(entry, "=")
		if key == "glibc.rtld.enable_secure" && setting == "1" {
			return nil, false, errors.New("S2Script requires executable-stack compatibility on SteamRT4, but GLIBC_TUNABLES glibc.rtld.enable_secure=1 disables it; update the shim or remove the conflicting secure-mode setting")
		}
		if key != "glibc.rtld.execstack" {
			continue
		}
		if setting != "2" {
			return nil, false, errors.New("S2Script requires an executable stack on SteamRT4; update its shim or explicitly allow GLIBC_TUNABLES=glibc.rtld.execstack=2")
		}
		alreadySet = true
	}
	if alreadySet {
		return inherited, true, nil
	}
	if value != "" && !strings.HasSuffix(value, ":") {
		value += ":"
	}
	value += "glibc.rtld.execstack=2"
	env := append([]string(nil), inherited...)
	if index < 0 {
		env = append(env, "GLIBC_TUNABLES="+value)
	} else {
		env[index] = "GLIBC_TUNABLES=" + value
	}
	return env, true, nil
}
