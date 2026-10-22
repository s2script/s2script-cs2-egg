// SPDX-License-Identifier: GPL-3.0-or-later

package nodelink_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/K4ryuu/CS2-Egg-Go/internal/proto"
)

func TestConnectionTraceWorksWithoutEggGuardAndBeforeConsoleFilter(t *testing.T) {
	for _, enabled := range []string{"0", "1"} {
		t.Run(enabled, func(t *testing.T) {
			root := volume(t)
			node := listen(t, root, []string{"guard", "consolelog"})
			cfg := filepath.Join(root, "egg", "configs")
			if err := os.MkdirAll(cfg, 0755); err != nil {
				t.Fatal(err)
			}
			// The trace must see evidence even when the panel filter hides it.
			filter := `{"patterns":["Receiving C2S_CONNECT","CServerSideClientBase","Client "]}`
			if err := os.WriteFile(filepath.Join(cfg, "console-filter.json"), []byte(filter), 0600); err != nil {
				t.Fatal(err)
			}
			fixture, err := os.ReadFile("../../unit/egg/conntrace/testdata/retry.log")
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(root, "fixture.log"), fixture, 0600); err != nil {
				t.Fatal(err)
			}
			runEgg(t, root, "cat fixture.log", map[string]string{"ENABLE_GUARD": "0", "ENABLE_FILTER": "1", "ENABLE_LOG_FILES": "1", "ENABLE_CONNECTION_TRACE": enabled, "CONNECTION_TRACE_IP": "203.0.113.7"})
			// Logs drain asynchronously; server.state may arrive first. This
			// FIFO sink marker follows all records flushed at shutdown.
			node.await(t, "log flush", func(m proto.Message) bool {
				l, ok := m.(*proto.LogLine)
				return ok && l.Msg == "Server stopped"
			})
			node.await(t, "server stopped", func(m proto.Message) bool { s, ok := m.(*proto.ServerState); return ok && !s.Up })
			node.mu.Lock()
			defer node.mu.Unlock()
			var query, trace, joined bool
			for _, msg := range node.msgs {
				switch m := msg.(type) {
				case *proto.ConnectionTraceQuery:
					query = true
				case *proto.GuardHit, *proto.GuardEvent:
					t.Fatal("diagnostics entered enforcement path")
				case *proto.LogLine:
					if strings.HasPrefix(m.Msg, "[connection-trace] ") {
						trace = true
						var event struct{ Stage string }
						if err := json.Unmarshal([]byte(strings.TrimPrefix(m.Msg, "[connection-trace] ")), &event); err != nil {
							t.Fatal(err)
						}
						joined = joined || event.Stage == "joined"
					}
				}
			}
			if enabled == "1" && (!query || !trace || !joined) {
				t.Fatalf("missing trace: query=%v trace=%v joined=%v", query, trace, joined)
			}
			if enabled == "0" && (query || trace) {
				t.Fatal("disabled tracing produced output or traffic")
			}
		})
	}
}
