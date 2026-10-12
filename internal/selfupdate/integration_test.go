// SPDX-License-Identifier: GPL-3.0-or-later
package selfupdate

import (
	"encoding/json"
	"github.com/s2script/s2script-cs2-egg/internal/protocol"
	"github.com/s2script/s2script-cs2-egg/internal/schematest"
	"strings"
	"testing"
)

func TestBotInterfaceHasNoArbitraryShellAction(t *testing.T) {
	for _, b := range []string{`{"operation":"stage","url":"https://evil.example/a"}`, `{"operation":"activate","command":"sh"}`, `{"operation":"activate","executable":"/tmp/a"}`, `{"operation":"activate","service":"ssh"}`} {
		var r Request
		if protocol.Decode([]byte(b), &r) == nil {
			t.Fatal("untyped action input accepted", b)
		}
	}
	b, _ := json.Marshal(Request{Operation: "check", RequestID: strings.Repeat("x", 32)})
	schematest.Validate(t, "actions.schema.json", map[string]any{"type": protocol.CapProjectUpdate, "payload": json.RawMessage(b)})
}
