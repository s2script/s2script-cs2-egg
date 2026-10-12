// SPDX-License-Identifier: GPL-3.0-or-later
package alerts

import (
	"encoding/json"
	"github.com/s2script/s2script-cs2-egg/internal/events"
	"strings"
	"testing"
)

func TestBackupNotificationNamesAffectedServer(t *testing.T) {
	d := &Dispatcher{}
	b := d.payload(events.Event{Origin: "host", Type: "backup", ServerID: "cs2-one", NodeID: "node-a", Producer: "backup"})
	var p struct{ Content string }
	if json.Unmarshal(b, &p) != nil || !strings.Contains(p.Content, "Server: cs2-one") {
		t.Fatalf("operator cannot identify backed-up server: %s", b)
	}
}
