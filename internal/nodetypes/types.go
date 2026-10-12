// SPDX-License-Identifier: GPL-3.0-or-later
// Package nodetypes holds the node contracts shared by optional modules and clients.
package nodetypes

import (
	"context"
	"github.com/s2script/s2script-cs2-egg/internal/config"
	"github.com/s2script/s2script-cs2-egg/internal/events"
	"github.com/s2script/s2script-cs2-egg/internal/protocol"
	"github.com/s2script/s2script-cs2-egg/internal/server"
)

type Server struct {
	ID         string            `json:"id"`
	Volume     string            `json:"volume"`
	SocketPath string            `json:"socket_path"`
	Allocation config.Allocation `json:"allocation"`
}
type Health struct {
	Status      string `json:"status"`
	Explanation string `json:"explanation,omitempty"`
}
type Module interface {
	Name() string
	Start(context.Context, Host) error
	Health() Health
	Close(context.Context) error
}
type Host interface {
	Servers() []Server
	Publish(context.Context, events.Event) error
	Status(string) server.Status
	Dispatch(context.Context, string, string, any) (any, error)
}

// Action handlers receive the configured immutable server, never client paths.
// Modules are trusted in-process code, not a process isolation/security sandbox.
type Actions interface {
	Capabilities() []string
	Handle(context.Context, Server, string, protocol.Message) (any, error)
}
