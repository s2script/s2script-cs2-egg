// SPDX-License-Identifier: GPL-3.0-or-later
package crashes

import "github.com/s2script/s2script-cs2-egg/internal/events"

func ShouldCapture(e events.Event) bool {
	return e.Type == "server.crash" && e.Reason == "unexpected_exit"
}
