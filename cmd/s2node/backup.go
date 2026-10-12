// SPDX-License-Identifier: GPL-3.0-or-later
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"github.com/s2script/s2script-cs2-egg/internal/backup"
	"github.com/s2script/s2script-cs2-egg/internal/config"
	"github.com/s2script/s2script-cs2-egg/internal/protocol"
	"github.com/s2script/s2script-cs2-egg/internal/redaction"
	"github.com/s2script/s2script-cs2-egg/internal/storeio"
	"io"
	"net"
	"strings"
)

func backupCLI(command string, args []string, out, errs io.Writer) int {
	f := flag.NewFlagSet(command, flag.ContinueOnError)
	f.SetOutput(errs)
	path := f.String("config", "/etc/s2node/node.json", "node configuration")
	server := f.String("server", "", "registered server ID")
	id := f.String("id", "", "backup, preview, recovery or job ID")
	request := f.String("request", "", "stable request ID (reuse to inspect same admission)")
	confirm := f.String("confirm", "", "exact preview digest for restore, archive digest for recovery release")
	mode := f.String("mode", "consistent", "consistent or live-best-effort")
	offset := f.Int("offset", 0, "page offset")
	doc := f.String("document", "plan", "plan or manifest")
	_ = f.Bool("json", false, "JSON output")
	if f.Parse(args) != nil || f.NArg() != 0 {
		return 2
	}
	op := strings.TrimPrefix(command, "backup-")
	switch op {
	case "now":
		op = "create"
	case "preview", "restore", "list", "job", "document", "status", "recovery-release":
	default:
		fmt.Fprintln(errs, "unknown backup command")
		return 2
	}
	if (op == "restore" || op == "recovery-release") && !storeio.Digest(*confirm) {
		fmt.Fprintln(errs, "explicit --confirm DIGEST from the reviewed preview/archive is required")
		return 2
	}
	cfg, e := config.LoadNode(*path)
	if e != nil {
		fmt.Fprintln(errs, e)
		return 1
	}
	r := backup.Request{Operation: op, ServerID: *server, ID: *id, Confirm: *confirm, Offset: *offset, Document: *doc}
	if op == "create" {
		r.Mode = *mode
	}
	if op == "create" || op == "preview" || op == "restore" || op == "recovery-release" {
		r.RequestID = *request
		if r.RequestID == "" {
			r.RequestID = storeio.ID()
		}
		fmt.Fprintln(errs, "request:", r.RequestID)
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
	e = client.Call(ctx, "hello", protocol.Hello{Capabilities: []string{protocol.CapBackupAdmin}}, &welcome)
	if e == nil {
		e = client.Call(ctx, protocol.CapBackupAdmin, r, &result)
	}
	if e != nil {
		fmt.Fprintln(errs, redaction.Redact(e.Error(), redaction.Secrets(cfg.Webhook.URL)))
		return 1
	}
	if op == "document" {
		var page backup.Reply
		if e = json.Unmarshal(result, &page); e != nil {
			return 1
		}
		fmt.Fprint(out, string(page.Data))
		if page.Next != 0 {
			fmt.Fprintf(errs, "next offset: %d\n", page.Next)
		}
		return 0
	}
	if e = json.NewEncoder(out).Encode(result); e != nil {
		return 1
	}
	return 0
}
