// SPDX-License-Identifier: GPL-3.0-or-later
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"github.com/s2script/s2script-cs2-egg/internal/buildinfo"
	"github.com/s2script/s2script-cs2-egg/internal/config"
	"github.com/s2script/s2script-cs2-egg/internal/protocol"
	"github.com/s2script/s2script-cs2-egg/internal/releasecontract"
	"github.com/s2script/s2script-cs2-egg/internal/selfupdate"
	"github.com/s2script/s2script-cs2-egg/internal/storeio"
	"io"
	"net"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

func projectManager(c config.Node) *selfupdate.Manager {
	return &selfupdate.Manager{Root: c.ProjectUpdates.Root, Client: selfupdate.Client{Enabled: c.ProjectUpdates.Enabled, Version: c.ProjectUpdates.Version, AllowDowngrade: c.ProjectUpdates.AllowDowngrade}, Required: releasecontract.Inventory().NodeContainer.Capabilities, Preflight: func(ctx context.Context, m releasecontract.Manifest) error { return selfupdate.CheckHelpers(ctx, c, m) }}
}
func projectCLI(command string, args []string, out, errs io.Writer) int {
	f := flag.NewFlagSet(command, flag.ContinueOnError)
	f.SetOutput(errs)
	path := f.String("config", "/etc/s2node/node.json", "operator-owned node policy")
	request := f.String("request", "", "stable request ID")
	id := f.String("id", "", "job ID")
	offset := f.Int("offset", 0, "history page offset")
	_ = f.Bool("json", true, "JSON output")
	if f.Parse(args) != nil || f.NArg() != 0 {
		return 2
	}
	cfg, e := config.LoadNode(*path)
	if e != nil {
		fmt.Fprintln(errs, e)
		return 1
	}
	op := strings.TrimPrefix(command, "project-")
	if op == "launch" || op == "bootstrap" {
		if e = selfupdate.OperatorFile(*path, false); e != nil {
			fmt.Fprintln(errs, e)
			return 1
		}
		m := projectManager(cfg)
		ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer cancel()
		if op == "launch" {
			exe, e := os.Executable()
			if e != nil || selfupdate.OperatorFile(exe, true) != nil {
				fmt.Fprintln(errs, "pinned operator-owned launcher executable required")
				return 1
			}
			return (selfupdate.Launcher{Manager: m, ConfigPath: *path, Timeout: time.Duration(cfg.ProjectUpdates.StartupSeconds) * time.Second}).Run(ctx)
		}
		if !cfg.ProjectUpdates.Enabled {
			fmt.Fprintln(errs, selfupdate.ErrDisabled)
			return 1
		}
		if cfg.ProjectUpdates.Version == "latest" {
			fmt.Fprintln(errs, "bootstrap requires an exact administrator-selected release version")
			return 1
		}
		r, e := m.Client.Check(ctx, cfg.ProjectUpdates.Channel, cfg.ProjectUpdates.Version)
		if e == nil {
			e = m.Stage(ctx, r)
		}
		if e == nil {
			e = m.Initialize(ctx, r.Version)
		}
		if e != nil {
			fmt.Fprintln(errs, e)
			return 1
		}
		json.NewEncoder(out).Encode(map[string]string{"state": "initialized", "version": r.Version})
		return 0
	}
	if op != "status" && op != "check" && op != "stage" && op != "activate" {
		fmt.Fprintln(errs, "unknown project command")
		return 2
	}
	r := selfupdate.Request{Operation: op, RequestID: *request, ID: *id, Offset: *offset}
	if op != "status" && r.RequestID == "" {
		r.RequestID = storeio.ID()
		fmt.Fprintln(errs, "request:", r.RequestID)
	}
	ctx, cancel := context.WithTimeout(context.Background(), protocol.Timeout)
	defer cancel()
	conn, e := (&net.Dialer{}).DialContext(ctx, "unix", cfg.AdminSocket)
	if e != nil {
		fmt.Fprintln(errs, "private node administrator socket unavailable")
		return 1
	}
	client := protocol.NewClient(conn)
	defer client.Close()
	var welcome protocol.Welcome
	var result json.RawMessage
	if e = client.Call(ctx, "hello", protocol.Hello{Capabilities: []string{protocol.CapProjectUpdate}}, &welcome); e == nil {
		e = client.Call(ctx, protocol.CapProjectUpdate, r, &result)
	}
	if e != nil {
		fmt.Fprintln(errs, e)
		return 1
	}
	json.NewEncoder(out).Encode(result)
	return 0
}
func validateManagedPolicy(path string, cfg config.Node) error {
	if !cfg.ProjectUpdates.Enabled {
		return nil
	}
	if e := selfupdate.OperatorFile(path, false); e != nil {
		return e
	}
	if cfg.ProjectUpdates.Activate && !projectManager(cfg).ManagedExecutable(buildinfo.Version) {
		return errors.New("automatic activation requires the pinned launcher and a verified versioned node executable")
	}
	return nil
}
