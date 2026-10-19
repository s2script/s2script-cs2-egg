// SPDX-License-Identifier: GPL-3.0-or-later

// Package passwords applies only the assignment delivered to this egg.
package passwords

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/K4ryuu/CS2-Egg-Go/internal/password"
	"github.com/K4ryuu/CS2-Egg-Go/internal/proto"
)

const metadataPrefix = "// cs2node assignment: "

const block = "\n// BEGIN cs2node rotating password\nexec cs2node-password.cfg\n// END cs2node rotating password\n"

type Manager struct {
	mu              sync.Mutex
	root            string
	enabled         bool
	current         proto.PasswordUpdate
	secrets         []string
	startedPassword string
	unsynced        bool
}

func Open(root string, enabled bool) (*Manager, error) {
	m := &Manager{root: root, enabled: enabled}
	// Migrate older eggs: map configs must no longer restore this assignment.
	if err := m.removeInclude(); err != nil {
		return nil, err
	}
	if !enabled {
		return m, nil
	}
	data, err := os.ReadFile(m.cachePath())
	if errors.Is(err, os.ErrNotExist) {
		return m, nil
	}
	if err != nil {
		return nil, err
	}
	var u proto.PasswordUpdate
	header, body, ok := strings.Cut(string(data), "\n")
	if !ok || !strings.HasPrefix(header, metadataPrefix) || json.Unmarshal([]byte(strings.TrimPrefix(header, metadataPrefix)), &u) != nil || body != command(u.Password)+"\n" {
		return nil, errors.New("invalid cached password assignment")
	}
	if err := m.Accept(u); err != nil {
		return nil, err
	}
	return m, nil
}

func (m *Manager) cachePath() string {
	return filepath.Join(m.root, "game/csgo/cfg/cs2node-password.cfg")
}
func (m *Manager) Ready() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return password.Valid(m.current.Password)
}

// Accept persists the assignment for the next startup. It never changes CS2's
// live password: operators may override or clear it for a playtest.
func (m *Manager) Accept(u proto.PasswordUpdate) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.enabled {
		return errors.New("password rotation is not enabled")
	}
	if !password.Valid(u.Password) {
		return errors.New("node did not provide a valid password assignment")
	}
	if _, err := time.Parse(time.RFC3339, u.NextAt); err != nil {
		return errors.New("invalid assignment due date")
	}
	if u.Password == m.current.Password && u.NextAt == m.current.NextAt {
		if m.unsynced {
			err := password.SyncDir(filepath.Dir(m.cachePath()))
			m.unsynced = err != nil
			return err
		}
		return nil
	}
	// Even a partial filesystem failure must not expose a value already written
	// into a cfg that the engine might execute.
	found := false
	for _, known := range m.secrets {
		if known == u.Password {
			found = true
			break
		}
	}
	if !found {
		m.secrets = append(m.secrets, u.Password)
	}
	u.Err = ""
	data, err := json.Marshal(u)
	if err != nil {
		return err
	}
	// Retain the cache format so existing assignments survive image upgrades.
	content := metadataPrefix + string(data) + "\n" + command(u.Password) + "\n"
	committed, err := password.WriteFile(m.cachePath(), []byte(content), 0600)
	if committed {
		m.current = u
		m.unsynced = err != nil
	} // keep memory consistent even if directory sync failed
	return err
}

func command(value string) string { return fmt.Sprintf("sv_password %q", value) }

// Startup appends the validated password as one startup convar and remembers
// the assignment this process launched with. Generated passwords are shell-safe.
func (m *Manager) Startup(cmdline string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.enabled || !password.Valid(m.current.Password) {
		return cmdline
	}
	m.startedPassword = m.current.Password
	return cmdline + " +" + command(m.startedPassword)
}

// RestartPending identifies a saved assignment newer than this process's startup
// password. Repeated delivery retries the restart, never the sv_password command.
func (m *Manager) RestartPending() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.enabled && !m.unsynced && m.startedPassword != "" && m.current.Password != m.startedPassword {
		return m.current.NextAt
	}
	return ""
}

func (m *Manager) Mask(line string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, secret := range m.secrets {
		line = strings.ReplaceAll(line, secret, "********")
	}
	return line
}

func (m *Manager) removeInclude() error {
	path := filepath.Join(m.root, "game/csgo/cfg/server.cfg")
	raw, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	original := string(raw)
	clean := strings.ReplaceAll(original, block, "")
	clean = strings.ReplaceAll(clean, strings.ReplaceAll(block, "\n", "\r\n"), "")
	if clean == original {
		return nil
	}
	mode := os.FileMode(0644)
	if st, err := os.Stat(path); err == nil {
		mode = st.Mode().Perm()
	}
	return atomicWrite(path, []byte(clean), mode)
}

func atomicWrite(path string, data []byte, mode os.FileMode) error {
	_, err := password.WriteFile(path, data, mode)
	return err
}
