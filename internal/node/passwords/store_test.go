// SPDX-License-Identifier: GPL-3.0-or-later

package passwords

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func testConfig(t *testing.T, mode string) Config {
	t.Helper()
	c := Defaults()
	c.Enabled = true
	c.Mode = mode
	c.Dir = t.TempDir()
	c.Wordlist = filepath.Join(c.Dir, "words.txt")
	var b strings.Builder
	for i := 0; i < 7776; i++ {
		fmt.Fprintf(&b, "word%c%c%c\n", 'a'+i/676, 'a'+(i/26)%26, 'a'+i%26)
	}
	if err := os.WriteFile(c.Wordlist, []byte(b.String()), 0600); err != nil {
		t.Fatal(err)
	}
	return c
}
func TestIntervalsAndMonthEnd(t *testing.T) {
	start := time.Date(2026, 1, 31, 15, 4, 5, 0, time.UTC)
	for _, tc := range []struct{ interval, want string }{{"daily", "2026-02-01T15:04:05Z"}, {"weekly", "2026-02-07T15:04:05Z"}, {"bi-weekly", "2026-02-14T15:04:05Z"}, {"monthly", "2026-02-28T15:04:05Z"}} {
		got, err := Next(start, tc.interval)
		if err != nil || got.Format(time.RFC3339) != tc.want {
			t.Fatalf("%s: %v %v", tc.interval, got, err)
		}
	}
	if _, err := Next(start, "yearly"); err == nil {
		t.Fatal("accepted unsupported interval")
	}
}
func TestSharedPersistenceRotationAndNotification(t *testing.T) {
	c := testConfig(t, "shared")
	s, err := Open(c)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	a, changed, err := s.Get("server-a", now)
	if err != nil || !changed || !ValidPassword(a.Password) {
		t.Fatalf("first assignment: changed=%v err=%v", changed, err)
	}
	b, changed, err := s.Get("server-b", now)
	if err != nil || changed || b != a {
		t.Fatal("shared assignment changed")
	}
	if len(s.Pending()) != 1 {
		t.Fatal("first assignment must notify once")
	}
	reopened, err := Open(c)
	if err != nil {
		t.Fatal(err)
	}
	b, changed, err = reopened.Get("server-a", now.Add(time.Hour))
	if err != nil || changed || b != a {
		t.Fatal("restart reset assignment")
	}
	if err := reopened.deliver("shared", now, func(Record) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if len(reopened.Pending()) != 0 {
		t.Fatal("notification not acknowledged")
	}
	b, changed, err = reopened.Get("server-a", a.NextAt)
	if err != nil || !changed || b.Password == a.Password {
		t.Fatal("due password not rotated")
	}
	if len(reopened.Pending()) != 1 {
		t.Fatal("rotation must notify")
	}
	st, err := os.Stat(filepath.Join(c.Dir, "state.json"))
	if err != nil || st.Mode().Perm() != 0600 {
		t.Fatal("state must be private")
	}
	st, _ = os.Stat(c.Dir)
	if st.Mode().Perm() != 0700 {
		t.Fatal("directory must be private")
	}
}
func TestUniqueAndConcurrentShared(t *testing.T) {
	c := testConfig(t, "unique")
	s, _ := Open(c)
	now := time.Now()
	seen := map[string]bool{}
	for i := 0; i < 50; i++ {
		r, _, err := s.Get(fmt.Sprintf("server-%d", i), now)
		if err != nil || seen[r.Password] {
			t.Fatal("unique collision", err)
		}
		seen[r.Password] = true
	}
	c = testConfig(t, "shared")
	s, _ = Open(c)
	var wg sync.WaitGroup
	var mu sync.Mutex
	changes := 0
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, changed, err := s.Get("one", now)
			if err != nil {
				t.Error(err)
			}
			if changed {
				mu.Lock()
				changes++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if changes != 1 {
		t.Fatalf("got %d shared generations", changes)
	}
}
func TestGenerationFailureKeepsOldAssignment(t *testing.T) {
	c := testConfig(t, "unique")
	s, _ := Open(c)
	a, _, _ := s.Get("one", time.Now())
	os.WriteFile(c.Wordlist, []byte("apple\npear\n"), 0600)
	b, changed, err := s.Get("one", a.NextAt)
	if err == nil || changed || b.Password != a.Password {
		t.Fatal("invalid dictionary replaced known password")
	}
	// A failed persistence must not publish the newly generated credential.
	c = testConfig(t, "unique")
	s, _ = Open(c)
	a, _, _ = s.Get("one", time.Now())
	os.Remove(filepath.Join(c.Dir, "state.json"))
	os.Mkdir(filepath.Join(c.Dir, "state.json"), 0700)
	b, changed, err = s.Get("one", a.NextAt)
	if err == nil || changed || b.Password != a.Password {
		t.Fatal("disk failure published a new credential")
	}
}
func TestRejectCorruptionAndDisabledGeneration(t *testing.T) {
	c := testConfig(t, "unique")
	os.WriteFile(filepath.Join(c.Dir, "state.json"), []byte("broken"), 0600)
	if _, err := Open(c); err == nil {
		t.Fatal("corrupt state accepted")
	}
	os.Remove(filepath.Join(c.Dir, "state.json"))
	c.Enabled = false
	s, _ := Open(c)
	if _, _, err := s.Get("one", time.Now()); err == nil {
		t.Fatal("disabled master generated password")
	}
	c.Enabled = true
	s, _ = Open(c)
	if _, _, err := s.Get("../other", time.Now()); err == nil {
		t.Fatal("unsafe server identifier accepted")
	}
}

func TestStructurallyIncompleteStateIsNotANewStore(t *testing.T) {
	c := testConfig(t, "unique")
	for _, raw := range []string{`{}`, `null`, `{"version":1}`, `{"records":{}}`, `{"version":1,"records":null}`} {
		os.WriteFile(filepath.Join(c.Dir, "state.json"), []byte(raw), 0600)
		if _, err := Open(c); err == nil {
			t.Fatalf("incomplete state accepted: %s", raw)
		}
	}
}

func TestDeliveryCoordinatesRotationAndSkipsExpired(t *testing.T) {
	c := testConfig(t, "shared")
	s, _ := Open(c)
	now := time.Now()
	a, _, _ := s.Get("one", now)
	started := make(chan struct{})
	release := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- s.deliver("shared", now, func(r Record) error { close(started); <-release; return nil })
	}()
	<-started
	b, changed, err := s.Get("one", a.NextAt)
	if err != nil || changed || b.Password != a.Password {
		t.Fatal("regenerated while credential delivery was in flight")
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	b, changed, err = s.Get("one", a.NextAt)
	if err != nil || !changed {
		t.Fatal("rotation did not resume")
	}
	called := false
	s.deliver("shared", b.NextAt, func(Record) error { called = true; return nil })
	if called {
		t.Fatal("delivered expired credential")
	}
	s.deliver("shared", b.GeneratedAt, func(r Record) error {
		if r.Password != b.Password {
			t.Fatal("stale snapshot delivered")
		}
		return nil
	})
}
func TestWordlistValidationAndCommandSafety(t *testing.T) {
	for _, raw := range []string{"apple\napple\n", "11111\tapple\n11112\tpear\n", "apple;quit\n", "UPPER\n"} {
		if _, err := ParseWords(strings.NewReader(raw)); err == nil {
			t.Fatal("accepted small or unsafe wordlist")
		}
	}
	for _, p := range []string{"", "a-b-c", "apple-pear-quit;", "apple-pear-map\nquit"} {
		if ValidPassword(p) {
			t.Fatal("unsafe password accepted")
		}
	}
}
