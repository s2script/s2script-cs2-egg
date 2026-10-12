// SPDX-License-Identifier: GPL-3.0-or-later
package config

import (
	"errors"
	"strconv"
	"time"
)

type CacheNode struct {
	Enabled  bool              `json:"enabled"`
	Root     string            `json:"root"`
	Origins  []string          `json:"origins"`
	Pins     map[string]string `json:"pins"`
	MaxBytes int64             `json:"max_bytes"`
}
type WorkshopNode struct {
	Enabled          bool     `json:"enabled"`
	Root             string   `json:"root"`
	TrustedImporters []string `json:"trusted_importers"`
	Items            []string `json:"items"`
	Mode             string   `json:"mode"`
	Mount            string   `json:"mount"`
}
type WorkshopClient struct {
	Enabled     bool     `json:"enabled"`
	Items       []string `json:"items"`
	Mode        string   `json:"mode"`
	Mount       string   `json:"mount"`
	WaitSeconds int      `json:"wait_seconds"`
}

func (w WorkshopClient) Validate() error {
	if len(w.Items) > 100 || w.WaitSeconds < 0 || w.WaitSeconds > 300 {
		return errors.New("invalid workshop selection/wait bound")
	}
	if w.Mode != "" && w.Mode != "copy" && w.Mode != "symlink" && w.Mode != "hardlink" {
		return errors.New("invalid workshop mode")
	}
	if w.Mount != "" && !cleanAbsolute(w.Mount) {
		return errors.New("workshop mount must be absolute")
	}
	for _, s := range w.Items {
		n, e := strconv.ParseUint(s, 10, 64)
		if e != nil || n == 0 || strconv.FormatUint(n, 10) != s {
			return errors.New("invalid workshop item identity")
		}
	}
	return nil
}
func (w WorkshopClient) Wait() time.Duration {
	if w.WaitSeconds == 0 {
		return 30 * time.Second
	}
	return time.Duration(w.WaitSeconds) * time.Second
}
func (n *Node) validateStores() error {
	if n.Cache.Enabled && n.Cache.Root == "" {
		n.Cache.Root = "/srv/s2node/cache"
	}
	if n.Workshop.Enabled && n.Workshop.Root == "" {
		n.Workshop.Root = "/srv/s2node/workshop"
	}
	roots := []string{}
	if n.Cache.Enabled {
		roots = append(roots, n.Cache.Root)
	}
	if n.Workshop.Enabled {
		roots = append(roots, n.Workshop.Root)
	}
	if n.Shared.Enabled {
		roots = append(roots, n.Shared.Root)
	}
	for i, r := range roots {
		if !cleanAbsolute(r) || overlaps(r, n.StateDir) || overlaps(r, n.AdminSocket) {
			return errors.New("node store overlaps private node paths")
		}
		for _, other := range roots[:i] {
			if overlaps(r, other) {
				return errors.New("node stores overlap")
			}
		}
	}
	for _, srv := range n.Servers {
		for _, r := range roots {
			if overlaps(r, srv.Volume) || srv.HelperSocket != "" && overlaps(r, srv.HelperSocket) {
				return errors.New("node store overlaps volume/helper socket")
			}
		}
	}
	for _, id := range n.Workshop.TrustedImporters {
		known := false
		for _, srv := range n.Servers {
			known = known || srv.ID == id
		}
		if !known {
			return errors.New("trusted importer must name registered server")
		}
	}
	return (WorkshopClient{Items: n.Workshop.Items, Mode: n.Workshop.Mode, Mount: n.Workshop.Mount}).Validate()
}
