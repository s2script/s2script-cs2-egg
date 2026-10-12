// SPDX-License-Identifier: GPL-3.0-or-later
package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"unicode"

	"github.com/s2script/s2script-cs2-egg/internal/catalog"
)

// Server is schema version 1. Root comes only from the caller, never from JSON/env.
type Server struct {
	Workshop      WorkshopClient `json:"workshop"`
	Updates       ServerUpdates  `json:"updates"`
	SchemaVersion int            `json:"schema_version"`
	Root          string         `json:"-"`
	Identity      string         `json:"identity"`
	Shared        SharedClient   `json:"shared"`
	Steam         Steam          `json:"steam"`
	Frameworks    Frameworks     `json:"frameworks"`
	Plugins       PluginPolicy   `json:"plugins"`
	Launch        Launch         `json:"launch"`
	Readiness     Readiness      `json:"readiness"`
	Webhook       Webhook        `json:"webhook"`
}
type SharedClient struct {
	Mount     string   `json:"mount"`
	Mode      string   `json:"mode"`
	Overrides []string `json:"overrides"`
}
type Steam struct {
	AppID          int    `json:"appid"`
	GSLT           string `json:"gslt"`
	AllowTokenless bool   `json:"allow_tokenless"`
	Update         bool   `json:"update"`
	Validate       bool   `json:"validate"`
}
type Frameworks struct {
	S2ScriptUpdate  bool   `json:"s2script_update"`
	MetamodUpdate   bool   `json:"metamod_update"`
	Channel         string `json:"channel"`
	S2ScriptVersion string `json:"s2script_version"`
	MetamodVersion  string `json:"metamod_version"`
	HUD             bool   `json:"hud"`
}
type PluginPolicy struct {
	ReviewedOnly bool   `json:"reviewed_only"`
	File         string `json:"file"`
	// Bundled holds effective enablement after saved overrides and panel policy.
	Bundled map[string]bool `json:"bundled"`
	// Overrides records explicit panel choices; default intentionally adds no override.
	Overrides map[string]bool `json:"-"`
}
type Launch struct {
	DebugPort    int      `json:"debug_port"`
	IP           string   `json:"ip"`
	Port         int      `json:"port"`
	Map          string   `json:"map"`
	MaxPlayers   int      `json:"max_players"`
	GameType     int      `json:"game_type"`
	GameMode     int      `json:"game_mode"`
	CustomParams string   `json:"custom_params"`
	ExtraArgs    []string `json:"-"`
}
type Readiness struct {
	TimeoutSeconds      int `json:"timeout_seconds"`
	PollIntervalSeconds int `json:"poll_interval_seconds"`
}
type Webhook struct {
	QueueCapacity  int      `json:"queue_capacity"`
	MaxRetries     int      `json:"max_retries"`
	URL            string   `json:"url"`
	Events         []string `json:"events"`
	TimeoutSeconds int      `json:"timeout_seconds"`
}

// MarshalJSON and Format redact secrets even when callers print the whole config.
func (s Server) MarshalJSON() ([]byte, error) {
	type plain Server
	if s.Steam.GSLT != "" {
		s.Steam.GSLT = "[REDACTED]"
	}
	if s.Webhook.URL != "" {
		s.Webhook.URL = "[REDACTED]"
	}
	return json.Marshal(struct {
		plain
		Root string `json:"root"`
	}{plain(s), s.Root})
}
func (s Server) Format(f fmt.State, verb rune) { b, _ := s.MarshalJSON(); _, _ = f.Write(b) }

// Individual settings remain safe when logged separately from Server.
func (s Steam) MarshalJSON() ([]byte, error) {
	type plain Steam
	if s.GSLT != "" {
		s.GSLT = "[REDACTED]"
	}
	return json.Marshal(plain(s))
}
func (s Steam) Format(f fmt.State, verb rune) { b, _ := s.MarshalJSON(); _, _ = f.Write(b) }
func (w Webhook) MarshalJSON() ([]byte, error) {
	type plain Webhook
	if w.URL != "" {
		w.URL = "[REDACTED]"
	}
	return json.Marshal(plain(w))
}
func (w Webhook) Format(f fmt.State, verb rune) { b, _ := w.MarshalJSON(); _, _ = f.Write(b) }

