// SPDX-License-Identifier: GPL-3.0-or-later
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"github.com/s2script/s2script-cs2-egg/internal/config"
	"github.com/s2script/s2script-cs2-egg/internal/node"
	"github.com/s2script/s2script-cs2-egg/internal/observability"
	"github.com/s2script/s2script-cs2-egg/internal/privateops"
	"github.com/s2script/s2script-cs2-egg/internal/protocol"
	"github.com/s2script/s2script-cs2-egg/internal/redaction"
	"io"
	"net"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

func isOperationsCommand(c string) bool {
	switch c {
	case "top", "logs", "logs-list", "logs-read", "crashes", "crash-read", "cleanup-plan", "cleanup-apply", "operations-job", "delivery-archive", "delivery-status", "delivery-finalize", "delivery-migrate", "dumps", "dump-retain", "dump-export":
		return true
	}
	return false
}
func operationsCLI(command string, args []string, out, errs io.Writer) int {
	f := flag.NewFlagSet(command, flag.ContinueOnError)
	f.SetOutput(errs)
	path := f.String("config", "/etc/s2node/node.json", "node config")
	server := f.String("server", "", "registered server ID")
	id := f.String("id", "", "sealed artifact/plan/job ID (source filename for dump-retain)")
	run := f.String("run", "", "exact runtime RunID to finalize")
	request := f.String("request", "", "stable job request identity")
	namespace := f.String("namespace", "logs", "logs/crashes/dumps")
	private := f.Bool("private", false, "use configured actual-UID helper")
	offset := f.Int64("offset", 0, "list/read offset")
	limit := f.Int("limit", 128, "list page bound")
	retain := f.Int("retain", 0, "number of newest sealed artifacts to retain")
	age := f.Int64("age-seconds", 0, "minimum cleanup age")
	output := f.String("output", "", "exclusive private local export destination")
	once := f.Bool("once", false, "one top snapshot")
	_ = f.Bool("json", false, "JSON output")
	if f.Parse(args) != nil || f.NArg() != 0 {
		return 2
	}
	cfg, e := config.LoadNode(*path)
	if e != nil {
		fmt.Fprintln(errs, e)
		return 1
	}
	report := func(e error) int {
		fmt.Fprintln(errs, redaction.Redact(e.Error(), redaction.Secrets(cfg.Webhook.URL)))
		return 1
	}
	if command == "delivery-migrate" {
		core, e := node.New(cfg, nil)
		if e != nil {
			return report(e)
		}
		if e = core.MigrateDelivery(); e != nil {
			return report(e)
		}
		fmt.Fprintln(out, "delivery queue migrated to format 2; private pre-migration copy retained; old binaries cannot read format 2")
		return 0
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	call := func(kind string, in, out any) error {
		c, e := (&net.Dialer{}).DialContext(ctx, "unix", cfg.AdminSocket)
		if e != nil {
			return errors.New("private administrator socket unavailable")
		}
		client := protocol.NewClient(c)
		defer client.Close()
		requestCtx, stop := context.WithTimeout(ctx, protocol.Timeout)
		defer stop()
		var welcome protocol.Welcome
		if e = client.Call(requestCtx, "hello", protocol.Hello{Capabilities: []string{kind}}, &welcome); e != nil {
			return e
		}
		return client.Call(requestCtx, kind, in, out)
	}
	if command == "top" {
		for {
			var status observability.Report
			if e = call(protocol.CapStatus, struct{}{}, &status); e != nil {
				return report(e)
			}
			status.At = time.Now().UTC()
			if e = json.NewEncoder(out).Encode(status); e != nil {
				return report(e)
			}
			if *once {
				return 0
			}
			select {
			case <-ctx.Done():
				return 0
			case <-time.After(2 * time.Second):
			}
		}
	}
	r := protocol.OperationsRequest{ServerID: *server, RunID: *run, ID: *id, RequestID: *request, Namespace: *namespace, Private: *private, Offset: *offset, Limit: *limit, Retain: *retain, AgeSeconds: *age}
	switch command {
	case "logs":
		r.Operation = "tail"
		r.Namespace = "logs"
	case "logs-list":
		r.Operation = "list"
		r.Namespace = "logs"
	case "logs-read":
		r.Operation = "read"
		r.Namespace = "logs"
		r.Limit = 64 << 10
	case "crashes":
		r.Operation = "list"
		r.Namespace = "crashes"
	case "crash-read":
		r.Operation = "read"
		r.Namespace = "crashes"
		r.Limit = 64 << 10
	case "dumps":
		r.Operation = "list"
		r.Namespace = "dumps"
		r.Private = true
	case "dump-retain":
		r.Operation = "dump-retain"
		r.Namespace = "dumps"
		r.Private = true
	case "dump-export":
		r.Operation = "read"
		r.Namespace = "dumps"
		r.Private = true
		r.Limit = 64 << 10
		r.Offset = 0
	case "operations-job":
		r.Operation = "job"
	case "delivery-archive":
		r.Operation = "archive"
	case "delivery-status":
		r.Operation = "archive-status"
	case "delivery-finalize":
		r.Operation = "finalize"
	default:
		r.Operation = command
	}
	if command == "dump-export" {
		if *output == "" {
			return report(errors.New("dump-export requires --output; binary dumps are sensitive and never printed"))
		}
		f, e := os.OpenFile(*output, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if e != nil {
			return report(e)
		}
		success := false
		defer func() {
			f.Close()
			if !success {
				os.Remove(*output)
			}
		}()
		h := sha256.New()
		size := int64(-1)
		digest := ""
		for {
			var reply privateops.Reply
			if e = call(protocol.CapOperations, r, &reply); e != nil {
				return report(e)
			}
			if !reply.Sensitive || reply.ID != r.ID || reply.Size < 0 || reply.Size > 2<<30 || len(reply.Digest) != 64 {
				return report(errors.New("invalid sensitive export identity"))
			}
			if size < 0 {
				size = reply.Size
				digest = reply.Digest
			}
			if size != reply.Size || digest != reply.Digest {
				return report(errors.New("dump changed during export"))
			}
			if len(reply.Data) == 0 && r.Offset < size {
				return report(errors.New("incomplete dump export"))
			}
			if _, e = io.MultiWriter(f, h).Write(reply.Data); e != nil {
				return report(e)
			}
			r.Offset += int64(len(reply.Data))
			if r.Offset == size {
				break
			}
			if r.Offset > size {
				return report(errors.New("dump export exceeds size"))
			}
		}
		if hex.EncodeToString(h.Sum(nil)) != digest {
			return report(errors.New("dump export checksum mismatch"))
		}
		if e = errors.Join(f.Sync(), f.Close()); e != nil {
			return report(e)
		}
		success = true
		fmt.Fprintf(out, "sensitive local dump exported: %d bytes; no remote upload\n", size)
		return 0
	}
	var result json.RawMessage
	if e = call(protocol.CapOperations, r, &result); e != nil {
		return report(e)
	}
	if strings.HasSuffix(command, "-read") || command == "logs" {
		var reply privateops.Reply
		if json.Unmarshal(result, &reply) != nil || reply.Sensitive {
			return report(errors.New("invalid text artifact reply"))
		}
		_, e = out.Write(reply.Data)
	} else {
		e = json.NewEncoder(out).Encode(result)
	}
	if e != nil {
		return report(e)
	}
	return 0
}
