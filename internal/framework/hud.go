// SPDX-License-Identifier: GPL-3.0-or-later
package framework

import (
	"bytes"
	"errors"
	"regexp"
	"strings"
)

const HUDAddon = "3790153369"

// MergeHUDConfig preserves unrelated text/comments and appends the HUD addon to
// an existing comma-separated list. Ambiguous multiple assignments fail closed.
func MergeHUDConfig(data []byte) ([]byte, error) {
	if len(data) > 1<<20 {
		return nil, errors.New("HUD configuration is oversized")
	}
	re := regexp.MustCompile(`(?m)^[\t ]*mm_extra_addons[\t ]+"([^"\r\n]*)"[\t ]*(?://[^\r\n]*)?$`)
	matches := re.FindAllSubmatchIndex(data, -1)
	if len(matches) > 1 {
		return nil, errors.New("multiple mm_extra_addons assignments")
	}
	// Refuse unrecognized active assignment syntax rather than override it.
	assignments := regexp.MustCompile(`(?m)^[\t ]*mm_extra_addons\b`).FindAllIndex(data, -1)
	if len(assignments) != len(matches) {
		return nil, errors.New("unsupported mm_extra_addons syntax")
	}
	if len(matches) == 0 {
		b := bytes.Clone(data)
		if len(b) > 0 && b[len(b)-1] != '\n' {
			b = append(b, '\n')
		}
		return append(b, []byte("mm_extra_addons \""+HUDAddon+"\"\n")...), nil
	}
	m := matches[0]
	old := string(data[m[2]:m[3]])
	for _, id := range strings.Split(old, ",") {
		if strings.TrimSpace(id) == HUDAddon {
			return bytes.Clone(data), nil
		}
	}
	value := old
	if strings.TrimSpace(value) != "" {
		value += ","
	}
	value += HUDAddon
	out := bytes.Clone(data[:m[2]])
	out = append(out, []byte(value)...)
	out = append(out, data[m[3]:]...)
	return out, nil
}
