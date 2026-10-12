// SPDX-License-Identifier: GPL-3.0-or-later
package alerts

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

func TestDispatcherFormattingRedactsAllVerbs(t *testing.T) {
	d, e := NewDiscord(Config{URL: "https://discord.com/api/webhooks/123/TASK8_SECRET"}, nil)
	if e != nil {
		t.Fatal(e)
	}
	defer d.Close(context.Background())
	for _, verb := range []string{"%v", "%+v", "%#v"} {
		s := fmt.Sprintf(verb, d)
		if strings.Contains(s, "TASK8_SECRET") || strings.Contains(s, "discord.com") {
			t.Fatalf("dispatcher format leaked secret with %s", verb)
		}
	}
}
