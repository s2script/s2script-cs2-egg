// SPDX-License-Identifier: GPL-3.0-or-later
package processgroup

import "testing"

func TestKillAndReapRejectsNonpositiveGroup(t *testing.T) {
	for _, pgid := range []int{0, -1, -123} {
		if e := KillAndReap(pgid); e == nil {
			t.Fatalf("accepted unsafe process group %d", pgid)
		}
	}
}
