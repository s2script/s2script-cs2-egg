// SPDX-License-Identifier: GPL-3.0-or-later
package selfupdate

import (
	"context"
	"fmt"
	"github.com/s2script/s2script-cs2-egg/internal/buildinfo"
	"os"
	"strings"
	"testing"
	"time"
)

func TestMain(m *testing.M) {
	if os.Getenv("S2_UPDATE_TEST_CHILD") == "1" {
		buildinfo.Version = "1.2.3"
		buildinfo.Revision = strings.Repeat("a", 40)
		if err := ReportReady(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(91)
		}
		time.Sleep(time.Minute)
		os.Exit(0)
	}
	os.Exit(m.Run())
}
func TestPrivateReadinessChannelAcrossRealExec(t *testing.T) {
	t.Setenv("S2_UPDATE_TEST_CHILD", "1")
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	tx := strings.Repeat("b", 64)
	child, err := startChild(context.Background(), exe, "/unused-test-policy", tx)
	if err != nil {
		t.Fatal(err)
	}
	defer child.Stop()
	select {
	case r, ok := <-child.Ready:
		if !ok || r.Transaction != tx || r.Version != "1.2.3" {
			t.Fatal("private pipe readiness failed", r, ok)
		}
	case code := <-child.Exit:
		t.Fatal("readiness child failed", code)
	case <-time.After(5 * time.Second):
		t.Fatal("readiness timeout")
	}
}
