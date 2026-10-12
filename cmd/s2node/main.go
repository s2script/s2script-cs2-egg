// SPDX-License-Identifier: GPL-3.0-or-later
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/s2script/s2script-cs2-egg/deploy"
	"github.com/s2script/s2script-cs2-egg/internal/alerts"
	"github.com/s2script/s2script-cs2-egg/internal/config"
	"github.com/s2script/s2script-cs2-egg/internal/events"
	"github.com/s2script/s2script-cs2-egg/internal/node"
	"github.com/s2script/s2script-cs2-egg/internal/protocol"
)

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }
func run(args []string, out, errs io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(errs, "usage: s2node run|config-check|status|doctor --config PATH | install --directory PATH")
		return 2
	}
	command := args[0]
	flags := flag.NewFlagSet("s2node", flag.ContinueOnError)
	flags.SetOutput(errs)
	path := flags.String("config", "/etc/s2node/node.json", "node configuration")
	directory := flags.String("directory", "", "install staging directory")
	if flags.Parse(args[1:]) != nil || flags.NArg() != 0 {
		return 2
	}
	if command == "install" {
		if *directory == "" {
			fmt.Fprintln(errs, "install requires --directory for reviewable staging")
			return 2
		}
		if e := install(*directory); e != nil {
			fmt.Fprintln(errs, "cannot stage installation:", e)
			return 1
		}
		fmt.Fprintln(out, "binary and unit staged; host service not enabled; follow docs/node.md for users, groups, paths and configuration")
		return 0
	}
	switch command {
	case "run", "config-check", "status", "doctor":
	default:
		fmt.Fprintln(errs, "unknown command")
		return 2
	}
	cfg, e := config.LoadNode(*path)
	if e != nil {
		fmt.Fprintln(errs, "node configuration error:", e)
		return 1
	}
	if command == "status" {
		ctx, cancel := context.WithTimeout(context.Background(), protocol.Timeout)
		defer cancel()
		conn, e := (&net.Dialer{}).DialContext(ctx, "unix", cfg.AdminSocket)
		if e != nil {
			fmt.Fprintln(errs, "administrator socket unavailable; state unknown")
			return 1
		}
		client := protocol.NewClient(conn)
		defer client.Close()
		var welcome protocol.Welcome
		var result json.RawMessage
		if e = client.Call(ctx, "hello", protocol.Hello{Capabilities: []string{protocol.CapStatus}}, &welcome); e == nil {
			e = client.Call(ctx, "status", struct{}{}, &result)
		}
		if e != nil {
			fmt.Fprintln(errs, "node status unavailable:", e)
			return 1
		}
		if e = json.NewEncoder(out).Encode(result); e != nil {
			return 1
		}
		return 0
	}
	var sink events.Sink
	var dispatcher *alerts.Dispatcher
	if cfg.Webhook.URL != "" {
		dispatcher, e = alerts.NewDiscord(alerts.Config{URL: cfg.Webhook.URL, Events: cfg.Webhook.Events, QueueCapacity: cfg.Webhook.QueueCapacity, MaxRetries: cfg.Webhook.MaxRetries, AttemptTimeout: time.Duration(cfg.Webhook.TimeoutSeconds) * time.Second}, nil)
		if e != nil {
			fmt.Fprintln(errs, "invalid node webhook settings:", e)
			return 1
		}
		defer dispatcher.Close(context.Background())
		sink = dispatcher
	}
	core, e := node.New(cfg, sink)
	if e != nil {
		fmt.Fprintln(errs, "node prerequisites unavailable:", e)
		return 1
	}
	switch command {
	case "config-check":
		fmt.Fprintln(out, "node configuration valid; configured volumes and access groups available; service sandbox and remote delivery untested")
		return 0
	case "doctor":
		fmt.Fprintln(out, "configuration: valid\nidentity: configured socket plus expected peer UID; container mount confinement required\nserver sockets: daemon-owned 0660; configured access GID required\nstatus: reported by runtime protocol; no report means unknown\nexisting runtime/artifact leases and status socket default to 0600; a separate UID cannot use group membership to access them\nadministrative stopped operations require cooperative caller-owned locks or an explicitly reviewed narrow access contract; inaccessible never means stopped")
		for _, s := range cfg.Servers {
			for _, name := range []string{".runtime-lease", ".artifact-lock", ".s2egg-status.sock"} {
				path := filepath.Join(s.Volume, name)
				if e := syscall.Access(path, 6); e != nil {
					fmt.Fprintf(out, "%s %s: unavailable to this process; stopped state unknown\n", s.ID, name)
				} else {
					fmt.Fprintf(out, "%s %s: path access available; stopped state not assessed\n", s.ID, name)
				}
			}
		}
		fmt.Fprintln(out, "operator must provision systemd supplementary groups and ReadWritePaths for egg directories; doctor does not change permissions or acquire game locks")
		return 0
	case "run":
		ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer cancel()
		if e = core.Run(ctx); e != nil {
			fmt.Fprintln(errs, "node stopped:", e)
			return 1
		}
	}
	return 0
}
func install(dir string) error {
	abs, e := filepath.Abs(dir)
	if e != nil {
		return e
	}
	if e = os.MkdirAll(abs, 0755); e != nil {
		return e
	}
	source, e := os.Executable()
	if e != nil {
		return e
	}
	in, e := os.Open(source)
	if e != nil {
		return e
	}
	defer in.Close()
	out, e := os.OpenFile(filepath.Join(abs, "s2node"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0755)
	if e != nil {
		return e
	}
	_, e = io.Copy(out, in)
	if closeErr := out.Close(); e == nil {
		e = closeErr
	}
	if e != nil {
		return e
	}
	unit, e := os.OpenFile(filepath.Join(abs, "s2node.service"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644)
	if e != nil {
		return e
	}
	_, e = unit.Write(deploy.Unit)
	if closeErr := unit.Close(); e == nil {
		e = closeErr
	}
	return e
}
