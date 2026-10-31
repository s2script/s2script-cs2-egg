// SPDX-License-Identifier: GPL-3.0-or-later

package addons_test

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/K4ryuu/CS2-Egg-Go/internal/egg/addons"
)

const translationDir = "addons/s2script/translations/"

func TestS2ScriptMergesNewTranslationPhrases(t *testing.T) {
	for _, catalog := range []string{"common.phrases.json", "ru/common.phrases.json"} {
		t.Run(catalog, func(t *testing.T) {
			files := s2files(t, "0.8.1")
			files[translationDir+catalog] = []byte(`{"Edited":"release {1}","Blank":"default","New":"new {1}"}`)
			// Root English phrases must not be copied into language catalogs.
			files[translationDir+"other.phrases.json"] = []byte(`{"English only":"English"}`)
			files[translationDir+"fresh.phrases.json"] = []byte(`{"Fresh":"new file"}`)
			files[translationDir+"de/fresh.phrases.json"] = []byte(`{"Fresh":"Neue Datei"}`)
			e := s2fixture(t, s2zip(t, files), "0.8.1", "valid", "/latest", false)
			name := translationDir + catalog
			s2write(t, e.Root, name, []byte(`{"Edited":"my wording {1}","Blank":"","Custom":"mine","Obsolete":"keep"}`))
			filename := filepath.Join(e.Root, "game/csgo", name)
			if err := os.Chmod(filename, 0664); err != nil {
				t.Fatal(err)
			}
			private := []byte("custom-only catalog, left untouched even if malformed")
			s2write(t, e.Root, translationDir+"private.phrases.json", private)
			if err := e.UpdateS2Script(context.Background(), addons.S2ScriptOptions{}); err != nil {
				t.Fatal(err)
			}
			var got map[string]string
			if err := json.Unmarshal(s2read(t, e.Root, name), &got); err != nil {
				t.Fatal(err)
			}
			want := map[string]string{
				"Edited": "my wording {1}", "Blank": "", "Custom": "mine",
				"Obsolete": "keep", "New": "new {1}",
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("merged phrases = %#v, want %#v", got, want)
			}
			info, err := os.Stat(filename)
			if err != nil || info.Mode().Perm() != 0664 {
				t.Fatalf("custom catalog permissions changed: %v, %v", info, err)
			}
			for _, fresh := range []string{"fresh.phrases.json", "de/fresh.phrases.json"} {
				if !bytes.Equal(s2read(t, e.Root, translationDir+fresh), files[translationDir+fresh]) {
					t.Fatalf("missing catalog %s was not installed", fresh)
				}
			}
			if !bytes.Equal(s2read(t, e.Root, translationDir+"private.phrases.json"), private) {
				t.Fatal("custom-only catalog changed")
			}
			if !addons.S2ScriptHostVerified(e.Root, "0.8.1") {
				t.Fatal("translation merge prevented framework verification")
			}
		})
	}
}

