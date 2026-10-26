// SPDX-License-Identifier: GPL-3.0-or-later

package passwords

import (
	"encoding/json"
	"errors"
	"github.com/K4ryuu/CS2-Egg-Go/internal/password"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

type Record struct {
	Password    string    `json:"password"`
	GeneratedAt time.Time `json:"generated_at"`
	NextAt      time.Time `json:"next_at"`
	Pending     bool      `json:"pending"`
}
type diskState struct {
	Version int               `json:"version"`
	Records map[string]Record `json:"records"`
}
type Store struct {
	mu         sync.Mutex
	cfg        Config
	state      diskState
	delivering map[string]bool
	unsynced   bool
}

var serverPattern = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,128}$`)

func Open(cfg Config) (*Store, error) {
	if err := cfg.Check(); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(cfg.Dir, 0700); err != nil {
		return nil, err
	}
	if err := os.Chmod(cfg.Dir, 0700); err != nil {
		return nil, err
	}
	state, err := readState(filepath.Join(cfg.Dir, "state.json"))
	if err != nil {
		return nil, err
	}
	return &Store{cfg: cfg, state: state, delivering: map[string]bool{}}, nil
}

func readState(path string) (diskState, error) {
	var state diskState
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return diskState{Version: 1, Records: map[string]Record{}}, nil
	}
	if err != nil {
		return state, err
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, 16<<20))
	if err != nil {
		return state, err
	}
	// Do not include JSON parse errors: corrupt input may contain a credential.
	if json.Unmarshal(raw, &state) != nil || state.Version != 1 || state.Records == nil {
		return state, errors.New("invalid password state; restore its backup before continuing")
	}
	for key, r := range state.Records {
		if key != "shared" && (!strings.HasPrefix(key, "server:") || !serverPattern.MatchString(strings.TrimPrefix(key, "server:"))) {
			return state, errors.New("invalid password state key")
		}
		if !ValidPassword(r.Password) || r.GeneratedAt.IsZero() || !r.NextAt.After(r.GeneratedAt) {
			return state, errors.New("invalid password state record")
		}
	}
	return state, nil
}

func (s *Store) key(server string) string {
	if s.cfg.Mode == "shared" {
		return "shared"
	}
	return "server:" + server
}

// Get retains and returns the old assignment alongside any generation error.
func (s *Store) Get(server string, now time.Time) (Record, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.cfg.Enabled {
		return Record{}, false, errors.New("password rotation is disabled")
	}
	if !serverPattern.MatchString(server) {
		return Record{}, false, errors.New("invalid server identifier")
	}
	if err := s.sync(); err != nil {
		return Record{}, false, err
	}
	key := s.key(server)
	old := s.state.Records[key]
	if old.Password != "" && (now.Before(old.NextAt) || s.delivering[key]) {
		return old, false, nil
	}
	var p string
	for tries := 0; tries < 128; tries++ {
		candidate, err := generate(s.cfg.Wordlist)
		if err != nil {
			return old, false, err
		}
		collision := false
		for _, r := range s.state.Records {
			if candidate == r.Password {
				collision = true
				break
			}
		}
		if !collision {
			p = candidate
			break
		}
	}
	if p == "" {
		return old, false, errors.New("could not generate a distinct password")
	}
	next, err := s.cfg.Next(now)
	if err != nil {
		return old, false, err
	}
	r := Record{Password: p, GeneratedAt: now.UTC(), NextAt: next, Pending: true}
	if err := s.replace(key, r); err != nil {
		return old, false, err
	}
	return r, true, nil
}

func (s *Store) replace(key string, r Record) error {
	state := diskState{Version: 1, Records: make(map[string]Record, len(s.state.Records)+1)}
	for k, v := range s.state.Records {
		state.Records[k] = v
	}
	state.Records[key] = r
	data, err := json.Marshal(state)
	if err != nil {
		return err
	}
	committed, err := password.WriteFile(filepath.Join(s.cfg.Dir, "state.json"), data, 0600)
	if committed {
		s.state = state
		s.unsynced = err != nil
	}
	return err
}
func (s *Store) Pending() map[string]Record {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[string]Record{}
	if s.sync() != nil {
		return out
	}
	for key, r := range s.state.Records {
		if r.Pending {
			out[key] = r
		}
	}
	return out
}
func Reveal(cfg Config, server string) (Record, error) {
	if err := cfg.Check(); err != nil {
		return Record{}, err
	}
	if !serverPattern.MatchString(server) {
		return Record{}, errors.New("invalid server identifier")
	}
	state, err := readState(filepath.Join(cfg.Dir, "state.json"))
	if err != nil {
		return Record{}, err
	}
	key := "server:" + server
	if cfg.Mode == "shared" {
		key = "shared"
	}
	r, ok := state.Records[key]
	if !ok {
		return r, errors.New("no saved assignment")
	}
	return r, nil
}

// sync retries an uncertain directory commit before anything is distributed.
func (s *Store) sync() error {
	if !s.unsynced {
		return nil
	}
	if err := password.SyncDir(s.cfg.Dir); err != nil {
		return err
	}
	s.unsynced = false
	return nil
}

// deliver rechecks the current record, and prevents that assignment from
// regenerating during its bounded HTTP delivery. Other assignments remain free.
func (s *Store) deliver(key string, now time.Time, send func(Record) error) error {
	s.mu.Lock()
	if err := s.sync(); err != nil {
		s.mu.Unlock()
		return err
	}
	r, ok := s.state.Records[key]
	if !ok || !r.Pending || !now.Before(r.NextAt) || s.delivering[key] {
		s.mu.Unlock()
		return nil
	}
	s.delivering[key] = true
	s.mu.Unlock()
	err := send(r)
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.delivering, key)
	if err != nil {
		return err
	}
	r.Pending = false
	return s.replace(key, r)
}

func writePrivate(path string, data []byte) error {
	_, err := password.WriteFile(path, data, 0600)
	return err
}
