// SPDX-License-Identifier: GPL-3.0-or-later
package scheduler

import "time"

// Clock keeps scheduling decisions independent of wall-clock timers in tests.
type Clock interface{ Now() time.Time }
type RealClock struct{}

func (RealClock) Now() time.Time { return time.Now().UTC() }