// LoadServer validates without writing files, executing commands, or making requests.
// Precedence: built-in defaults, optional egg/server.json, explicitly present env.
func LoadServer(root string, lookup func(string) (string, bool)) (Server, error) {
	fail := func(err error) (Server, error) { return Server{}, err }
	abs, err := filepath.Abs(root)
	if err != nil {
		return fail(errors.New("invalid server root"))
	}
	abs, err = filepath.EvalSymlinks(abs)
	if err != nil {
		return fail(errors.New("server root must exist"))
	}
	info, err := os.Stat(abs)
	if err != nil || !info.IsDir() {
		return fail(errors.New("server root must be a directory"))
	}
	s := Server{
		SchemaVersion: 1, Root: abs,
		Shared:     SharedClient{Mount: "/mnt/s2node", Mode: "symlink"},
		Steam:      Steam{AppID: 730, Update: true},
		Frameworks: Frameworks{S2ScriptUpdate: true, MetamodUpdate: true, Channel: "stable", S2ScriptVersion: "latest", MetamodVersion: "latest"},
		Plugins:    PluginPolicy{File: "s2script-plugins.json", Bundled: map[string]bool{}, Overrides: map[string]bool{}},
		Launch:     Launch{IP: "0.0.0.0", Port: 27015, Map: "de_dust2", MaxPlayers: 32},
		Readiness:  Readiness{TimeoutSeconds: 120, PollIntervalSeconds: 1},
		Webhook:    Webhook{Events: []string{"server.up", "server.down", "server.crash", "server.start_failed"}, TimeoutSeconds: 10, QueueCapacity: 64, MaxRetries: 2},
	}
	for _, p := range catalog.Bundled() {
		s.Plugins.Bundled[p.Name] = p.DefaultEnabled
	}
	cfgPath, err := withinRoot(abs, "egg/server.json")
	if err != nil {
		return fail(fmt.Errorf("egg/server.json: %w", err))
	}
	f, err := os.Open(cfgPath)
	if err == nil {
		defer f.Close()
		data, readErr := io.ReadAll(io.LimitReader(f, (1<<20)+1))
		if readErr != nil || len(data) > 1<<20 {
			return fail(errors.New("egg/server.json: unreadable or oversized configuration (limit 1 MiB)"))
		}
		// JSON null must not silently preserve a default for a malformed override.
		tokens := json.NewDecoder(bytes.NewReader(data))
		for {
			token, tokenErr := tokens.Token()
			if tokenErr == io.EOF {
				break
			}
			if tokenErr != nil || token == nil {
				return fail(errors.New("egg/server.json: invalid configuration JSON (null is not supported)"))
			}
		}
		d := json.NewDecoder(bytes.NewReader(data))
		d.DisallowUnknownFields()
		if err := d.Decode(&s); err != nil {
			return fail(errors.New("egg/server.json: invalid configuration JSON"))
		}
		if err := d.Decode(new(any)); err != io.EOF {
			return fail(errors.New("egg/server.json: trailing configuration data"))
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return fail(errors.New("egg/server.json: cannot read configuration"))
	}
	if e := s.Workshop.Validate(); e != nil {
		return s, e
	}
	if !filepath.IsAbs(s.Shared.Mount) || filepath.Clean(s.Shared.Mount) != s.Shared.Mount || s.Shared.Mount == "/" || (s.Shared.Mode != "symlink" && s.Shared.Mode != "copy" && s.Shared.Mode != "hardlink") {
		return fail(errors.New("shared mount must be an absolute clean path and mode symlink, copy or hardlink; hardlinks must be installed by host helper"))
	}
	for _, p := range s.Shared.Overrides {
		if p == "" || p == "." || filepath.IsAbs(p) || filepath.Clean(p) != p || strings.HasPrefix(p, "../") {
			return fail(errors.New("shared overrides must be relative file/directory paths"))
		}
	}
	if lookup == nil {
		lookup = os.LookupEnv
	}
	stringVars := map[string]*string{"SERVER_ID": &s.Identity, "SERVER_IP": &s.Launch.IP, "SRCDS_MAP": &s.Launch.Map, "STEAM_ACC": &s.Steam.GSLT, "CUSTOM_PARAMS": &s.Launch.CustomParams, "S2SCRIPT_CHANNEL": &s.Frameworks.Channel, "S2SCRIPT_VERSION": &s.Frameworks.S2ScriptVersion, "METAMOD_VERSION": &s.Frameworks.MetamodVersion, "S2SCRIPT_PLUGIN_FILE": &s.Plugins.File, "DISCORD_WEBHOOK_URL": &s.Webhook.URL}
	for k, p := range stringVars {
		if v, ok := lookup(k); ok {
			*p = v
		}
	}
	for k, p := range map[string]*int{"SRCDS_APPID": &s.Steam.AppID, "SERVER_PORT": &s.Launch.Port, "SRCDS_MAXPLAYERS": &s.Launch.MaxPlayers, "GAME_TYPE": &s.Launch.GameType, "GAME_MODE": &s.Launch.GameMode, "GDB_DEBUG_PORT": &s.Launch.DebugPort} {
		if v, ok := lookup(k); ok {
			n, err := strconv.Atoi(v)
			if err != nil {
				return fail(fmt.Errorf("%s must be an integer", k))
			}
			*p = n
		}
	}
	for k, p := range map[string]*bool{"S2SCRIPT_REVIEWED_ONLY": &s.Plugins.ReviewedOnly, "ALLOW_TOKENLESS": &s.Steam.AllowTokenless, "SRCDS_VALIDATE": &s.Steam.Validate, "S2SCRIPT_HUD": &s.Frameworks.HUD, "S2SCRIPT_UPDATE": &s.Frameworks.S2ScriptUpdate, "METAMOD_UPDATE": &s.Frameworks.MetamodUpdate} {
		if v, ok := lookup(k); ok {
			if v != "0" && v != "1" {
				return fail(fmt.Errorf("%s must be 0 or 1", k))
			}
			*p = v == "1"
		}
	}
	if v, ok := lookup("SRCDS_STOP_UPDATE"); ok {
		if v != "0" && v != "1" {
			return fail(errors.New("SRCDS_STOP_UPDATE must be 0 or 1"))
		}
		s.Steam.Update = v == "0"
	}
	if s.SchemaVersion != 1 {
		return fail(errors.New("unsupported server schema_version"))
	}
	if s.Steam.AppID != 730 {
		return fail(errors.New("SRCDS_APPID must be 730"))
	}
	if s.Launch.Port < 1 || s.Launch.Port > 65535 {
		return fail(errors.New("SERVER_PORT must be in 1..65535"))
	}
	if s.Launch.DebugPort < 0 || s.Launch.DebugPort > 65535 || (s.Launch.DebugPort != 0 && s.Launch.DebugPort == s.Launch.Port) {
		return fail(errors.New("GDB_DEBUG_PORT must be 0 (disabled) or 1..65535 distinct from SERVER_PORT"))
	}
	if s.Launch.MaxPlayers < 1 || s.Launch.MaxPlayers > 64 {
		return fail(errors.New("SRCDS_MAXPLAYERS must be in 1..64"))
	}
	if net.ParseIP(s.Launch.IP) == nil {
		return fail(errors.New("SERVER_IP must be an IP address"))
	}
	if s.Launch.GameType < 0 || s.Launch.GameMode < 0 {
		return fail(errors.New("GAME_TYPE and GAME_MODE must be nonnegative"))
	}
	if strings.TrimSpace(s.Launch.Map) == "" || strings.ContainsAny(s.Launch.Map, "\x00\r\n") {
		return fail(errors.New("SRCDS_MAP must be a nonempty map name"))
	}
	if strings.ContainsAny(s.Identity, "\x00\r\n") {
		return fail(errors.New("SERVER_ID contains invalid characters"))
	}
	if s.Steam.GSLT == "" && !s.Steam.AllowTokenless {
		return fail(errors.New("STEAM_ACC is required unless ALLOW_TOKENLESS=1"))
	}
	if strings.ContainsAny(s.Steam.GSLT, "\x00\r\n") {
		return fail(errors.New("STEAM_ACC contains invalid characters"))
	}
	if s.Frameworks.Channel != "stable" && s.Frameworks.Channel != "prerelease" {
		return fail(errors.New("S2SCRIPT_CHANNEL must be stable or prerelease"))
	}
	for k, v := range map[string]string{"S2SCRIPT_VERSION": s.Frameworks.S2ScriptVersion, "METAMOD_VERSION": s.Frameworks.MetamodVersion} {
		if v != "latest" && !versionPattern.MatchString(v) {
			return fail(fmt.Errorf("%s must be latest or an exact version", k))
		}
	}
	pluginPath, err := withinRoot(abs, s.Plugins.File)
	if err != nil {
		return fail(fmt.Errorf("S2SCRIPT_PLUGIN_FILE: %w", err))
	}
	s.Plugins.File = pluginPath
	known := map[string]bool{}
	for _, p := range catalog.Bundled() {
		known[p.Name] = true
	}
	for name := range s.Plugins.Bundled {
		if !known[name] {
			return fail(errors.New("plugins.bundled contains unknown plugin identity"))
		}
	}
	if s.Plugins.Bundled == nil {
		s.Plugins.Bundled = map[string]bool{}
	}
	s.Plugins.Overrides = map[string]bool{}
	for _, p := range catalog.Bundled() {
		if _, ok := s.Plugins.Bundled[p.Name]; !ok {
			s.Plugins.Bundled[p.Name] = p.DefaultEnabled
		}
		if v, ok := lookup(p.Variable); ok {
			switch v {
			case "default":
			case "0", "1":
				s.Plugins.Bundled[p.Name] = v == "1"
				s.Plugins.Overrides[p.Name] = v == "1"
			default:
				return fail(fmt.Errorf("%s must be default, 0 or 1", p.Variable))
			}
		}
	}
	if s.Readiness.TimeoutSeconds < 1 || s.Readiness.TimeoutSeconds > 3600 || s.Readiness.PollIntervalSeconds < 1 || s.Readiness.PollIntervalSeconds > s.Readiness.TimeoutSeconds {
		return fail(errors.New("invalid readiness timing"))
	}
	if s.Webhook.QueueCapacity < 1 || s.Webhook.QueueCapacity > 1024 || s.Webhook.MaxRetries < 0 || s.Webhook.MaxRetries > 10 {
		return fail(errors.New("invalid webhook queue_capacity or max_retries"))
	}
	if s.Webhook.TimeoutSeconds < 1 || s.Webhook.TimeoutSeconds > 60 {
		return fail(errors.New("invalid webhook timeout"))
	}
	if s.Webhook.URL != "" {
		u, err := url.Parse(s.Webhook.URL)
		if err != nil || u.Scheme != "https" || !discordHost(u.Host) || u.User != nil || u.RawQuery != "" || u.Fragment != "" || !webhookPath.MatchString(u.Path) {
			return fail(errors.New("DISCORD_WEBHOOK_URL must be an HTTPS Discord webhook URL"))
		}
	}
	allowedEvents := map[string]bool{"server.up": true, "server.down": true, "server.crash": true, "server.start_failed": true, "startup.failure": true, "server.restart": true, "server.update": true, "backup": true, "guard": true}
	for _, event := range s.Webhook.Events {
		if !allowedEvents[event] {
			return fail(errors.New("webhook.events contains unsupported event"))
		}
	}
	if s.Updates.Enabled {
		if s.Updates.PollSeconds < 10 || s.Updates.PollSeconds > 86400 {
			return fail(errors.New("standalone update poll_seconds must be 10..86400"))
		}
		s.Updates.Policy.Defaults()
		if err := s.Updates.Policy.Validate(); err != nil {
			return fail(err)
		}
	}
	s.Launch.ExtraArgs, err = parseArgs(s.Launch.CustomParams)
	if err != nil {
		return fail(fmt.Errorf("CUSTOM_PARAMS: %w", err))
	}
	return s, nil
}

var versionPattern = regexp.MustCompile(`^v?[0-9]+(?:\.[0-9]+){1,2}(?:[-+][A-Za-z0-9][A-Za-z0-9.-]*)?$`)
var webhookPath = regexp.MustCompile(`^/api(?:/v[0-9]+)?/webhooks/[0-9]+/[A-Za-z0-9_-]+$`)

func discordHost(host string) bool {
	return host == "discord.com" || host == "discordapp.com" || host == "canary.discord.com" || host == "ptb.discord.com"
}

// withinRoot resolves existing symlink ancestors, including for a not-yet-created file.
func withinRoot(root, rel string) (string, error) {
	if rel == "" || filepath.IsAbs(rel) {
		return "", errors.New("path must be relative within server root")
	}
	target := filepath.Join(root, rel)
	if !contained(root, target) {
		return "", errors.New("path escapes server root")
	}
	ancestor := target
	for {
		resolved, err := filepath.EvalSymlinks(ancestor)
		if err == nil {
			if !contained(root, resolved) {
				return "", errors.New("symlink escapes server root")
			}
			break
		}
		if !errors.Is(err, os.ErrNotExist) {
			return "", errors.New("cannot resolve path")
		}
		// ENOENT can mean an existing symlink has a missing target. Do not
		// discard that component: a later write would follow it outside root.
		if info, statErr := os.Lstat(ancestor); statErr == nil && info.Mode()&os.ModeSymlink != 0 {
			return "", errors.New("dangling symlink is not allowed")
		} else if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
			return "", errors.New("cannot inspect unresolved path")
		}
		parent := filepath.Dir(ancestor)
		if parent == ancestor {
			return "", errors.New("cannot resolve path")
		}
		ancestor = parent
	}
	if info, err := os.Stat(target); err == nil && !info.Mode().IsRegular() {
		return "", errors.New("path must name a regular file")
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", errors.New("cannot inspect path")
	}
	return target, nil
}
func contained(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// parseArgs handles quoting only. Shell metacharacters and substitutions are literal data.
func parseArgs(input string) ([]string, error) {
	args := []string{}
	var token strings.Builder
	var quote rune
	escaped, started := false, false
	for _, r := range input {
		if r == 0 {
			return nil, errors.New("NUL is not allowed")
		}
		if escaped {
			token.WriteRune(r)
			escaped = false
			started = true
			continue
		}
		if r == '\\' && quote != '\'' {
			escaped = true
			started = true
			continue
		}
		if quote != 0 {
			if r == quote {
				quote = 0
			} else {
				token.WriteRune(r)
			}
			continue
		}
		if r == '\'' || r == '"' {
			quote = r
			started = true
			continue
		}
		if unicode.IsSpace(r) {
			if started {
				args = append(args, token.String())
				token.Reset()
				started = false
			}
			continue
		}
		token.WriteRune(r)
		started = true
	}
	if quote != 0 || escaped {
		return nil, errors.New("unterminated quote or escape")
	}
	if started {
		args = append(args, token.String())
	}
	return args, nil
}
