// SPDX-License-Identifier: GPL-3.0-or-later
package server

import (
	"context"
	"fmt"
	"io"
	"sync/atomic"
	"time"

	"github.com/s2script/s2script-cs2-egg/internal/events"
)

// One worker decouples raw console/status events from process I/O. Lifecycle
// capacity is reserved for the at-most-four terminal/up events of one Run.
// All publishers must stop before finish; Run's cleanup ordering guarantees it.
type eventQueue struct {
	sink                         events.Sink
	log                          io.Writer
	raw, lifecycle               chan events.Event
	ctx                          context.Context
	cancel                       context.CancelFunc
	done                         chan struct{}
	publishTimeout, drainTimeout time.Duration
	dropped, failed              atomic.Uint64
}

func newEventQueue(sink events.Sink, log io.Writer, publishTimeout, drainTimeout time.Duration) *eventQueue {
	q := &eventQueue{sink: sink, log: log, publishTimeout: publishTimeout, drainTimeout: drainTimeout}
	if sink == nil {
		return q
	}
	q.raw = make(chan events.Event, 128)
	q.lifecycle = make(chan events.Event, 16)
	q.done = make(chan struct{})
	q.ctx, q.cancel = context.WithCancel(context.Background())
	go q.work()
	return q
}
func (q *eventQueue) publish(e events.Event) {
	if q.sink == nil {
		return
	}
	target := q.raw
	switch e.Type {
	case "server.up", "server.down", "server.crash", "server.start_failed":
		target = q.lifecycle
	}
	select {
	case target <- e:
	default:
		q.dropped.Add(1)
	}
}
func (q *eventQueue) work() {
	defer close(q.done)
	raw, lifecycle := q.raw, q.lifecycle
	handoff := func(e events.Event) {
		ctx, cancel := context.WithTimeout(q.ctx, q.publishTimeout)
		err := q.sink.Publish(ctx, e)
		cancel()
		if err != nil {
			q.failed.Add(1)
		}
	}
	for raw != nil || lifecycle != nil {
		if q.ctx.Err() != nil {
			q.dropped.Add(uint64(len(raw) + len(lifecycle)))
			return
		}
		// Lifecycle events cannot sit behind a full console backlog.
		select {
		case e, ok := <-lifecycle:
			if !ok {
				lifecycle = nil
			} else {
				handoff(e)
			}
			continue
		default:
		}
		select {
		case <-q.ctx.Done():
			q.dropped.Add(uint64(len(raw) + len(lifecycle)))
			return
		case e, ok := <-lifecycle:
			if !ok {
				lifecycle = nil
			} else {
				handoff(e)
			}
		case e, ok := <-raw:
			if !ok {
				raw = nil
			} else {
				handoff(e)
			}
		}
	}
}
func (q *eventQueue) finish() {
	if q.sink == nil {
		return
	}
	close(q.raw)
	close(q.lifecycle)
	// One absolute budget, not a fresh drain timeout for every queued event.
	timer := time.AfterFunc(q.drainTimeout, q.cancel)
	<-q.done
	timer.Stop()
	q.cancel()
	if (q.dropped.Load() > 0 || q.failed.Load() > 0) && q.log != nil {
		fmt.Fprintf(q.log, "runtime event delivery: dropped=%d failed=%d\n", q.dropped.Load(), q.failed.Load())
	}
}
