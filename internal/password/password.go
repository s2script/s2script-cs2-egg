// SPDX-License-Identifier: GPL-3.0-or-later

// Package password validates generated join passwords at trust boundaries.
package password

import "regexp"

var phrase = regexp.MustCompile(`^[a-z]{2,16}-[a-z]{2,16}-[a-z]{2,16}$`)

func Valid(s string) bool { return phrase.MatchString(s) }
