// SPDX-License-Identifier: GPL-3.0-or-later
package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestVersionDoesNotLoadSecretsOrServer(t *testing.T) {
	var out, errs bytes.Buffer
	if c := run([]string{"version"}, func(string) (string, bool) { t.Fatal("loaded environment"); return "", false }, &out, &errs); c != 0 || !strings.Contains(out.String(), "dev") {
		t.Fatal(c, out.String(), errs.String())
	}
}
