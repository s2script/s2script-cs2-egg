// SPDX-License-Identifier: GPL-3.0-or-later
package update_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/K4ryuu/CS2-Egg-Go/internal/egg/addons"
	"github.com/K4ryuu/CS2-Egg-Go/internal/node/core"
	"github.com/K4ryuu/CS2-Egg-Go/internal/node/nodeconfig"
	"github.com/K4ryuu/CS2-Egg-Go/internal/node/update"
)

func TestForkUpdatesDoNotReplaceS2ScriptWithUpstreamBinary(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/s2script/s2script-cs2-egg/releases/latest" {
			http.NotFound(w, r)
			return
		}
		w.Write([]byte(`{"tag_name":"v9.0.0","assets":[{"name":"cs2node_linux_amd64","browser_download_url":"https://github.com/s2script/s2script-cs2-egg/releases/download/v9.0.0/cs2node_linux_amd64"},{"name":"checksums.txt","browser_download_url":"https://github.com/s2script/s2script-cs2-egg/releases/download/v9.0.0/checksums.txt"}]}`))
	}))
	defer server.Close()
	previous := addons.APIBase
	addons.APIBase = server.URL
	defer func() { addons.APIBase = previous }()
	candidate, err := update.Check(context.Background(), server.Client(), "stable", "0.1.0")
	if err != nil || candidate.Version != "9.0.0" {
		t.Fatalf("fork release lookup: %+v %v", candidate, err)
	}
}

func TestForkDiscoversOwnServersWithoutManagingUpstreamServers(t *testing.T) {
	images := nodeconfig.Default().Images
	if !core.ImageMatches("ghcr.io/s2script/s2script-cs2-egg:beta", images) {
		t.Fatal("fork image is not discovered")
	}
	if core.ImageMatches("ghcr.io/k4ryuu/cs2-egg-go:stable", images) {
		t.Fatal("fork must not manage upstream servers")
	}
}

func TestOnlySupportedNativeAddonsCanBePinned(t *testing.T) {
	for repo, want := range map[string]string{"s2script/s2script": "s2script", "Source2ZE/MultiAddonManager": "multiaddonmanager", "SlynxCZ/GameFixes_mm": "gamefixes"} {
		if got := addons.KeyForRepo(repo); got != want {
			t.Errorf("pin lookup %s: %q", repo, got)
		}
	}
	for _, repo := range []string{"roflmuffin/CounterStrikeSharp", "swiftly-solution/swiftlys2", "Kxnrl/modsharp-public"} {
		if addons.KeyForRepo(repo) != "" {
			t.Errorf("removed framework remains pinnable: %s", repo)
		}
	}
}