func TestS2ScriptDoesNotRewriteTranslationsWithoutNewPhrases(t *testing.T) {
	files := s2files(t, "0.8.1")
	name := translationDir + "common.phrases.json"
	files[name] = []byte(`{"Edited":"upstream"}`)
	e := s2fixture(t, s2zip(t, files), "0.8.1", "valid", "/latest", false)
	custom := []byte("{\n    \"Edited\": \"Custom 日本語\",\n    \"Extra\": \"mine\"\n}\n")
	s2write(t, e.Root, name, custom)
	filename := filepath.Join(e.Root, "game/csgo", name)
	stamp := time.Unix(1_700_000_000, 0)
	if err := os.Chtimes(filename, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(filename)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.UpdateS2Script(context.Background(), addons.S2ScriptOptions{}); err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(filename)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(s2read(t, e.Root, name), custom) || !os.SameFile(before, after) || !after.ModTime().Equal(stamp) {
		t.Fatal("catalog with no missing keys was rewritten")
	}
}

func TestS2ScriptPreflightsTranslationJSONBeforeCopies(t *testing.T) {
	for _, origin := range []string{"installed", "release", "new release file"} {
		for _, malformed := range []string{`{`, `null`, `[]`, `{"Phrase":null}`, `{"Phrase":7}`, `{"Phrase":{}}`, `{} {}`} {
			t.Run(origin+"/"+malformed, func(t *testing.T) {
				files := s2files(t, "0.8.1")
				name := translationDir + "z-bad.phrases.json"
				files[name] = []byte(`{"Phrase":"release"}`)
				files[translationDir+"a-good.phrases.json"] = []byte(`{"New":"new"}`)
				custom := []byte(`{"Phrase":"custom"}`)
				if origin == "installed" {
					custom = []byte(malformed)
				} else {
					files[name] = []byte(malformed)
				}
				e := s2fixture(t, s2zip(t, files), "0.8.1", "valid", "/latest", false)
				if origin != "new release file" {
					s2write(t, e.Root, name, custom)
				}
				s2write(t, e.Root, translationDir+"a-good.phrases.json", []byte(`{"Custom":"keep"}`))
				s2write(t, e.Root, "addons/metamod/s2script.vdf", []byte("installed loader"))
				if err := e.UpdateS2Script(context.Background(), addons.S2ScriptOptions{}); err == nil || !strings.Contains(err.Error(), "z-bad.phrases.json") {
					t.Fatalf("invalid translation should fail with its path: %v", err)
				}
				if origin != "new release file" && !bytes.Equal(s2read(t, e.Root, name), custom) {
					t.Fatal("invalid translation overwritten")
				}
				if string(s2read(t, e.Root, translationDir+"a-good.phrases.json")) != `{"Custom":"keep"}` {
					t.Fatal("another translation changed before all catalogs were validated")
				}
				if string(s2read(t, e.Root, "addons/metamod/s2script.vdf")) != "installed loader" {
					t.Fatal("framework changed before translation validation")
				}
			})
		}
	}
}

func TestS2ScriptRejectsUnsafeTranslationDestinationsBeforeCopies(t *testing.T) {
	for _, kind := range []string{"symlink", "directory", "parent symlink"} {
		t.Run(kind, func(t *testing.T) {
			files := s2files(t, "0.8.1")
			name := translationDir + "ru/common.phrases.json"
			files[name] = []byte(`{"New":"release"}`)
			e := s2fixture(t, s2zip(t, files), "0.8.1", "valid", "/latest", false)
			s2write(t, e.Root, "addons/metamod/s2script.vdf", []byte("installed loader"))
			private := filepath.Join(t.TempDir(), "common.phrases.json")
			if err := os.WriteFile(private, []byte(`{"Private":"keep"}`), 0600); err != nil {
				t.Fatal(err)
			}
			target := filepath.Join(e.Root, "game/csgo", name)
			if kind == "parent symlink" {
				target = filepath.Dir(target)
			}
			if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
				t.Fatal(err)
			}
			var err error
			switch kind {
			case "symlink":
				err = os.Symlink(private, target)
			case "directory":
				err = os.Mkdir(target, 0755)
			case "parent symlink":
				err = os.Symlink(filepath.Dir(private), target)
			}
			if err != nil {
				t.Fatal(err)
			}
			if err := e.UpdateS2Script(context.Background(), addons.S2ScriptOptions{}); err == nil {
				t.Fatal("unsafe translation destination accepted")
			}
			if string(s2read(t, e.Root, "addons/metamod/s2script.vdf")) != "installed loader" {
				t.Fatal("framework changed before destination validation")
			}
			data, err := os.ReadFile(private)
			if err != nil || string(data) != `{"Private":"keep"}` {
				t.Fatal("private translation target changed")
			}
			info, err := os.Lstat(target)
			if err != nil || (kind == "directory" && !info.IsDir()) || (kind != "directory" && info.Mode()&os.ModeSymlink == 0) {
				t.Fatal("unsafe destination was replaced")
			}
		})
	}
}
