// SPDX-License-Identifier: GPL-3.0-or-later
// Package schematest is used only by development tests with an explicitly
// provisioned Python JSON Schema validator. Product binaries do not import it.
package schematest

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func Require(t *testing.T) {
	t.Helper()
	if os.Getenv("S2_SCHEMA_PYTHON") == "" {
		t.Skip("run make schema-check with an isolated JSON Schema validator")
	}
}
func Validate(t *testing.T, schema string, v any) {
	t.Helper()
	Require(t)
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Getenv("S2_SCHEMA_PYTHON"), filepath.Join("..", "..", "tests", "validate_schema.py"), filepath.Join("..", "..", "schemas", schema))
	cmd.Stdin = bytes.NewReader(b)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("schema validation: %s: %v", out, err)
	}
}
