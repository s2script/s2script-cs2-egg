// SPDX-License-Identifier: GPL-3.0-or-later

package startup_test

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/K4ryuu/CS2-Egg-Go/internal/egg/startup"
)

const shimPath = "game/csgo/addons/s2script/bin/linuxsteamrt64/s2script.so"

// Minimal ELF64 shared object with one PT_GNU_STACK program header. The
// flags are ELF PF_R|PF_W (6), optionally PF_X (1); no code is executed.
func writeShim(t *testing.T, root string, flags uint32, stackHeader bool) {
	t.Helper()
	data := make([]byte, 120)
	copy(data, []byte{0x7f, 'E', 'L', 'F', 2, 1, 1})
	binary.LittleEndian.PutUint16(data[16:], 3)
	binary.LittleEndian.PutUint16(data[18:], 62)
	binary.LittleEndian.PutUint32(data[20:], 1)
	binary.LittleEndian.PutUint64(data[32:], 64)
	binary.LittleEndian.PutUint16(data[52:], 64)
	binary.LittleEndian.PutUint16(data[54:], 56)
	if stackHeader {
		binary.LittleEndian.PutUint16(data[56:], 1)
		binary.LittleEndian.PutUint32(data[64:], 0x6474e551)
		binary.LittleEndian.PutUint32(data[68:], flags)
	}
	path := filepath.Join(root, shimPath)
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatal(err)
	}
}

func TestRuntimeEnvironmentAddsExecstackOnlyForRequestingShim(t *testing.T) {
	for _, tc := range []struct {
		name        string
		installed   bool
		flags       uint32
		stackHeader bool
		wantCompat  bool
	}{
		{"absent", false, 0, false, false},
		{"non-executable", true, 6, true, false},
		{"executable", true, 7, true, true},
		{"legacy missing stack header", true, 0, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			if tc.installed {
				writeShim(t, root, tc.flags, tc.stackHeader)
			}
			in := []string{"PATH=/usr/bin", "GLIBC_TUNABLES=glibc.malloc.trim_threshold=1024"}
			original := append([]string(nil), in...)
			got, compat, err := startup.Environment(root, in)
			if err != nil || compat != tc.wantCompat {
				t.Fatalf("compat = %v, err = %v", compat, err)
			}
			want := original
			if tc.wantCompat {
				want = []string{"PATH=/usr/bin", "GLIBC_TUNABLES=glibc.malloc.trim_threshold=1024:glibc.rtld.execstack=2"}
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("environment = %v, want %v", got, want)
			}
			if !reflect.DeepEqual(in, original) {
				t.Fatalf("inherited environment mutated: %v", in)
			}
		})
	}
}

func TestRuntimeEnvironmentHandlesExistingExecstackPolicy(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   []string
		want []string
	}{
		{"unset", []string{"PATH=/usr/bin"}, []string{"PATH=/usr/bin", "GLIBC_TUNABLES=glibc.rtld.execstack=2"}},
		{"already compatible", []string{"GLIBC_TUNABLES=glibc.rtld.execstack=2:glibc.malloc.arena_max=4"}, []string{"GLIBC_TUNABLES=glibc.rtld.execstack=2:glibc.malloc.arena_max=4"}},
		{"explicit denial", []string{"GLIBC_TUNABLES=glibc.rtld.execstack=0"}, nil},
		{"auto negotiation cannot load plugin", []string{"GLIBC_TUNABLES=glibc.rtld.execstack=1"}, nil},
		{"secure mode disables compatibility", []string{"GLIBC_TUNABLES=glibc.rtld.enable_secure=1"}, nil},
		{"secure mode overrides explicit compatibility", []string{"GLIBC_TUNABLES=glibc.rtld.enable_secure=1:glibc.rtld.execstack=2"}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			writeShim(t, root, 7, true)
			got, _, err := startup.Environment(root, tc.in)
			if tc.want == nil {
				if err == nil || !strings.Contains(err.Error(), "GLIBC_TUNABLES") {
					t.Fatalf("expected actionable policy conflict, got %v", err)
				}
				return
			}
			if err != nil || !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("environment = %v, err = %v; want %v", got, err, tc.want)
			}
		})
	}
}

func TestRuntimeEnvironmentRejectsInvalidOrEscapingShim(t *testing.T) {
	for _, escaping := range []bool{false, true} {
		root := t.TempDir()
		writeShim(t, root, 7, true)
		path := filepath.Join(root, shimPath)
		if escaping {
			outside := t.TempDir()
			writeShim(t, outside, 7, true)
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(filepath.Join(outside, shimPath), path); err != nil {
				t.Fatal(err)
			}
		} else if err := os.WriteFile(path, []byte("broken ELF"), 0644); err != nil {
			t.Fatal(err)
		}
		if _, _, err := startup.Environment(root, nil); err == nil {
			t.Fatalf("invalid/escaping shim accepted (escaping=%v)", escaping)
		}
	}
}
