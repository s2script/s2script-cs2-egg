// SPDX-License-Identifier: GPL-3.0-or-later
package observability

import (
	node "github.com/s2script/s2script-cs2-egg/internal/nodetypes"
	"github.com/s2script/s2script-cs2-egg/internal/server"
	"time"
)

type Report struct {
	At      time.Time                `json:"at"`
	Servers map[string]server.Status `json:"servers"`
	Modules map[string]node.Health   `json:"modules"`
}

func Snapshot(host node.Host) Report {
	r := Report{At: time.Now().UTC(), Servers: map[string]server.Status{}, Modules: host.ModuleHealth()}
	for _, s := range host.Servers() {
		r.Servers[s.ID] = host.Status(s.ID)
	}
	return r
}
