// SPDX-License-Identifier: GPL-3.0-or-later
package selfupdate

import (
	"context"
	"errors"
	"github.com/s2script/s2script-cs2-egg/internal/config"
	"github.com/s2script/s2script-cs2-egg/internal/guard"
	"github.com/s2script/s2script-cs2-egg/internal/releasecontract"
	"github.com/s2script/s2script-cs2-egg/internal/volumehelper"
)

// CheckHelpers probes fixed operator endpoints only, without touching leases.
func CheckHelpers(ctx context.Context, c config.Node, m releasecontract.Manifest) error {
	for _, s := range c.Servers {
		var operations []string
		if c.Shared.Enabled && s.HelperSocket != "" {
			operations = append(operations, "shared.attach")
		}
		if c.Workshop.Enabled && s.HelperSocket != "" {
			operations = append(operations, "workshop.attach")
		}
		if c.Observability.Enabled && s.HelperSocket != "" {
			operations = append(operations, "operations.private")
		}
		if c.Backup.Enabled {
			operations = append(operations, "backup.catalog")
		}
		if len(operations) == 0 {
			continue
		}
		if s.HelperSocket == "" {
			return errors.New("required private helper discovery unavailable")
		}
		h, e := volumehelper.Discover(ctx, s.HelperSocket, s.PeerUID)
		if e != nil {
			return errors.New("required volume helper discovery unavailable; provision/upgrade helper separately")
		}
		if e = releasecontract.HelperCompatible(m.Compatibility.Helper, h, operations); e != nil {
			return e
		}
	}
	if c.Guard.Enabled {
		required := false
		for _, p := range c.Guard.Policies {
			required = required || p.Enabled && p.Action == "block"
		}
		if required {
			h, e := guard.Call(ctx, c.Guard.HelperSocket, c.Guard.HelperUID, guard.Request{Operation: "info"})
			if e != nil || h.State != "ready" {
				return errors.New("required guard helper discovery unavailable or uncertain")
			}
			has := map[string]bool{}
			for _, v := range h.Capabilities {
				has[v] = true
			}
			for _, v := range []string{"guard-v1", "block", "unblock", "list", "rates", "refresh"} {
				if !has[v] {
					return errors.New("guard helper contract missing")
				}
			}
			actual := releasecontract.Helper{DiscoveryVersion: 1, Protocol: releasecontract.Range{Min: 1, Max: 1}, Provides: []string{"guard.enforce.v1"}}
			if e = releasecontract.HelperCompatible(m.Compatibility.Helper, actual, []string{"guard.enforce"}); e != nil {
				return e
			}
		}
	}
	return nil
}
