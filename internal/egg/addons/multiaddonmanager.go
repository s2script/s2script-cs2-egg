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
	"regexp"
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
// The settings move only while the plugin's config lists no addons (a missing
// file, or the defaults an S2Script release ships), so an operator's own
// addonmanager lists always win. MultiAddonManager's cfg directory is
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
	var out []byte
	existing, err := root.ReadFile(AddonManagerConfig)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		if out, err = json.MarshalIndent(values, "", "  "); err != nil {
			return err
		}
		out = append(out, '\n')
	case err != nil:
		return err
	default:
		// S2Script releases ship each bundled plugin's default config, so the
		// file usually exists by now. One that lists no addons holds no
		// operator choice yet: fill in the moved values, keeping its comments.
		merged, ok := mergeAddonManagerConfig(existing, values)
		if !ok {
			e.Log.Logf(logx.Warning, "MultiAddonManager settings were not moved: %s already lists addons. Copy anything still needed from %s by hand", AddonManagerConfig, mamConfig)
			return nil
		}
		out = merged
	}
	if err := root.MkdirAll(path.Dir(AddonManagerConfig), 0o755); err != nil {
		return err
	}
	tmp := AddonManagerConfig + ".tmp"
	if err := writeFile(root, tmp, bytes.NewReader(out), 0o644); err != nil {
		return err
	}
	if err := root.Rename(tmp, AddonManagerConfig); err != nil {
		root.Remove(tmp)
		return err
	}
	e.Log.Logf(logx.Success, "Moved MultiAddonManager settings to %s", AddonManagerConfig)
	return nil
}

// mergeAddonManagerConfig writes values into an existing JSONC config that
// lists no addons yet, line by line so its comments survive. It reports false
// when the file already lists addons or is not a config it can edit safely.
func mergeAddonManagerConfig(existing []byte, values map[string]any) ([]byte, bool) {
	var current map[string]any
	if json.Unmarshal(stripJSONComments(existing), &current) != nil {
		return nil, false
	}
	for _, key := range []string{"addons_extra", "addons_client_extra"} {
		if v, _ := current[key].(string); strings.TrimSpace(v) != "" {
			return nil, false
		}
	}
	lines := strings.Split(string(existing), "\n")
	done := map[string]bool{}
	for i, line := range lines {
		m := jsoncMember.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		v, ok := values[m[2]]
		if !ok || done[m[2]] {
			continue
		}
		encoded, err := json.Marshal(v)
		if err != nil {
			return nil, false
		}
		lines[i] = m[1] + string(encoded) + m[4]
		done[m[2]] = true
	}
	var missing []string
	for _, s := range mamSettings {
		if _, ok := values[s.key]; ok && !done[s.key] {
			encoded, err := json.Marshal(values[s.key])
			if err != nil {
				return nil, false
			}
			missing = append(missing, "  "+strconv.Quote(s.key)+": "+string(encoded)+",")
		}
	}
	if len(missing) > 0 {
		open := -1
		for i, line := range lines {
			if strings.TrimSpace(line) == "{" {
				open = i
				break
			}
		}
		if open < 0 {
			return nil, false
		}
		lines = append(lines[:open+1], append(missing, lines[open+1:]...)...)
	}
	out := []byte(strings.Join(lines, "\n"))
	// Keep the result only when it still parses and says what was moved.
	var check map[string]any
	if json.Unmarshal(stripJSONComments(out), &check) != nil {
		return nil, false
	}
	for key, want := range values {
		if fmt.Sprint(check[key]) != fmt.Sprint(want) {
			return nil, false
		}
	}
	return out, true
}

// jsoncMember matches one `"key": value` line, capturing the prefix, key,
// value and the trailing comma and comment.
var jsoncMember = regexp.MustCompile(`^(\s*"([a-z_]+)"\s*:\s*)("(?:[^"\\]|\\.)*"|[^,/\s]+)(\s*,?\s*(?://.*)?)$`)

// stripJSONComments removes // and /* */ comments outside strings and the
// trailing commas they can leave, enough to read S2Script's JSONC configs.
func stripJSONComments(data []byte) []byte {
	var out bytes.Buffer
	inString := false
	for i := 0; i < len(data); i++ {
		c := data[i]
		switch {
		case inString:
			out.WriteByte(c)
			if c == '\\' && i+1 < len(data) {
				i++
				out.WriteByte(data[i])
			} else if c == '"' {
				inString = false
			}
		case c == '"':
			inString = true
			out.WriteByte(c)
		case c == '/' && i+1 < len(data) && data[i+1] == '/':
			for i < len(data) && data[i] != '\n' {
				i++
			}
			out.WriteByte('\n')
		case c == '/' && i+1 < len(data) && data[i+1] == '*':
			end := bytes.Index(data[i+2:], []byte("*/"))
			if end < 0 {
				return out.Bytes()
			}
			i += end + 3
		default:
			out.WriteByte(c)
		}
	}
	return trailingComma.ReplaceAll(out.Bytes(), []byte("$1"))
}

var trailingComma = regexp.MustCompile(`,(\s*[}\]])`)

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
