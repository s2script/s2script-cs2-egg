// SPDX-License-Identifier: GPL-3.0-or-later
// Package alerts delivers selected lifecycle facts through bounded, best-effort queues.
package alerts

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/s2script/s2script-cs2-egg/internal/events"
)

type Config struct {
	URL            string
	Events         []string
	QueueCapacity  int
	AttemptTimeout time.Duration
	MaxRetries     int
}

func (c Config) MarshalJSON() ([]byte, error) {
	type plain Config
	if c.URL != "" {
		c.URL = "[REDACTED]"
	}
	return json.Marshal(plain(c))
}
func (c Config) Format(f fmt.State, verb rune) { b, _ := c.MarshalJSON(); _, _ = f.Write(b) }

type Stats struct{ Delivered, Failed, Dropped, Duplicates uint64 }
type Dispatcher struct {
	cfg                                    Config
	client                                 *http.Client
	selected                               map[string]bool
	queue                                  chan events.Event
	ctx                                    context.Context
	cancel                                 context.CancelFunc
	done                                   chan struct{}
	mu                                     sync.Mutex
	closed                                 bool
	seen                                   map[string]bool
	recent                                 []string
	delivered, failed, dropped, duplicates atomic.Uint64
	dead                                   atomic.Bool
}

var _ events.Sink = (*Dispatcher)(nil)

// Publish never waits for network or queue capacity. IDs are remembered while
// pending and for the last 1024 completed events; overflow may be retried later.
func (d *Dispatcher) Publish(ctx context.Context, e events.Event) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if d.cfg.URL == "" || !d.selected[e.Type] {
		return nil
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed {
		return errors.New("webhook dispatcher is closed")
	}
	if e.ID == "" {
		return errors.New("webhook event ID is required")
	}
	if d.seen[e.ID] {
		d.duplicates.Add(1)
		return nil
	}
	if d.dead.Load() {
		d.failed.Add(1)
		return errors.New("webhook endpoint is disabled")
	}
	select {
	case d.queue <- cloneEvent(e):
		d.seen[e.ID] = true
		return nil
	default:
		d.dropped.Add(1)
		return errors.New("webhook queue is full")
	}
}
func cloneEvent(e events.Event) events.Event {
	details := make(map[string]string, len(e.Details))
	for k, v := range e.Details {
		details[k] = v
	}
	e.Details = details
	return e
}
func (d *Dispatcher) Stats() Stats {
	return Stats{d.delivered.Load(), d.failed.Load(), d.dropped.Load(), d.duplicates.Load()}
}

// Close rejects new events, drains pending delivery and cancels on the caller's
// absolute deadline. The HTTP transport must obey request context cancellation.
func (d *Dispatcher) Close(ctx context.Context) error {
	d.mu.Lock()
	if !d.closed {
		d.closed = true
		close(d.queue)
	}
	d.mu.Unlock()
	select {
	case <-d.done:
		return nil
	case <-ctx.Done():
		d.cancel()
		return ctx.Err()
	}
}
func (d *Dispatcher) worker() {
	defer close(d.done)
	defer d.cancel()
	for e := range d.queue {
		if d.ctx.Err() != nil {
			d.dropped.Add(1)
		} else if d.dead.Load() || !d.deliver(e) {
			d.failed.Add(1)
		} else {
			d.delivered.Add(1)
		}
		d.mu.Lock()
		d.recent = append(d.recent, e.ID)
		if len(d.recent) > 1024 {
			delete(d.seen, d.recent[0])
			d.recent = d.recent[1:]
		}
		d.mu.Unlock()
	}
}

// DeliveryResult describes an actual synchronous transport outcome. Publish's
// nil return is deliberately NOT this contract.
type DeliveryResult string

const (
	Delivered DeliveryResult = "delivered"
	Filtered  DeliveryResult = "filtered"
	Failed    DeliveryResult = "failed"
)

type AcknowledgedDelivery interface {
	Enabled() bool
	Accepts(events.Event) bool
	Deliver(context.Context, events.Event) (DeliveryResult, error)
}

func (d *Dispatcher) Format(f fmt.State, verb rune) {
	_, _ = fmt.Fprintf(f, "Dispatcher{stats:%+v}", d.Stats())
}
func (d *Dispatcher) Enabled() bool               { return d.cfg.URL != "" }
func (d *Dispatcher) Accepts(e events.Event) bool { return d.cfg.URL != "" && d.selected[e.Type] }

// Deliver bypasses the asynchronous queue and its in-memory deduplication. The
// durable caller owns admission, retries and IDs. Failure includes uncertain
// remote outcomes; retries can duplicate a message whose receipt was lost.
func (d *Dispatcher) Deliver(ctx context.Context, e events.Event) (DeliveryResult, error) {
	if err := ctx.Err(); err != nil {
		return Failed, err
	}
	if !d.Accepts(e) {
		return Filtered, nil
	}
	if d.dead.Load() {
		return Failed, errors.New("webhook endpoint is disabled")
	}
	if !d.deliverContext(ctx, e) {
		return Failed, errors.New("webhook delivery was not acknowledged")
	}
	return Delivered, nil
}
