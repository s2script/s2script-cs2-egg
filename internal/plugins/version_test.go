// SPDX-License-Identifier: GPL-3.0-or-later
package plugins

import "testing"

func TestVersionSatisfaction(t *testing.T) {
	for _, tc := range []struct {
		version, selector string
		want              bool
	}{{"1.2.3", "^1.0.0", true}, {"2.0.0", "^1.0.0", false}, {"0.3.0", "^0.2.3", false}, {"0.2.9", "^0.2.3", true}, {"0.0.4", "^0.0.3", false}, {"1.3.0", "~1.2.3", false}, {"1.2.99", "~1.2.3", true}, {"1.2.3-beta.2", "*", false}, {"1.2.3-beta.2", ">=1.2.3-beta.1 <1.2.3", true}, {"1.2.4-beta.1", ">=1.2.3-beta.1", false}, {"1.2.3+new", "1.2.3+old", false}, {"1.2.3+new", "=1.2.3", true}, {"3.0.0", ">=1.0.0 <2.0.0 || >=3.0.0", true}, {"999999999999999999999999.1.0", ">999999999999999999999998.1.0", true}, {"1.2.3-beta.10", ">1.2.3-beta.2", true}, {"1.2.3-beta.2", ">1.2.3-beta.10", false}} {
		if got := satisfies(tc.version, tc.selector); got != tc.want {
			t.Errorf("%s in %s=%v want %v", tc.version, tc.selector, got, tc.want)
		}
	}
}
