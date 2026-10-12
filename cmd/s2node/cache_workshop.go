// SPDX-License-Identifier: GPL-3.0-or-later
package main

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/s2script/s2script-cs2-egg/internal/cache"
	"github.com/s2script/s2script-cs2-egg/internal/config"
	"github.com/s2script/s2script-cs2-egg/internal/node"
	"github.com/s2script/s2script-cs2-egg/internal/workshop"
	"io"
	"os"
	"path/filepath"
	"strings"
)

func configuredServers(c config.Node) []node.Server {
	var out []node.Server
	for _, s := range c.Servers {
		v := node.Server{ID: s.ID, Volume: s.Volume, SocketPath: filepath.Join(s.Volume, "egg/s2node.sock"), Allocation: s.Allocation}
		if s.HelperSocket != "" {
			v.Helper = &node.PrivateHelper{Socket: s.HelperSocket, UID: s.PeerUID, GID: s.AccessGID}
		}
		out = append(out, v)
	}
	return out
}
func workshopOptions(c config.Node) workshop.Options {
	return workshop.Options{Root: c.Workshop.Root, TrustedImporters: c.Workshop.TrustedImporters, Items: c.Workshop.Items, Servers: configuredServers(c), Mode: c.Workshop.Mode, Mount: c.Workshop.Mount}
}
func cacheOptions(c config.Node) cache.Options {
	return cache.Options{Root: c.Cache.Root, Origins: c.Cache.Origins, MaxBytes: c.Cache.MaxBytes}
}
func storeCommand(ctx context.Context, c config.Node, command, server, source, name, version string, out io.Writer) error {
	if strings.HasPrefix(command, "cache-") {
		if !c.Cache.Enabled {
			return errors.New("cache disabled")
		}
		s, e := cache.New(cacheOptions(c))
		if e != nil {
			return e
		}
		if command == "cache-sync" {
			m := cache.Module{Store: s, Policy: cache.Policy{Pins: c.Cache.Pins}}
			v, e := m.Sync(ctx, source, name, version)
			if e != nil {
				return e
			}
			return json.NewEncoder(out).Encode(v)
		}
		if command == "cache-prune" {
			v, e := s.Prune(ctx)
			if e != nil {
				return e
			}
			return json.NewEncoder(out).Encode(v)
		}
		entries, e := os.ReadDir(filepath.Join(c.Cache.Root, "objects"))
		if e != nil {
			return e
		}
		var ids []string
		for _, f := range entries {
			ids = append(ids, f.Name())
		}
		return json.NewEncoder(out).Encode(ids)
	}
	if !c.Workshop.Enabled {
		return errors.New("workshop disabled")
	}
	s, e := workshop.New(workshopOptions(c))
	if e != nil {
		return e
	}
	if command == "workshop-sync" {
		if e = s.Start(ctx); e != nil {
			return e
		}
		defer (&workshop.Module{Store: s}).Close(context.Background())
	}
	var result any
	switch command {
	case "workshop-prune":
		result, e = s.Prune(ctx)
	case "workshop-sync":
		found := false
		for _, srv := range configuredServers(c) {
			if srv.ID == server {
				found = true
				result, e = s.Sync(ctx, srv)
				break
			}
		}
		if !found {
			e = errors.New("unknown registered server")
		}
	case "workshop-status":
		result, e = s.Status()
	default:
		e = errors.New("unknown store command")
	}
	if e != nil {
		return e
	}
	return json.NewEncoder(out).Encode(result)
}
