// SPDX-License-Identifier: GPL-3.0-or-later
package plugins

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/s2script/s2script-cs2-egg/internal/artifact"
	"github.com/s2script/s2script-cs2-egg/internal/config"
	"github.com/s2script/s2script-cs2-egg/internal/runtimelease"
)

var ErrDependencyEvidenceUnavailable = errors.New("dependency provider evidence unavailable; registry reconciliation is required before enable/removal/disable")

var ErrReconcileUnavailable = errors.New("registry reconciliation is unavailable in this milestone")

// ReconcileRequest is passed with runtimelease and artifact.Acquire held, after
// Recover. Implementations stage/Apply a combined transaction and must NOT
// reacquire locks. They preserve user policy; resolved facts belong in the lock.
type ReconcileRequest struct {
	Root       string
	PolicyPath string
	Policy     Policy
	Overrides  map[string]bool
	Installed  []Installed
}
type ReconcileFunc func(context.Context, ReconcileRequest) error

// RunCLI implements `s2egg plugins ...`. An injected resolver is the integration
// boundary for registry work; an absent resolver fails visibly after add saves
// the operator's requested version and disabled choice.
func RunCLI(ctx context.Context, args []string, lookup func(string) (string, bool), out, errs io.Writer, reconcile ReconcileFunc) int {
	usage := func() int {
		fmt.Fprintln(errs, "usage: s2egg plugins list|inspect|add|remove|pin|unpin|enable|disable|reconcile [NAME] [--root PATH] [--version SEMVER] [--disabled] [--with NAME,...]")
		return 2
	}
	if len(args) == 0 {
		return usage()
	}
	command := args[0]
	args = args[1:]
	needsName := false
	switch command {
	case "list", "reconcile":
	case "inspect", "add", "remove", "pin", "unpin", "enable", "disable":
		needsName = true
	default:
		return usage()
	}
	name := ""
	if needsName {
		if len(args) == 0 || strings.HasPrefix(args[0], "-") || !validName(args[0]) {
			return usage()
		}
		name = args[0]
		args = args[1:]
	}
	fs := flag.NewFlagSet("plugins", flag.ContinueOnError)
	fs.SetOutput(errs)
	root := fs.String("root", ".", "server root directory")
	version := fs.String("version", "", "desired version")
	disabled := fs.Bool("disabled", false, "install disabled")
	enabled := fs.Bool("enabled", false, "install enabled")
	with := fs.String("with", "", "comma-separated optional plugin identities")
	if e := fs.Parse(args); e != nil {
		return 2
	}
	if fs.NArg() != 0 {
		return usage()
	}
	invalid := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name != "root" && !(command == "add" && (f.Name == "version" || f.Name == "disabled" || f.Name == "enabled" || f.Name == "with")) && !(command == "pin" && f.Name == "version") {
			invalid = true
		}
	})
	if invalid || (*disabled && *enabled) || (command == "pin" && !ExactVersion(*version)) {
		return usage()
	}
	fail := func(e error) int { fmt.Fprintln(errs, "plugin error:", e); return 1 }
	cfg, e := config.LoadServer(*root, lookup)
	if e != nil {
		return fail(e)
	}
	mutation := command != "list" && command != "inspect"
	var releaseRuntime func() error
	running := false
	if mutation {
		releaseRuntime, e = runtimelease.Acquire(cfg.Root)
		if errors.Is(e, runtimelease.ErrHeld) {
			running = true
		} else if e != nil {
			return fail(e)
		}
		if releaseRuntime != nil {
			defer releaseRuntime()
		}
	}
	release, e := artifact.Acquire(cfg.Root)
	if e != nil {
		return fail(e)
	}
	defer release()
	if mutation && !running {
		if e = artifact.Recover(cfg.Root); e != nil {
			return fail(e)
		}
	} else {
		pending, e := artifact.HasPending(cfg.Root)
		if e != nil {
			return fail(e)
		}
		if pending {
			return fail(errors.New("pending artifact recovery requires a stopped managed runtime"))
		}
	}
	policy, e := ReadPolicy(cfg.Plugins.File)
	if e != nil {
		return fail(e)
	}
	installed, e := Discover(cfg.Root)
	if e != nil {
		return fail(e)
	}
	emit := func(v any) int {
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		if e = enc.Encode(v); e != nil {
			return fail(e)
		}
		return 0
	}
	if command == "list" {
		return emit(struct {
			Policy    Policy      `json:"policy"`
			Installed []Installed `json:"installed"`
		}{policy, installed})
	}
	index := -1
	for i, p := range installed {
		if p.Name == name {
			index = i
			break
		}
	}
	entry, exists := policy.Plugins[name]
	if !exists {
		entry = Entry{Source: "registry", Version: "*"}
		if index >= 0 {
			entry.Source = installed[index].Source
		}
	}
	if command == "inspect" {
		if !exists && index < 0 {
			return fail(errors.New("plugin is not desired or installed"))
		}
		var actual *Installed
		if index >= 0 {
			actual = &installed[index]
		}
		return emit(struct {
			Desired   *Entry     `json:"desired,omitempty"`
			Installed *Installed `json:"installed,omitempty"`
		}{func() *Entry {
			if exists {
				return &entry
			}
			return nil
		}(), actual})
	}
	if command == "reconcile" {
		if running {
			return fail(runtimelease.ErrHeld)
		}
		if reconcile == nil {
			return fail(ErrReconcileUnavailable)
		}
		if e = reconcile(ctx, ReconcileRequest{cfg.Root, cfg.Plugins.File, policy, cfg.Plugins.Overrides, installed}); e != nil {
			return fail(e)
		}
		fmt.Fprintln(out, "plugins reconciled")
		return 0
	}
	stage, e := os.MkdirTemp("", "s2egg-plugin-cli-")
	if e != nil {
		return fail(e)
	}
	defer os.RemoveAll(stage)
	changes := []artifact.Change{}
	switch command {
	case "add":
		if exists {
			return fail(errors.New("plugin already has policy; use pin/enable/disable to change it"))
		}
		entry.Source = "registry"
		entry.Version = *version
		if *with != "" {
			entry.With = strings.Split(*with, ",")
		}
		if *disabled || *enabled {
			v := !*disabled
			entry.Enabled = &v
		}
		policy.Plugins[name] = entry
	case "pin", "unpin":
		if !exists && index < 0 {
			return fail(errors.New("plugin is not desired or installed"))
		}
		entry.Version = "*"
		if command == "pin" {
			entry.Version = *version
		}
		policy.Plugins[name] = entry
	case "enable", "disable":
		if !exists && index < 0 {
			return fail(errors.New("plugin is not desired or installed"))
		}
		want := command == "enable"
		entry.Enabled = &want
		if index >= 0 && installed[index].LastCheckStatus != "missing" && installed[index].Enabled != want {
			if running {
				return fail(runtimelease.ErrHeld)
			}
			p := &installed[index]
			if !p.Managed || p.Verification != "verified" {
				return fail(errors.New("plugin is not owned; preserve manual/local artifact and stage policy by editing its entry"))
			}
			// Required interface providers need the registry dependency checker. A
			// direct artifact move must not disable a provider under active consumers.
			if want {
				if e = checkDirectEnable(ctx, cfg, p.Name, installed); e != nil {
					return fail(e)
				}
			} else {
				if e = checkDirectRemoval(ctx, cfg, *p, installed, true); e != nil {
					return fail(e)
				}
			}

			old, _ := Target(p.Filename, p.Enabled)
			p.Enabled = want
			target, _ := Target(p.Filename, want)
			source, e := stageInstalled(stage, cfg.Root, old, p.SHA256)
			if e != nil {
				return fail(e)
			}
			changes = append(changes, artifact.Change{Path: "game/csgo/" + old, Remove: true}, artifact.Change{Path: "game/csgo/" + target, Source: source})
			lock, e := StageLock(stage, installed)
			if e != nil {
				return fail(e)
			}
			changes = append(changes, lock)
		}
		policy.Plugins[name] = entry
	case "remove":
		if running {
			return fail(runtimelease.ErrHeld)
		}
		if !exists && index < 0 {
			return fail(errors.New("plugin is not desired or installed"))
		}
		if index >= 0 {
			p := installed[index]
			if !p.Managed || p.Verification != "verified" {
				return fail(errors.New("plugin is not owned; manual/local files are preserved"))
			}
			if e = checkDirectRemoval(ctx, cfg, p, installed, false); e != nil {
				return fail(e)
			}
			if p.LastCheckStatus != "missing" {
				target, _ := Target(p.Filename, p.Enabled)
				changes = append(changes, artifact.Change{Path: "game/csgo/" + target, Remove: true})
			}
			installed = append(installed[:index], installed[index+1:]...)
			for i := range installed {
				parents := []string{}
				for _, parent := range installed[i].Parents {
					if parent != name {
						parents = append(parents, parent)
					}
				}
				installed[i].Parents = parents
			}
			lock, e := StageLock(stage, installed)
			if e != nil {
				return fail(e)
			}
			changes = append(changes, lock)
		}
		delete(policy.Plugins, name)
	}
	change, e := stagePolicy(stage, cfg.Root, cfg.Plugins.File, policy)
	if e != nil {
		return fail(e)
	}
	changes = append(changes, change)
	if e = artifact.Apply(ctx, cfg.Root, changes); e != nil {
		return fail(e)
	}
	fmt.Fprintln(out, "plugin policy saved")
	if command == "add" {
		if reconcile == nil {
			return fail(ErrReconcileUnavailable)
		}
		if running {
			return fail(runtimelease.ErrHeld)
		}
		if e = reconcile(ctx, ReconcileRequest{cfg.Root, cfg.Plugins.File, policy, cfg.Plugins.Overrides, installed}); e != nil {
			return fail(e)
		}
		fmt.Fprintln(out, "plugins reconciled")
	}
	return 0
}
func stagePolicy(stage, root, path string, policy Policy) (artifact.Change, error) {
	if e := validatePolicy(&policy); e != nil {
		return artifact.Change{}, e
	}
	relative, e := filepath.Rel(root, path)
	if e != nil || !safeMember(relative) {
		return artifact.Change{}, errors.New("policy path escapes server root")
	}
	if relative == LockFilename || strings.HasPrefix(relative, "game/") || strings.HasPrefix(relative, ".artifact-") || relative == ".runtime-lease" {
		return artifact.Change{}, errors.New("policy path conflicts with managed runtime files")
	}
	b, e := json.MarshalIndent(policy, "", "  ")
	if e != nil {
		return artifact.Change{}, e
	}
	if len(b) > 1<<20 {
		return artifact.Change{}, errors.New("plugin policy exceeds size limit")
	}
	source := filepath.Join(stage, "policy.json")
	if e = os.WriteFile(source, append(b, '\n'), 0600); e != nil {
		return artifact.Change{}, e
	}
	return artifact.Change{Path: filepath.ToSlash(relative), Source: source}, nil
}
func stageInstalled(stage, root, relative, digest string) (string, error) {
	b, e := readSafe(filepath.Join(root, "game/csgo", relative), maxPluginBytes)
	if e != nil {
		return "", e
	}
	if fileSHA256(b) != digest {
		return "", errors.New("plugin changed during inspection")
	}
	path := filepath.Join(stage, "plugin.s2sp")
	e = os.WriteFile(path, b, 0600)
	return path, e
}

// PanelOverride uses only explicit panel choices. Effective bundled defaults
// would erase the distinction between default inheritance and operator policy.
func PanelOverride(overrides map[string]bool, name string) string {
	if v, ok := overrides[name]; ok {
		if v {
			return "1"
		}
		return "0"
	}
	return "default"
}
func fileSHA256(b []byte) string { return fmt.Sprintf("%x", sha256.Sum256(b)) }
