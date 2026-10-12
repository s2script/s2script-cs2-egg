// SPDX-License-Identifier: GPL-3.0-or-later
// Package protocol defines the bounded local node wire contract.
package protocol

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"time"
)

const Version = 1
const MaxFrame = 1 << 20
const Timeout = 5 * time.Second
const (
	CapStatus   = "status"
	CapEvents   = "events"
	CapPrepare  = "game.prepare"
	CapCache    = "cache"
	CapWorkshop = "workshop"
	CapRestart  = "restart.schedule"
)

type Message struct {
	Version int             `json:"version"`
	ID      string          `json:"id"`
	Type    string          `json:"type"`
	Payload json.RawMessage `json:"payload"`
}
type Hello struct {
	Capabilities []string `json:"capabilities,omitempty"`
}
type Welcome struct {
	Capabilities []string `json:"capabilities"`
	ServerID     string   `json:"server_id"`
	NodeID       string   `json:"node_id"`
}
type Failure struct {
	Error string `json:"error"`
}
type Claim struct {
	RunID string `json:"run_id"`
}
type Ownership struct {
	Owner string `json:"owner"`
}
type Receipt struct {
	Result string `json:"result"`
}

// PrepareRequest reserves a generation only. No path or claimed server selector
// is accepted. Materialization belongs to the caller-held preparation adapter.
type PrepareRequest struct {
	AppID    int  `json:"app_id"`
	Validate bool `json:"validate"`
}
type Preparation struct {
	Reservation string `json:"reservation"`
	Generation  string `json:"generation"`
}
type CacheRequest struct {
	Operation string `json:"operation"`
}
type CacheResult struct {
	State string `json:"state"`
}
type WorkshopRequest struct {
	ItemID string `json:"item_id"`
}
type WorkshopResult struct {
	State string `json:"state"`
}
type RestartRequest struct {
	Policy          string `json:"policy"`
	MaxDelaySeconds int    `json:"max_delay_seconds"`
	WindowStart     string `json:"window_start,omitempty"`
	WindowEnd       string `json:"window_end,omitempty"`
}
type RestartResult struct {
	ScheduleID string `json:"schedule_id"`
}

func Decode(b []byte, v any) error {
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if e := d.Decode(v); e != nil {
		return errors.New("invalid payload")
	}
	if d.Decode(new(any)) != io.EOF {
		return errors.New("trailing payload")
	}
	return nil
}
func Write(w io.Writer, m Message) error {
	b, e := json.Marshal(m)
	if e != nil {
		return e
	}
	if len(b) > MaxFrame {
		return errors.New("frame exceeds 1 MiB")
	}
	var h [4]byte
	binary.BigEndian.PutUint32(h[:], uint32(len(b)))
	if _, e = w.Write(h[:]); e != nil {
		return e
	}
	_, e = w.Write(b)
	return e
}
func Read(r io.Reader) (Message, error) {
	var m Message
	var h [4]byte
	if _, e := io.ReadFull(r, h[:]); e != nil {
		return m, e
	}
	n := binary.BigEndian.Uint32(h[:])
	if n == 0 || n > MaxFrame {
		return m, errors.New("invalid frame size")
	}
	b := make([]byte, n)
	if _, e := io.ReadFull(r, b); e != nil {
		return m, e
	}
	e := Decode(b, &m)
	return m, e
}
