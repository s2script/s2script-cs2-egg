// SPDX-License-Identifier: GPL-3.0-or-later
package node

import (
	"context"
	"github.com/s2script/s2script-cs2-egg/internal/events"
	"github.com/s2script/s2script-cs2-egg/internal/schematest"
	"testing"
	"time"
)

func TestHostEventMatchesPublishedSchema(t *testing.T) {
	schematest.Require(t)
	c := newDiskCore(t, &operationalSink{})
	_, err := c.publishOperational(context.Background(), "selfupdate", events.OperationalEvent{ActionID: "trial", EventKey: "accepted", Type: "node.update", Severity: "info", At: time.Now().UTC()})
	if err != nil {
		t.Fatal(err)
	}
	if len(c.state.Records) != 1 {
		t.Fatal("missing emitted host event")
	}
	for _, r := range c.state.Records {
		schematest.Validate(t, "events.schema.json", r.Event)
	}
}
