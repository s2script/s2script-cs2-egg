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
	CapBackupAdmin   = "backup.admin"
	CapOperations    = "operations.admin"
	CapStatus        = "status"
	CapEvents        = "events"
	CapPrepare       = "game.prepare"
	CapCache         = "cache"
	CapWorkshop      = "workshop"
	CapRestart       = "restart.schedule"
	CapUpdateControl = "update.control"
	CapUpdateExecute = "update.execute"
	CapUpdateAdmin   = "update.admin"
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
	Operation   string `json:"operation,omitempty"`
	RunID       string `json:"run_id,omitempty"`
	RequestID   string `json:"request_id,omitempty"`
	Reservation string `json:"reservation,omitempty"`
	AppID       int    `json:"app_id"`
	Validate    bool   `json:"validate"`
}
type Preparation struct {
	BuildID     string `json:"build_id,omitempty"`
	Digest      string `json:"digest,omitempty"`
	Reservation string `json:"reservation"`
	Generation  string `json:"generation"`
}
type ArtifactSelection struct {
	Source         string `json:"source"`
	Name           string `json:"name,omitempty"`
	Version        string `json:"version"`
	SHA256         string `json:"sha256"`
	SourceRevision string `json:"source_revision,omitempty"`
	Integrity      string `json:"integrity,omitempty"`
}
type CacheRequest struct {
	Operation string            `json:"operation"`
	RequestID string            `json:"request_id,omitempty"`
	Handle    string            `json:"handle,omitempty"`
	Artifact  ArtifactSelection `json:"artifact"`
	Offset    int64             `json:"offset,omitempty"`
}
type CacheResult struct {
	State  string `json:"state"`
	Handle string `json:"handle,omitempty"`
	SHA256 string `json:"sha256,omitempty"`
	Size   int64  `json:"size,omitempty"`
	Data   []byte `json:"data,omitempty"`
	Error  string `json:"error,omitempty"`
}

func CacheOperation(s string) bool {
	switch s {
	case "submit", "validate", "status", "read", "release":
		return true
	}
	return false
}

type WorkshopRequest struct {
	Digest    string         `json:"digest,omitempty"`
	Operation string         `json:"operation"`
	RequestID string         `json:"request_id,omitempty"`
	Handle    string         `json:"handle,omitempty"`
	ItemID    string         `json:"item_id,omitempty"`
	Revision  string         `json:"revision,omitempty"`
	Token     string         `json:"token,omitempty"`
	File      string         `json:"file,omitempty"`
	Offset    int64          `json:"offset,omitempty"`
	Data      []byte         `json:"data,omitempty"`
	Files     []WorkshopFile `json:"files,omitempty"`
}
type WorkshopFile struct {
	Name   string `json:"name"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}
type WorkshopGeneration struct {
	AppID    int            `json:"app_id"`
	ItemID   string         `json:"item_id"`
	Revision string         `json:"revision"`
	Importer string         `json:"importer"`
	Digest   string         `json:"digest"`
	Updated  int64          `json:"updated"`
	Files    []WorkshopFile `json:"files"`
}
type WorkshopLink struct {
	Name   string `json:"name"`
	Device uint64 `json:"device"`
	Inode  uint64 `json:"inode"`
	UID    uint32 `json:"uid"`
	Mode   uint32 `json:"mode"`
}
type WorkshopLinkInfo struct {
	AppID    int            `json:"app_id"`
	ItemID   string         `json:"item_id"`
	Revision string         `json:"revision"`
	Digest   string         `json:"digest"`
	Files    []WorkshopLink `json:"files"`
}
type WorkshopResult struct {
	Links      *WorkshopLinkInfo   `json:"links,omitempty"`
	State      string              `json:"state"`
	Handle     string              `json:"handle,omitempty"`
	Token      string              `json:"token,omitempty"`
	Owner      string              `json:"owner,omitempty"`
	Revision   string              `json:"revision,omitempty"`
	Generation *WorkshopGeneration `json:"generation,omitempty"`
	Data       []byte              `json:"data,omitempty"`
	Error      string              `json:"error,omitempty"`
}

func WorkshopOperation(s string) bool {
	switch s {
	case "select", "begin", "import-chunk", "seal", "status", "reserve", "read", "commit", "finish", "link-info":
		return true
	}
	return false
}

type UpdateControl struct {
	ActionID  string    `json:"action_id,omitempty"`
	RunID     string    `json:"run_id,omitempty"`
	Target    string    `json:"target,omitempty"`
	ExpiresAt time.Time `json:"expires_at,omitempty"`
}
type ControlRequest struct {
	RunID string `json:"run_id"`
}
type ExecuteUpdate struct {
	JobID string `json:"job_id"`
}
type UpdateAdmin struct {
	Operation  string `json:"operation"`
	JobID      string `json:"job_id,omitempty"`
	AllowStart bool   `json:"allow_start,omitempty"`
}
type RestartRequest struct {
	Policy          string `json:"policy"`
	MaxDelaySeconds int    `json:"max_delay_seconds"`
	WindowStart     string `json:"window_start,omitempty"`
	WindowEnd       string `json:"window_end,omitempty"`
}
type RestartResult struct {
	Generation string `json:"generation,omitempty"`
	BuildID    string `json:"build_id,omitempty"`
	State      string `json:"state,omitempty"`
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

type OperationsRequest struct {
	Operation  string `json:"operation"`
	ServerID   string `json:"server_id,omitempty"`
	RunID      string `json:"run_id,omitempty"`
	ID         string `json:"id,omitempty"`
	RequestID  string `json:"request_id,omitempty"`
	Namespace  string `json:"namespace,omitempty"`
	Private    bool   `json:"private,omitempty"`
	Offset     int64  `json:"offset,omitempty"`
	Limit      int    `json:"limit,omitempty"`
	Retain     int    `json:"retain,omitempty"`
	AgeSeconds int64  `json:"age_seconds,omitempty"`
}
