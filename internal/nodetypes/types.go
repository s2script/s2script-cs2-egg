// SPDX-License-Identifier: GPL-3.0-or-later
// Package nodetypes holds the node contracts shared by optional modules and clients.
package nodetypes

import (
	"context"
	"github.com/s2script/s2script-cs2-egg/internal/config"
	"github.com/s2script/s2script-cs2-egg/internal/events"
	"github.com/s2script/s2script-cs2-egg/internal/protocol"
	"github.com/s2script/s2script-cs2-egg/internal/server"
	"time"
)

type PrivateHelper struct {
	Socket string `json:"socket"`
	UID    uint32 `json:"uid"`
	GID    int    `json:"gid"`
}

type Server struct {
	Helper     *PrivateHelper    `json:"-"`
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
	ModuleHealth() map[string]Health
	PublishOperational(context.Context, events.OperationalEvent) (events.Admission, error)
	Subscribe(int) (*Subscription, error)
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

const GrantOperationalEvents = "internal.operational-events"

type Observation struct {
	Event                     events.Event
	Server                    *Server
	ReceivedAt                time.Time
	Source, Replay, SessionID string
	Sequence                  uint64
}
type Subscription struct {
	Events  <-chan Observation
	Close   func()
	Dropped func() uint64
}
