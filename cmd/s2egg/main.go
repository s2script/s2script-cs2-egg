// SPDX-License-Identifier: GPL-3.0-or-later
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/s2script/s2script-cs2-egg/internal/config"
	"github.com/s2script/s2script-cs2-egg/internal/plugins"
	runtime "github.com/s2script/s2script-cs2-egg/internal/server"
)

func main() { os.Exit(run(os.Args[1:], os.LookupEnv, os.Stdout, os.Stderr)) }

func run(args []string, lookup func(string) (string, bool), out, errs io.Writer) int {
	usage := func() int {
		fmt.Fprintln(errs, "usage: s2egg config check|show [--root PATH] | s2egg doctor [--root PATH] | s2egg run|prepare|status [--root PATH] | s2egg plugins COMMAND [NAME] [OPTIONS]")
		return 2
	}
	if len(args) == 0 {
		return usage()
	}
	command := ""
	switch args[0] {
	case "plugins":
		return plugins.RunCLI(context.Background(), args[1:], lookup, out, errs, func(ctx context.Context, req plugins.ReconcileRequest) error {
			cfg, e := config.LoadServer(req.Root, lookup)
			if e != nil {
				return e
			}
			return runtime.ReconcileLocked(ctx, cfg, req, errs)
		})
	case "run", "prepare", "status":
		command = args[0]
		args = args[1:]
	case "config":
		if len(args) < 2 || (args[1] != "check" && args[1] != "show") {
			return usage()
		}
		command = args[1]
		args = args[2:]
	case "doctor":
		command = "doctor"
		args = args[1:]
	default:
		fmt.Fprintln(errs, "unknown command")
		return usage()
	}
	flags := flag.NewFlagSet("s2egg", flag.ContinueOnError)
	flags.SetOutput(errs)
	root := flags.String("root", ".", "server root directory")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 {
		return usage()
	}
	server, err := config.LoadServer(*root, lookup)
	if err != nil {
		fmt.Fprintln(errs, "configuration error:", err)
		return 1
	}
	switch command {
	case "run":
		code, e := runtime.Run(context.Background(), server, nil, os.Stdin, out, errs)
		if e != nil {
			fmt.Fprintln(errs, "server error:", e)
		}
		return code
	case "prepare":
		// Keep the command alive to cancel and reap SteamCMD before preparation
		// releases its runtime/artifact locks.
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		if e := runtime.Prepare(ctx, server); e != nil {
			fmt.Fprintln(errs, "preparation error:", e)
			return 1
		}
		fmt.Fprintln(out, "server preparation complete")
	case "status":
		status, e := runtime.Snapshot(context.Background(), server.Root)
		if e != nil {
			if errors.Is(e, runtime.ErrNotRunning) {
				fmt.Fprintln(out, "managed runtime is not running")
			} else {
				fmt.Fprintln(errs, "status error:", e)
			}
			return 1
		}
		if e = json.NewEncoder(out).Encode(status); e != nil {
			return 1
		}
	case "check":
		fmt.Fprintln(out, "configuration valid (schema 1; CS2 app 730)")
	case "show":
		encoder := json.NewEncoder(out)
		encoder.SetIndent("", "  ")
		if err := encoder.Encode(server); err != nil {
			fmt.Fprintln(errs, "cannot encode configuration")
			return 1
		}
	case "doctor":
		fmt.Fprintln(out, "configuration: valid\ngame readiness is not assessed; this command inspects local prerequisites only")
		missing := false
		steamPath := filepath.Join(server.Root, "steamcmd/steamcmd.sh")
		if info, e := os.Stat(steamPath); e == nil && info.Mode().IsRegular() && info.Mode().Perm()&0111 != 0 {
			fmt.Fprintln(out, "steamcmd: persistent executable present")
		} else if info, e := os.Stat(filepath.Join(runtime.SteamSeed, "steamcmd.sh")); e == nil && info.Mode().IsRegular() {
			fmt.Fprintln(out, "steamcmd: image seed present; initialization required")
		} else {
			fmt.Fprintln(out, "steamcmd: missing persistent executable and image seed")
			missing = true
		}

		for _, entry := range []struct {
			name, path string
			directory  bool
		}{{"CS2 launcher", "game/cs2.sh", false}, {"CS2 executable", "game/bin/linuxsteamrt64/cs2", false}, {"Metamod directory", "game/csgo/addons/metamod", true}, {"S2Script directory", "game/csgo/addons/s2script", true}} {
			info, err := os.Stat(filepath.Join(server.Root, entry.path))
			if err != nil || info.IsDir() != entry.directory || (!entry.directory && info.Mode().Perm()&0111 == 0) {
				fmt.Fprintf(out, "%s: missing or invalid (%s)\n", entry.name, entry.path)
				missing = true
			} else {
				fmt.Fprintf(out, "%s: present (%s); runtime compatibility not checked\n", entry.name, entry.path)
			}
		}
		if missing {
			return 1
		}
	}
	return 0
}
