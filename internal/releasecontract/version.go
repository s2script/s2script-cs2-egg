// SPDX-License-Identifier: GPL-3.0-or-later
package releasecontract

import (
	"errors"
	"regexp"
	"strings"
)

var versionPattern = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(?:-([0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*))?$`)

type Version struct {
	Core [3]string
	Pre  []string
}

func ParseVersion(s string) (Version, error) {
	var v Version
	m := versionPattern.FindStringSubmatch(s)
	if len(s) > 128 || m == nil {
		return v, errors.New("canonical SemVer without v/build metadata required")
	}
	copy(v.Core[:], m[1:4])
	if m[4] != "" {
		v.Pre = strings.Split(m[4], ".")
		for _, p := range v.Pre {
			if numeric(p) && len(p) > 1 && p[0] == '0' {
				return v, errors.New("numeric prerelease leading zero")
			}
		}
	}
	return v, nil
}
func numeric(s string) bool {
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return s != ""
}
func compareNumeric(a, b string) int {
	if len(a) < len(b) {
		return -1
	}
	if len(a) > len(b) {
		return 1
	}
	return strings.Compare(a, b)
}

// Compare requires validated canonical versions.
func Compare(a, b string) int {
	av, ae := ParseVersion(a)
	bv, be := ParseVersion(b)
	if ae != nil || be != nil {
		panic("Compare requires canonical versions")
	}
	for i := range av.Core {
		if n := compareNumeric(av.Core[i], bv.Core[i]); n != 0 {
			return n
		}
	}
	if len(av.Pre) == 0 && len(bv.Pre) == 0 {
		return 0
	}
	if len(av.Pre) == 0 {
		return 1
	}
	if len(bv.Pre) == 0 {
		return -1
	}
	for i := 0; i < min(len(av.Pre), len(bv.Pre)); i++ {
		x, y := av.Pre[i], bv.Pre[i]
		n := strings.Compare(x, y)
		if numeric(x) && numeric(y) {
			n = compareNumeric(x, y)
		} else if numeric(x) {
			n = -1
		} else if numeric(y) {
			n = 1
		}
		if n != 0 {
			return n
		}
	}
	if len(av.Pre) < len(bv.Pre) {
		return -1
	}
	if len(av.Pre) > len(bv.Pre) {
		return 1
	}
	return 0
}
