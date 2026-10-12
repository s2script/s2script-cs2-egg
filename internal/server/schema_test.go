// SPDX-License-Identifier: GPL-3.0-or-later
package server

import (
	"context"
	"fmt"
	"github.com/s2script/s2script-cs2-egg/internal/events"
	"github.com/s2script/s2script-cs2-egg/internal/schematest"
	"io"
	"testing"
	"time"
)

func TestLifecycleEventsMatchPublishedSchema(t *testing.T) {
	schematest.Require(t)
	cfg, o := fixtureConfig(t, "ready")
	o.timeout = 5 * time.Second
	r, w := io.Pipe()
	defer r.Close()
	defer w.Close()
	sink := &recordSink{make(chan events.Event, 128)}
	done := make(chan int, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { code, _ := runWith(ctx, cfg, sink, r, io.Discard, io.Discard, o); done <- code }()
	up := nextEvent(t, sink, "server.up")
	fmt.Fprintln(w, "quit")
	w.Close()
	down := nextEvent(t, sink, "server.down")
	if code := <-done; code != 0 {
		t.Fatal(code)
	}
	schematest.Validate(t, "events.schema.json", up)
	schematest.Validate(t, "events.schema.json", down)
}
