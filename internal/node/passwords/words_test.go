// SPDX-License-Identifier: GPL-3.0-or-later

package passwords

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"strings"
	"testing"
	"time"
)

// Exercise the actual pinned download, including its four compound entries.
// Synthetic all-ASCII lists failed to catch the production provisioning failure.
func TestPinnedEFFDictionaryGeneratesPersistedAssignment(t *testing.T) {
	raw, err := os.ReadFile("testdata/eff_large_wordlist.txt")
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(raw)
	if hex.EncodeToString(sum[:]) != WordlistSHA256 {
		t.Fatal("fixture differs from pinned download")
	}
	words, err := ParseWords(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("pinned dictionary rejected: %v", err)
	}
	if len(words) != 7772 {
		t.Fatalf("got %d usable words, want 7772", len(words))
	}
	for _, w := range words {
		if !ValidPassword(w + "-lantern-copper") {
			t.Fatalf("unsafe generated word %q", w)
		}
	}
	cfg := testConfig(t, "shared")
	if err := os.WriteFile(cfg.Wordlist, raw, 0600); err != nil {
		t.Fatal(err)
	}
	s, err := Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	r, created, err := s.Get("server-one", time.Now())
	if err != nil || !created || !ValidPassword(r.Password) {
		t.Fatalf("generation failed: created=%v err=%v", created, err)
	}
	reopened, err := Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	again, created, err := reopened.Get("server-two", r.GeneratedAt)
	if err != nil || created || again.Password != r.Password {
		t.Fatal("shared assignment did not survive restart", err)
	}
}

func TestEFFCompoundFilteringDoesNotAdmitUnsafeOrUndersizedLists(t *testing.T) {
	raw, err := os.ReadFile("testdata/eff_large_wordlist.txt")
	if err != nil {
		t.Fatal(err)
	}
	for _, extra := range []string{"11111\tapple;quit\n", "11111\tapple--pear\n", "11111\t-apple\n"} {
		if _, err := ParseWords(strings.NewReader(string(raw) + extra)); err == nil {
			t.Fatal("accepted unsafe dictionary entry")
		}
	}
	_, rest, _ := strings.Cut(string(raw), "\n")
	if _, err := ParseWords(strings.NewReader(rest)); err == nil {
		t.Fatal("accepted too few usable words")
	}
}
