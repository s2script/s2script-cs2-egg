// SPDX-License-Identifier: GPL-3.0-or-later

package addons

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"strconv"
	"strings"

	"github.com/K4ryuu/CS2-Egg-Go/internal/logx"
)

// S2Script's addonmanager plugin mounts workshop addons and sends them to
// clients itself, so the egg no longer installs MultiAddonManager. The two
// hook the same engine functions; a server must not load both.
const (
	mamLoader   = "addons/metamod/multiaddonmanager.vdf"
	mamBinaries = "addons/multiaddonmanager/bin"
	mamConfig   = "cfg/multiaddonmanager/multiaddonmanager.cfg"
	mamEvidence = "egg/native-addons/multiaddonmanager.json"

	// addonManagerPlugin is the active (not disabled/) addonmanager package.
	addonManagerPlugin = "addons/s2script/plugins/_s2script_addonmanager.s2sp"
	s2scriptLoader     = "addons/metamod/s2script.vdf"

	// AddonManagerConfig is the addonmanager plugin's config file (the
	// runtime names it after the plugin id, with '@' and '/' as '_').
	AddonManagerConfig = "addons/s2script/configs/_s2script_addonmanager.json"
)

// mamSetting maps one MultiAddonManager convar to its addonmanager key.
type mamSetting struct {
	convar, key, kind string // kind: string, float or bool
}

var mamSettings = []mamSetting{
	{"mm_extra_addons", "addons_extra", "string"},
	{"mm_client_extra_addons", "addons_client_extra", "string"},
	{"mm_extra_addons_timeout", "addons_extra_timeout", "float"},
	{"mm_addon_connection_timeout", "addons_connection_timeout", "float"},
	{"mm_addon_mount_download", "addons_mount_download", "bool"},
	{"mm_cache_clients_with_addons", "addons_cache_clients", "bool"},
	{"mm_cache_clients_duration", "addons_cache_duration", "float"},
	{"mm_block_disconnect_messages", "addons_block_disconnect_messages", "bool"},
}

// MultiAddonManagerInstalled reports whether Metamod would still load
// MultiAddonManager on this server.
func MultiAddonManagerInstalled(serverRoot string) bool {
	return gameFileExists(serverRoot, mamLoader)
}

// AddonManagerActive reports whether Metamod loads S2Script and its
// addonmanager plugin is installed and enabled, so it can take over from
// MultiAddonManager.
func AddonManagerActive(serverRoot string) bool {
	return gameFileExists(serverRoot, s2scriptLoader) && gameFileExists(serverRoot, addonManagerPlugin)
}

func gameFileExists(serverRoot, rel string) bool {
	root, err := nativeGameRoot(serverRoot, false)
	if err != nil {
		return false
	}
	defer root.Close()
	_, err = root.Lstat(rel)
	return err == nil
}

// RetireMultiAddonManager stops Metamod from loading MultiAddonManager and
// carries its settings over to S2Script's addonmanager plugin.
//
// The settings move only when the plugin has no config yet, so an operator's
// own addonmanager config always wins. MultiAddonManager's cfg directory is
// left in place for reference; only the files the egg used to manage (the
// loader, the binaries and the install receipt) are removed. The loader goes
// last: until it is gone, the next boot tries again.
func (e *Env) RetireMultiAddonManager() error {
	root, err := nativeGameRoot(e.Root, false)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer root.Close()
	if _, err := root.Lstat(mamLoader); errors.Is(err, fs.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}

	if err := e.migrateMultiAddonManager(root); err != nil {
		return fmt.Errorf("move MultiAddonManager settings: %w", err)
	}
	if err := nativeCheckParents(root, mamBinaries+"/placeholder"); err != nil {
		return err
	}
	if err := root.RemoveAll(mamBinaries); err != nil {
		return err
	}
	if server, err := os.OpenRoot(e.Root); err == nil {
		if err := server.Remove(mamEvidence); err != nil && !errors.Is(err, fs.ErrNotExist) {
			e.Log.Logf(logx.Warning, "Could not remove the MultiAddonManager install receipt: %v", err)
		}
		server.Close()
	}
	if err := root.Remove(mamLoader); err != nil {
		return err
	}
	e.Log.Log(logx.Success, "Removed MultiAddonManager: S2Script's addonmanager plugin mounts workshop addons now")
	return nil
}

