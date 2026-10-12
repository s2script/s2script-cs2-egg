// SPDX-License-Identifier: GPL-3.0-or-later
package observability

import (
	"bytes"
	"github.com/s2script/s2script-cs2-egg/internal/server"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSecretSplitAcrossChunksRedacted(t *testing.T) {
	var b bytes.Buffer
	w, e := NewStreamRedactor(&b, []string{"abc", "abcdef"})
	if e != nil {
		t.Fatal(e)
	}
	for _, s := range []string{"before a", "bc", "def after"} {
		if _, e = w.Write([]byte(s)); e != nil {
			t.Fatal(e)
		}
	}
	if e = w.Close(); e != nil {
		t.Fatal(e)
	}
	if b.String() != "before [REDACTED] after" {
		t.Fatal(b.String())
	}
}
func TestLogsRotateWithinBound(t *testing.T) {
	root := t.TempDir()
	l, e := NewLogs(root, LogOptions{FileBytes: 128, Files: 3, TailBytes: 64}, []string{"secret"})
	if e != nil {
		t.Fatal(e)
	}
	for range 100 {
		if e = l.Append("a", []byte("message secret repeated here\n")); e != nil {
			t.Fatal(e)
		}
	}
	if e = l.Close(); e != nil {
		t.Fatal(e)
	}
	entries, e := os.ReadDir(filepath.Join(root, "a", "logs"))
	if e != nil {
		t.Fatal(e)
	}
	n := 0
	for _, f := range entries {
		if strings.HasSuffix(f.Name(), ".log") {
			n++
			b, _ := os.ReadFile(filepath.Join(root, "a", "logs", f.Name()))
			if len(b) > 128 || bytes.Contains(b, []byte("secret")) {
				t.Fatal("unsafe log", f.Name())
			}
		}
	}
	if n > 3 {
		t.Fatal(n)
	}
}
func TestMetricsPrivateBindingAndBoundedLabels(t *testing.T) {
	if ValidateMetricsAddress("0.0.0.0:9100") == nil {
		t.Fatal("public bind accepted")
	}
	if ValidateMetricsAddress("localhost:9100") == nil {
		t.Fatal("DNS bind accepted")
	}
	if e := ValidateMetricsAddress("127.0.0.1:9100"); e != nil {
		t.Fatal(e)
	}
	r := Report{Servers: map[string]server.Status{"a": {State: "ready", Map: "SECRET", RunID: "unbounded", Players: server.Players{}}}}
	var b bytes.Buffer
	WriteMetrics(&b, r)
	if strings.Contains(b.String(), "SECRET") || strings.Contains(b.String(), "unbounded") || strings.Contains(b.String(), "s2node_players{") {
		t.Fatal(b.String())
	}
}
