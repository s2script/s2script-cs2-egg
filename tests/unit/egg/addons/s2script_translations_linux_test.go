// SPDX-License-Identifier: GPL-3.0-or-later

package addons_test

import (
	"context"
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/K4ryuu/CS2-Egg-Go/internal/egg/addons"
	"golang.org/x/sys/unix"
)

func TestS2ScriptPublishesTranslationsWithoutRemovingOrTruncatingLiveCatalog(t *testing.T) {
	files := s2files(t, "0.8.1")
	name := translationDir + "common.phrases.json"
	files[name] = []byte(`{"New":"new"}`)
	e := s2fixture(t, s2zip(t, files), "0.8.1", "valid", "/latest", false)
	s2write(t, e.Root, name, []byte(`{"Custom":"keep"}`))
	fd, err := unix.InotifyInit1(unix.IN_CLOEXEC | unix.IN_NONBLOCK)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(fd)
	dir := filepath.Join(e.Root, "game/csgo", translationDir)
	if _, err := unix.InotifyAddWatch(fd, dir, unix.IN_DELETE|unix.IN_CREATE|unix.IN_MODIFY|unix.IN_MOVED_TO); err != nil {
		t.Fatal(err)
	}
	if err := e.UpdateS2Script(context.Background(), addons.S2ScriptOptions{}); err != nil {
		t.Fatal(err)
	}
	// Observe publication through the filesystem: readers must never see the
	// live name disappear or its content being written in place. All data must
	// be written successfully before the catalog is replaced in one operation.
	published := false
	var buf [4096]byte
	for {
		n, err := unix.Read(fd, buf[:])
		if errors.Is(err, unix.EAGAIN) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		for offset := 0; offset < n; {
			header := buf[offset : offset+unix.SizeofInotifyEvent]
			mask := binary.NativeEndian.Uint32(header[4:8])
			length := int(binary.NativeEndian.Uint32(header[12:16]))
			offset += unix.SizeofInotifyEvent
			filename := strings.TrimRight(string(buf[offset:offset+length]), "\x00")
			offset += length
			if filename != "common.phrases.json" {
				continue
			}
			if mask&(unix.IN_DELETE|unix.IN_CREATE|unix.IN_MODIFY) != 0 {
				t.Fatalf("live translation was removed or written in place: mask %#x", mask)
			}
			published = published || mask&unix.IN_MOVED_TO != 0
		}
	}
	if !published {
		t.Fatal("merged catalog was not published")
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "common.phrases.json" {
		t.Fatal("temporary publication files were left behind")
	}
}
