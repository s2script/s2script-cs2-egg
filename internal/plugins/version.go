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
