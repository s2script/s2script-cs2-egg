// SPDX-License-Identifier: GPL-3.0-or-later
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"github.com/s2script/s2script-cs2-egg/internal/config"
	"github.com/s2script/s2script-cs2-egg/internal/guard"
	"github.com/s2script/s2script-cs2-egg/internal/protocol"
	"github.com/s2script/s2script-cs2-egg/internal/redaction"
	"github.com/s2script/s2script-cs2-egg/internal/storeio"
	"io"
	"net"
	"net/netip"
	"strings"
)

func guardCLI(command string, args []string, out, errs io.Writer) int {
	f := flag.NewFlagSet(command, flag.ContinueOnError)
	f.SetOutput(errs)
	path := f.String("config", "/etc/s2node/node.json", "node configuration")
	server := f.String("server", "", "registered server ID")
	id := f.String("id", "", "action audit ID")
	request := f.String("request", "", "stable request ID")
	address := f.String("address", "", "canonical public IPv4 source")
	seconds := f.Int("seconds", 0, "temporary block duration (policy default when omitted)")
	offset := f.Int("offset", 0, "block list offset")
	_ = f.Bool("json", false, "JSON output")
	if f.Parse(args) != nil || f.NArg() != 0 {
		return 2
	}
	operation := strings.TrimPrefix(command, "guard-")
	switch operation {
	case "blocks", "why", "block", "unblock", "rates", "preview", "refresh", "info":
	default:
		fmt.Fprintln(errs, "unknown guard command")
		return 2
	}
	r := guard.AdminRequest{Operation: operation, ServerID: *server, ID: *id, Seconds: *seconds, Offset: *offset}
	if operation == "block" || operation == "unblock" {
		a, e := netip.ParseAddr(*address)
		if e != nil {
			fmt.Fprintln(errs, "valid source IP required")
			return 2
		}
		r.Address = a
		r.RequestID = *request
		if r.RequestID == "" {
			r.RequestID = storeio.ID()
		}
		fmt.Fprintln(errs, "request:", r.RequestID)
	}
	cfg, e := config.LoadNode(*path)
	if e != nil {
		fmt.Fprintln(errs, e)
		return 1
	}
	ctx, cancel := context.WithTimeout(context.Background(), protocol.Timeout)
	defer cancel()
	conn, e := (&net.Dialer{}).DialContext(ctx, "unix", cfg.AdminSocket)
	if e != nil {
		fmt.Fprintln(errs, "private administrator socket unavailable")
		return 1
	}
	client := protocol.NewClient(conn)
	defer client.Close()
	var welcome protocol.Welcome
	var result json.RawMessage
	e = client.Call(ctx, "hello", protocol.Hello{Capabilities: []string{protocol.CapGuardAdmin}}, &welcome)
	if e == nil {
		e = client.Call(ctx, protocol.CapGuardAdmin, r, &result)
	}
	if e != nil {
		fmt.Fprintln(errs, redaction.Redact(e.Error(), redaction.Secrets(cfg.Webhook.URL)))
		return 1
	}
	if e = json.NewEncoder(out).Encode(result); e != nil {
		return 1
	}
	return 0
}
