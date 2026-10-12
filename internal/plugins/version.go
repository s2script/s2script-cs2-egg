// SPDX-License-Identifier: GPL-3.0-or-later
package plugins

import (
	"errors"
	"regexp"
	"strings"
)

var identityPattern = regexp.MustCompile(`^(?:@[a-zA-Z0-9][a-zA-Z0-9._-]*/)?[a-zA-Z0-9][a-zA-Z0-9._-]*$`)
var exactPattern = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(?:-([0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*))?(?:\+[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?$`)

func validName(s string) bool { return len(s) <= 214 && identityPattern.MatchString(s) }

// ExactVersion validates SemVer without coercing partial versions or changing pins.
func ExactVersion(v string) bool {
	m := exactPattern.FindStringSubmatch(v)
	if m == nil || len(v) > 128 {
		return false
	}
	if m[4] != "" {
		for _, p := range strings.Split(m[4], ".") {
			if len(p) > 1 && p[0] == '0' && strings.Trim(p, "0123456789") == "" {
				return false
			}
		}
	}
	return true
}

// NormalizeVersion validates policy syntax only; registry reconciliation decides
// range satisfaction. Omission means wildcard, while exact pins remain verbatim.
func NormalizeVersion(v string) (string, error) {
	if v == "" {
		return "*", nil
	}
	if v == "*" || ExactVersion(v) {
		return v, nil
	}
	if len(v) > 256 || strings.TrimSpace(v) != v || strings.ContainsAny(v, "\r\n\t\x00") {
		return "", errors.New("unsafe or unsupported version range")
	}
	for _, group := range strings.Split(v, "||") {
		tokens := strings.Fields(group)
		if len(tokens) == 0 {
			return "", errors.New("empty version range")
		}
		for _, token := range tokens {
			base := strings.TrimLeft(token, "<>=^~")
			prefix := strings.TrimSuffix(token, base)
			if prefix == "" || !(prefix == "<" || prefix == ">" || prefix == "<=" || prefix == ">=" || prefix == "=" || prefix == "^" || prefix == "~") || !ExactVersion(base) {
				return "", errors.New("version must be *, exact SemVer, or a supported SemVer range")
			}
		}
	}
	return v, nil
}

var apiPattern = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(?:[xX*]|(?:0|[1-9][0-9]*)\.[xX*])$`)

func validAPIVersion(v string) bool {
	if v == "" || len(v) > 128 {
		return false
	}
	if apiPattern.MatchString(v) {
		return true
	}
	_, e := NormalizeVersion(v)
	return e == nil
}

// compareVersion compares arbitrary-length SemVer numbers without integer
// overflow; build metadata has no precedence. Pins are compared verbatim below.
func compareVersion(a, b string) int {
	split := func(v string) ([]string, []string) {
		v = strings.SplitN(v, "+", 2)[0]
		p := strings.SplitN(v, "-", 2)
		var pre []string
		if len(p) > 1 {
			pre = strings.Split(p[1], ".")
		}
		return strings.Split(p[0], "."), pre
	}
	an, ap := split(a)
	bn, bp := split(b)
	number := func(x, y string) int {
		if len(x) < len(y) {
			return -1
		}
		if len(x) > len(y) {
			return 1
		}
		return strings.Compare(x, y)
	}
	for i := 0; i < 3; i++ {
		if c := number(an[i], bn[i]); c != 0 {
			return c
		}
	}
	if len(ap) == 0 && len(bp) > 0 {
		return 1
	}
	if len(bp) == 0 && len(ap) > 0 {
		return -1
	}
	for i := 0; i < len(ap) && i < len(bp); i++ {
		x, y := ap[i], bp[i]
		xn, yn := strings.Trim(x, "0123456789") == "", strings.Trim(y, "0123456789") == ""
		c := strings.Compare(x, y)
		if xn && yn {
			c = number(x, y)
		} else if xn {
			c = -1
		} else if yn {
			c = 1
		}
		if c != 0 {
			return c
		}
	}
	if len(ap) < len(bp) {
		return -1
	}
	if len(ap) > len(bp) {
		return 1
	}
	return 0
}
func satisfies(v, selector string) bool {
	if !ExactVersion(v) {
		return false
	}
	selector, e := NormalizeVersion(selector)
	if e != nil {
		return false
	}
	if ExactVersion(selector) {
		return v == selector
	}
	core := strings.SplitN(strings.SplitN(v, "+", 2)[0], "-", 2)[0]
	pre := strings.Contains(strings.SplitN(v, "+", 2)[0], "-")
	if selector == "*" {
		return !pre
	}
	for _, group := range strings.Split(selector, "||") {
		ok := true
		allowsPre := !pre
		for _, token := range strings.Fields(group) {
			base := strings.TrimLeft(token, "<>=^~")
			op := strings.TrimSuffix(token, base)
			c := compareVersion(v, base)
			bc := strings.SplitN(strings.SplitN(base, "+", 2)[0], "-", 2)[0]
			if strings.Contains(strings.SplitN(base, "+", 2)[0], "-") && bc == core {
				allowsPre = true
			}
			switch op {
			case "=":
				ok = ok && c == 0
			case ">":
				ok = ok && c > 0
			case ">=":
				ok = ok && c >= 0
			case "<":
				ok = ok && c < 0
			case "<=":
				ok = ok && c <= 0
			case "^", "~":
				vp, bp := strings.Split(core, "."), strings.Split(bc, ".")
				same := vp[0] == bp[0]
				if op == "~" || bp[0] == "0" {
					same = same && vp[1] == bp[1]
				}
				if op == "^" && bp[0] == "0" && bp[1] == "0" {
					same = same && vp[2] == bp[2]
				}
				ok = ok && c >= 0 && same
			default:
				ok = false
			}
		}
		if ok && allowsPre {
			return true
		}
	}
	return false
}
