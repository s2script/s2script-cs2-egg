// SPDX-License-Identifier: GPL-3.0-or-later
// Package events defines unfiltered, credential-redacted runtime facts.
package events

import (
	"context"
	"time"
)

type Event struct {
	Severity      string            `json:"severity"`
	CorrelationID string            `json:"correlation_id"`
	Version       int               `json:"version"`
	ID            string            `json:"id"`
	Type          string            `json:"type"`
	ServerID      string            `json:"server_id"`
	NodeID        string            `json:"node_id,omitempty"`
	RunID         string            `json:"run_id"`
	At            time.Time         `json:"at"`
	Reason        string            `json:"reason,omitempty"`
	Details       map[string]string `json:"details,omitempty"`
}

// Sink must honor cancellation; Run publishes serially with a bounded context.
// Filtering and remote delivery belong to the sink, after raw events are built.
type Sink interface {
	Publish(context.Context, Event) error
}
