// SPDX-License-Identifier: GPL-3.0-or-later
package shared

import (
	"context"
	"encoding/json"
	node "github.com/s2script/s2script-cs2-egg/internal/nodetypes"
	"github.com/s2script/s2script-cs2-egg/internal/protocol"
	"testing"
)

func TestPrepareReservationIdempotentAcrossRestart(t *testing.T) {
	s, _ := testStore(t)
	if _, e := s.Build(context.Background()); e != nil {
		t.Fatal(e)
	}
	request := protocol.PrepareRequest{AppID: 730, Operation: "reserve", RunID: "run", RequestID: "same"}
	b, _ := json.Marshal(request)
	module := Module{Store: s}
	a, e := module.Handle(context.Background(), node.Server{ID: "one"}, protocol.CapPrepare, protocol.Message{Payload: b})
	if e != nil {
		t.Fatal(e)
	}
	restarted, e := New(s.options)
	if e != nil {
		t.Fatal(e)
	}
	module.Store = restarted
	v, e := module.Handle(context.Background(), node.Server{ID: "one"}, protocol.CapPrepare, protocol.Message{Payload: b})
	if e != nil {
		t.Fatal(e)
	}
	if a.(protocol.Preparation).Reservation != v.(protocol.Preparation).Reservation {
		t.Fatal("retry allocated a new reservation")
	}
	request.Operation = "commit"
	request.Reservation = a.(protocol.Preparation).Reservation
	b, _ = json.Marshal(request)
	if _, e = module.Handle(context.Background(), node.Server{ID: "other"}, protocol.CapPrepare, protocol.Message{Payload: b}); e == nil {
		t.Fatal("another server committed reference")
	}
}
