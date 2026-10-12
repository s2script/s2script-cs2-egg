// SPDX-License-Identifier: GPL-3.0-or-later
package server

import (
	"github.com/s2script/s2script-cs2-egg/internal/config"
	"testing"
)

func TestOverlappingKnownSecretsDoNotLeakSuffix(t *testing.T) {
	r := redactFor(config.Server{Steam: config.Steam{GSLT: "abc"}, Webhook: config.Webhook{URL: "https://discord.com/api/webhooks/1/abcdef"}})
	if got := r("abcdef"); got != "[REDACTED]" {
		t.Fatalf("longer known secret suffix leaked: %q", got)
	}
}
