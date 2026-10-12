// SPDX-License-Identifier: GPL-3.0-or-later
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/s2script/s2script-cs2-egg/internal/config"
)

func main() { os.Exit(run(os.Args[1:], os.LookupEnv, os.Stdout, os.Stderr)) }

func run(args []string, lookup func(string) (string, bool), out, errs io.Writer) int {
	usage := func() int {
		fmt.Fprintln(errs, "usage: s2egg config check|show [--root PATH] | s2egg doctor [--root PATH]")
		return 2
	}
	if len(args) == 0 {
		return usage()
	}
	command := ""
	switch args[0] {
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
		fmt.Fprintln(errs, "command is not implemented in this configuration milestone")
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
		if steamcmd, err := exec.LookPath("steamcmd"); err == nil {
			fmt.Fprintln(out, "steamcmd:", steamcmd)
		} else {
			fmt.Fprintln(out, "steamcmd: missing from PATH")
			missing = true
		}
		for _, entry := range []struct {
			name, path string
			directory  bool
		}{{"CS2 executable", "game/bin/linuxsteamrt64/cs2", false}, {"Metamod directory", "game/csgo/addons/metamod", true}, {"S2Script directory", "game/csgo/addons/s2script", true}} {
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
