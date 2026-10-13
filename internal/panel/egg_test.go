// SPDX-License-Identifier: GPL-3.0-or-later
package panel_test

import (
	"bytes"
	"encoding/json"
	"github.com/s2script/s2script-cs2-egg/internal/panel"
	"github.com/s2script/s2script-cs2-egg/internal/plugins"
	"strings"
	"testing"
)

// Catches drift between the canonical catalog, effective defaults, and importable controls.
func TestEggMatchesCatalog(t *testing.T) {
	data, err := panel.Generate("s2script-cs2-egg:dev")
	if err != nil {
		t.Fatal(err)
	}
	var egg struct {
		Meta struct {
			Version string `json:"version"`
		} `json:"meta"`
		Startup string `json:"startup"`
		Config  struct {
			Stop string `json:"stop"`
		} `json:"config"`
		Scripts struct {
			Installation struct{ Script, Container, Entrypoint string } `json:"installation"`
		} `json:"scripts"`
		Variables []struct {
			Name, Description string
			EnvVariable       string `json:"env_variable"`
			Default           string `json:"default_value"`
			UserViewable      bool   `json:"user_viewable"`
			UserEditable      bool   `json:"user_editable"`
			Rules             string `json:"rules"`
			FieldType         string `json:"field_type"`
		} `json:"variables"`
	}
	if err := json.Unmarshal(data, &egg); err != nil {
		t.Fatal(err)
	}
	if egg.Meta.Version != "PTDL_v2" || egg.Startup != "s2egg run" || egg.Config.Stop != "quit" || egg.Scripts.Installation.Script == "" || egg.Scripts.Installation.Container == "" || egg.Scripts.Installation.Entrypoint == "" {
		t.Fatalf("invalid panel structure: %+v", egg)
	}
	vars := map[string]string{}
	controls := map[string]string{}
	for _, v := range egg.Variables {
		if _, ok := vars[v.EnvVariable]; ok {
			t.Fatal("duplicate variable")
		}
		vars[v.EnvVariable] = v.Default
		if v.Name == "" || v.Description == "" || v.Rules == "" || v.FieldType != "text" || !v.UserViewable || !v.UserEditable {
			t.Fatalf("incomplete definition: %+v", v)
		}
		if strings.HasPrefix(v.EnvVariable, "S2SCRIPT_PLUGIN_") && v.EnvVariable != "S2SCRIPT_PLUGIN_FILE" {
			controls[v.EnvVariable] = v.Default
			if v.Rules != "required|string|in:default,0,1" {
				t.Fatalf("invalid control rules: %s", v.Rules)
			}
		}
	}
	if vars["SRCDS_APPID"] != "730" || len(controls) != 18 {
		t.Fatalf("appid=%s controls=%d", vars["SRCDS_APPID"], len(controls))
	}
	seen := map[string]bool{}
	enabled, disabled := 0, 0
	for _, p := range plugins.Bundled() {
		if seen[p.Name] {
			t.Fatal("duplicate plugin")
		}
		seen[p.Name] = true
		if !strings.HasPrefix(p.Name, "@s2script/") {
			t.Fatal("noncanonical name")
		}
		if controls[p.Variable] != "default" {
			t.Fatalf("missing inherited control %s", p.Variable)
		}
		if p.DefaultEnabled {
			enabled++
		} else {
			disabled++
		}
	}
	if enabled != 14 || disabled != 4 {
		t.Fatalf("defaults %d/%d", enabled, disabled)
	}
	for _, bad := range []string{"COUNTERSTRIKESHARP", "SWIFTLY", "MODSHARP"} {
		if strings.Contains(strings.ToUpper(string(data)), bad) {
			t.Fatal("alternate framework exposed")
		}
	}
	again, _ := panel.Generate("s2script-cs2-egg:dev")
	if !bytes.Equal(data, again) {
		t.Fatal("nondeterministic egg")
	}
	if _, err := panel.Generate(""); err == nil {
		t.Fatal("accepted empty image")
	}
}

func TestEggUsesPanelAllocationVariables(t *testing.T) {
	b, err := panel.Generate("s2script-cs2-egg:dev")
	if err != nil {
		t.Fatal(err)
	}
	var data map[string]json.RawMessage
	if err := json.Unmarshal(b, &data); err != nil {
		t.Fatal(err)
	}
	var variables []map[string]any
	if err := json.Unmarshal(data["variables"], &variables); err != nil {
		t.Fatal(err)
	}
	for _, v := range variables {
		if v["env_variable"] == "SERVER_IP" || v["env_variable"] == "SERVER_PORT" {
			t.Fatal("reserved allocation variable exposed as editable")
		}
	}
}

func TestExplicitImageRemainsPinnedWithoutChannelUpdates(t *testing.T) {
	for _, image := range []string{"s2script-cs2-egg:dev", "ghcr.io/s2script/s2script-cs2-egg:v0.0.1-alpha.1"} {
		t.Run(image, func(t *testing.T) {
			b, err := panel.Generate(image)
			if err != nil {
				t.Fatal(err)
			}
			var egg struct {
				Meta struct {
					UpdateURL string `json:"update_url"`
				} `json:"meta"`
				Images  map[string]string `json:"docker_images"`
				Scripts struct {
					Installation struct{ Container, Script, Entrypoint string }
				} `json:"scripts"`
			}
			if err := json.Unmarshal(b, &egg); err != nil {
				t.Fatal(err)
			}
			if len(egg.Images) != 1 || egg.Images["S2Script runtime"] != image || egg.Meta.UpdateURL != "" {
				t.Fatalf("explicit image can drift to channel defaults: %+v", egg)
			}
			if egg.Scripts.Installation.Container != "ghcr.io/pterodactyl/installers:debian" {
				t.Fatal("installer depends on runtime image availability")
			}
		})
	}
}

func TestCatalogIdentitiesAndDocumentation(t *testing.T) {
	want := map[string]bool{"basecommands": true, "basechat": true, "playercommands": true, "antiflood": true, "adminhelp": true, "basecomm": true, "basebans": true, "reservedslots": true, "basetriggers": true, "funcommands": true, "clientprefs": true, "adminmenu": true, "basevotes": true, "zones": true, "nominations": false, "rockthevote": false, "nextmap": false, "funvotes": false}
	doc := string(panel.CatalogMarkdown())
	for _, p := range plugins.Bundled() {
		short := strings.TrimPrefix(p.Name, "@s2script/")
		enabled, ok := want[short]
		if !ok || enabled != p.DefaultEnabled {
			t.Fatalf("wrong catalog entry %+v", p)
		}
		if p.Variable != "S2SCRIPT_PLUGIN_"+strings.ToUpper(short) {
			t.Fatalf("wrong variable %+v", p)
		}
		if !strings.Contains(doc, p.Name) || !strings.Contains(doc, p.Variable) {
			t.Fatal("documentation omitted catalog entry")
		}
	}
}
