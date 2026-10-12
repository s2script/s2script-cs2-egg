// SPDX-License-Identifier: GPL-3.0-or-later
package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestDoctorJSONIsMachineReadable(t *testing.T) {
	p := configFixture(t)
	var out, errs bytes.Buffer
	if c := run([]string{"doctor", "--json", "--config", p}, &out, &errs); c != 0 {
		t.Fatal(c, errs.String())
	}
	var v struct {
		Schema int    `json:"schema_version"`
		Ready  string `json:"game_readiness"`
		Checks []struct {
			Server string `json:"server"`
			Access bool   `json:"accessible"`
		} `json:"checks"`
	}
	if e := json.Unmarshal(out.Bytes(), &v); e != nil {
		t.Fatal(e, out.String())
	}
	if v.Schema != 1 || v.Ready != "not_assessed" || len(v.Checks) != 3 || v.Checks[0].Access {
		t.Fatal(out.String())
	}
}
func TestNodeVersionDoesNotRequireConfiguration(t *testing.T) {
	var out, errs bytes.Buffer
	if c := run([]string{"version"}, &out, &errs); c != 0 || !strings.Contains(out.String(), "dev") {
		t.Fatal(c, out.String(), errs.String())
	}
}