func (e *Env) migrateMultiAddonManager(root *os.Root) error {
	if err := nativeCheckParents(root, mamConfig); err != nil {
		return err
	}
	data, err := root.ReadFile(mamConfig)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	values := ParseMultiAddonManagerConfig(data)
	extra, _ := values["addons_extra"].(string)
	clientExtra, _ := values["addons_client_extra"].(string)
	if strings.TrimSpace(extra) == "" && strings.TrimSpace(clientExtra) == "" {
		// No addons to carry over: let S2Script write its documented defaults.
		return nil
	}
	if err := nativeCheckParents(root, AddonManagerConfig); err != nil {
		return err
	}
	if _, err := root.Lstat(AddonManagerConfig); err == nil {
		e.Log.Logf(logx.Warning, "MultiAddonManager settings were not moved: %s already exists. Copy anything still needed from %s by hand", AddonManagerConfig, mamConfig)
		return nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	out, err := json.MarshalIndent(values, "", "  ")
	if err != nil {
		return err
	}
	if err := root.MkdirAll(path.Dir(AddonManagerConfig), 0o755); err != nil {
		return err
	}
	tmp := AddonManagerConfig + ".tmp"
	if err := writeFile(root, tmp, bytes.NewReader(append(out, '\n')), 0o644); err != nil {
		return err
	}
	if err := root.Rename(tmp, AddonManagerConfig); err != nil {
		root.Remove(tmp)
		return err
	}
	e.Log.Logf(logx.Success, "Moved MultiAddonManager settings to %s", AddonManagerConfig)
	return nil
}

// ParseMultiAddonManagerConfig reads MultiAddonManager's convars from a
// Source cfg file and returns them under addonmanager's keys, typed the way
// the plugin declares them. Unknown commands and malformed values are skipped.
func ParseMultiAddonManagerConfig(data []byte) map[string]any {
	byConvar := make(map[string]mamSetting, len(mamSettings))
	for _, s := range mamSettings {
		byConvar[s.convar] = s
	}
	values := map[string]any{}
	for _, line := range strings.Split(string(data), "\n") {
		tokens := cfgTokens(line)
		if len(tokens) < 2 {
			continue
		}
		s, ok := byConvar[strings.ToLower(tokens[0])]
		if !ok {
			continue
		}
		raw := strings.TrimSpace(tokens[1])
		switch s.kind {
		case "string":
			values[s.key] = raw
		case "float":
			if f, err := strconv.ParseFloat(raw, 64); err == nil {
				values[s.key] = f
			}
		case "bool":
			if f, err := strconv.ParseFloat(raw, 64); err == nil {
				values[s.key] = f != 0
			} else if b, err := strconv.ParseBool(raw); err == nil {
				values[s.key] = b
			}
		}
	}
	return values
}

// cfgTokens splits one cfg line the way the console does: quoted strings are
// one token, "//" outside quotes starts a comment, and ';' ends the command.
func cfgTokens(line string) []string {
	var tokens []string
	var cur strings.Builder
	quoted, have := false, false
	flush := func() {
		if have {
			tokens = append(tokens, cur.String())
		}
		cur.Reset()
		have = false
	}
	for i := 0; i < len(line); i++ {
		c := line[i]
		switch {
		case quoted && c == '"':
			quoted = false
			flush()
		case quoted:
			cur.WriteByte(c)
		case c == '"':
			flush()
			quoted, have = true, true
		case c == '/' && i+1 < len(line) && line[i+1] == '/', c == ';':
			flush()
			return tokens
		case c == ' ' || c == '\t' || c == '\r':
			flush()
		default:
			cur.WriteByte(c)
			have = true
		}
	}
	flush()
	return tokens
}
