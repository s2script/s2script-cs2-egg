// SPDX-License-Identifier: GPL-3.0-or-later
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/s2script/s2script-cs2-egg/deploy"
	"github.com/s2script/s2script-cs2-egg/internal/alerts"
	"github.com/s2script/s2script-cs2-egg/internal/cache"
	"github.com/s2script/s2script-cs2-egg/internal/config"
	"github.com/s2script/s2script-cs2-egg/internal/events"
	"github.com/s2script/s2script-cs2-egg/internal/node"
	"github.com/s2script/s2script-cs2-egg/internal/nodetypes"
	"github.com/s2script/s2script-cs2-egg/internal/observability"
	"github.com/s2script/s2script-cs2-egg/internal/protocol"
	"github.com/s2script/s2script-cs2-egg/internal/redaction"
	"github.com/s2script/s2script-cs2-egg/internal/scheduler"
	"github.com/s2script/s2script-cs2-egg/internal/shared"
	"github.com/s2script/s2script-cs2-egg/internal/wings"
	"github.com/s2script/s2script-cs2-egg/internal/workshop"
)

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }
func run(args []string, out, errs io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(errs, "usage: s2node run|config-check|status|top|doctor|logs|logs-list|logs-read|crashes|crash-read|cleanup-plan|cleanup-apply|operations-job|delivery-status|delivery-archive|delivery-finalize|delivery-migrate|dumps|dump-retain|dump-export|shared-build|shared-prune|shared-attach|cache-status|cache-sync|cache-prune|workshop-status|workshop-sync|workshop-prune|helper-run --config PATH | install --directory PATH")
		return 2
	}
	command := args[0]
	if isOperationsCommand(command) {
		return operationsCLI(command, args[1:], out, errs)
	}
	flags := flag.NewFlagSet("s2node", flag.ContinueOnError)
	flags.SetOutput(errs)
	_ = flags.Bool("json", false, "JSON output")
	source := flags.String("source", "", "cache source: s2script/metamod/mam/registry")
	name := flags.String("name", "", "registry package identity")
	version := flags.String("version", "", "exact artifact version")
	path := flags.String("config", "/etc/s2node/node.json", "node configuration")
	serverID := flags.String("server", "", "registered server ID for shared-attach")
	generation := flags.String("generation", "", "published generation ID")
	mode := flags.String("mode", "symlink", "attachment mode: symlink/copy/hardlink")
	jobID := flags.String("job", "", "durable update job ID")
	allowStart := flags.Bool("allow-start", false, "explicitly authorize offline job resumption to start server")
	directory := flags.String("directory", "", "install staging directory")
	if flags.Parse(args[1:]) != nil || flags.NArg() != 0 {
		return 2
	}
	if command == "helper-run" {
		cfg, e := shared.LoadHelperConfig(*path)
		if e != nil {
			fmt.Fprintln(errs, "helper configuration:", e)
			return 1
		}
		ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer cancel()
		if e = shared.RunHelper(ctx, cfg); e != nil {
			fmt.Fprintln(errs, "helper stopped:", e)
			return 1
		}
		return 0
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
	case "cache-sync", "cache-status", "cache-prune", "workshop-status", "workshop-sync", "workshop-prune", "run", "config-check", "status", "doctor", "shared-build", "shared-prune", "shared-attach", "updates-list", "updates-retry", "updates-cancel":
	default:
		fmt.Fprintln(errs, "unknown command")
		return 2
	}
	cfg, e := config.LoadNode(*path)
	if e != nil {
		fmt.Fprintln(errs, "node configuration error:", e)
		return 1
	}
	if command == "updates-list" || command == "updates-retry" || command == "updates-cancel" {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		conn, e := (&net.Dialer{}).DialContext(ctx, "unix", cfg.AdminSocket)
		if e != nil {
			fmt.Fprintln(errs, "administrator socket unavailable")
			return 1
		}
		client := protocol.NewClient(conn)
		defer client.Close()
		var welcome protocol.Welcome
		var result json.RawMessage
		e = client.Call(ctx, "hello", protocol.Hello{Capabilities: []string{protocol.CapUpdateAdmin}}, &welcome)
		if e == nil {
			e = client.Call(ctx, protocol.CapUpdateAdmin, protocol.UpdateAdmin{Operation: strings.TrimPrefix(command, "updates-"), JobID: *jobID, AllowStart: *allowStart}, &result)
		}
		if e != nil {
			fmt.Fprintln(errs, e)
			return 1
		}
		json.NewEncoder(out).Encode(result)
		return 0
	}
	if strings.HasPrefix(command, "cache-") || strings.HasPrefix(command, "workshop-") {
		ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer cancel()
		if e := storeCommand(ctx, cfg, command, *serverID, *source, *name, *version, out); e != nil {
			fmt.Fprintln(errs, e)
			return 1
		}
		return 0
	}
	if command == "shared-build" || command == "shared-prune" || command == "shared-attach" {
		if !cfg.Shared.Enabled {
			fmt.Fprintln(errs, "shared module is not enabled")
			return 1
		}
		store, e := shared.New(sharedOptions(cfg))
		if e != nil {
			fmt.Fprintln(errs, e)
			return 1
		}
		ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer cancel()
		var result any
		switch command {
		case "shared-build":
			result, e = store.Build(ctx)
		case "shared-prune":
			result, e = store.Prune(ctx)
		case "shared-attach":
			var srv node.Server
			for _, v := range cfg.Servers {
				if v.ID == *serverID {
					srv = node.Server{ID: v.ID, Volume: v.Volume}
				}
			}
			if srv.ID == "" {
				fmt.Fprintln(errs, "unknown registered server")
				return 1
			}
			e = store.Attach(ctx, srv, *generation, *mode)
			result = "attached while stopped; container mount readiness is checked on next startup"
		}
		if e != nil {
			fmt.Fprintln(errs, e)
			return 1
		}
		json.NewEncoder(out).Encode(result)
		return 0
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
		fmt.Fprintf(out, "observability: enabled=%v privateMetrics=%q\nlogs: %d bytes × %d/server; tail %d bytes; crashes %d bytes × %d/server\nqueue archives: exact retained evidence, 64 segments/256 MiB; no TTL deletion\nbinary dumps: separate sensitive opt-in helper policy; no automatic upload\n", cfg.Observability.Enabled, cfg.Observability.PrivateMetrics, cfg.Observability.LogBytes, cfg.Observability.LogFiles, cfg.Observability.TailBytes, cfg.Observability.CrashBytes, cfg.Observability.CrashFiles)
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
		if cfg.Observability.Enabled {
			m := &observability.Module{Root: filepath.Join(cfg.StateDir, "observability"), Config: cfg.Observability, Secrets: redaction.Secrets(cfg.Webhook.URL)}
			if e = core.AddModule(m, []string{protocol.CapOperations, nodetypes.GrantOperationalEvents}); e != nil {
				fmt.Fprintln(errs, e)
				return 1
			}
		}
		if cfg.Cache.Enabled {
			s, e := cache.New(cacheOptions(cfg))
			if e != nil {
				fmt.Fprintln(errs, e)
				return 1
			}
			if e = core.AddModule(&cache.Module{Store: s, Policy: cache.Policy{Pins: cfg.Cache.Pins}}, []string{protocol.CapCache}); e != nil {
				fmt.Fprintln(errs, e)
				return 1
			}
		}
		if cfg.Workshop.Enabled {
			s, e := workshop.New(workshopOptions(cfg))
			if e != nil {
				fmt.Fprintln(errs, e)
				return 1
			}
			if e = core.AddModule(&workshop.Module{Store: s}, []string{protocol.CapWorkshop}); e != nil {
				fmt.Fprintln(errs, e)
				return 1
			}
		}
		if cfg.Shared.Enabled {
			store, e := shared.New(sharedOptions(cfg))
			if e != nil {
				fmt.Fprintln(errs, e)
				return 1
			}
			if cfg.Updates.Enabled {
				power, err := wings.New(cfg.Updates.Wings)
				if err != nil {
					fmt.Fprintln(errs, err)
					return 1
				}
				engine, err := scheduler.Open(scheduler.Options{Directory: filepath.Join(cfg.StateDir, "updates"), Power: power, Status: core.Status, Pin: store.PinTarget, Select: store.SelectTarget, Clear: store.ClearTarget, Attach: func(ctx context.Context, id, generation, mode string) error {
					for _, srv := range core.Servers() {
						if srv.ID == id {
							return store.Attach(ctx, srv, generation, mode)
						}
					}
					return fmt.Errorf("unknown configured server")
				}})
				if err != nil {
					fmt.Fprintln(errs, err)
					return 1
				}
				policies := map[string]scheduler.ServerPolicy{}
				for _, srv := range cfg.Servers {
					if srv.Updates.Enabled {
						policies[srv.ID] = scheduler.ServerPolicy{Policy: scheduler.FromConfig(srv.Updates.Policy), AttachMode: srv.Updates.AttachMode}
					}
				}
				updates := &scheduler.Module{Engine: engine, Policies: policies, Interval: time.Duration(cfg.Updates.PollSeconds) * time.Second, Poll: func(ctx context.Context) (scheduler.Target, error) {
					g, _, err := store.PollUpdate(ctx, nil)
					return scheduler.Target{Generation: g.ID, Build: g.BuildID}, err
				}}
				if err = core.AddModule(updates, updates.Capabilities()); err != nil {
					engine.Close()
					fmt.Fprintln(errs, err)
					return 1
				}
			}
			if e = core.AddModule(&shared.Module{Store: store}, []string{protocol.CapPrepare}); e != nil {
				fmt.Fprintln(errs, e)
				return 1
			}
		}
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
	if e != nil {
		return e
	}
	helper, e := os.OpenFile(filepath.Join(abs, "s2node-helper@.service"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644)
	if e != nil {
		return e
	}
	_, e = helper.Write(deploy.HelperUnit)
	return errors.Join(e, helper.Close())
}

func sharedOptions(c config.Node) shared.Options {
	o := shared.Options{Root: c.Shared.Root, SteamCMD: c.Shared.SteamCMD, ReserveBytes: c.Shared.ReserveBytes, Helpers: map[string]shared.HelperEndpoint{}}
	for _, s := range c.Servers {
		o.Registered = append(o.Registered, node.Server{ID: s.ID, Volume: s.Volume})
		if s.HelperSocket != "" {
			o.Helpers[s.ID] = shared.HelperEndpoint{Socket: s.HelperSocket, UID: s.PeerUID, GID: s.AccessGID}
		}
	}
	return o
}
