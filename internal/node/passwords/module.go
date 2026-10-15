// SPDX-License-Identifier: GPL-3.0-or-later

package passwords

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/K4ryuu/CS2-Egg-Go/internal/node/alerts"
	"github.com/K4ryuu/CS2-Egg-Go/internal/node/core"
	"github.com/K4ryuu/CS2-Egg-Go/internal/node/doctor"
	"github.com/K4ryuu/CS2-Egg-Go/internal/node/module"
	"github.com/K4ryuu/CS2-Egg-Go/internal/node/nodeconfig"
	"github.com/K4ryuu/CS2-Egg-Go/internal/node/ui"
	"github.com/K4ryuu/CS2-Egg-Go/internal/proto"
)

type Module struct {
	delivery sync.Map
	Log      *slog.Logger
	HTTP     *http.Client
	cfg      Config
	store    *Store
}

func New(log *slog.Logger, client *http.Client) *Module { return &Module{Log: log, HTTP: client} }
func (*Module) DefaultEnabled() bool                    { return false }
func (*Module) Name() string                            { return "passwords" }
func (*Module) Describe() string {
	return "Rotating join passwords: opt-in servers, three random words, Discord delivery"
}
func (*Module) Questions() []module.Question {
	d := Defaults()
	return []module.Question{
		{Key: "mode", Prompt: "Password sharing", Default: d.Mode, Kind: module.Choice, Choices: []module.Option{{Value: "unique", Desc: "each participating server gets a different password"}, {Value: "shared", Desc: "participating servers on this node share one password"}}},
		{Key: "interval", Prompt: "Password regeneration interval", Default: d.Interval, Kind: module.Choice, Choices: []module.Option{{Value: "daily", Desc: "every 24 hours"}, {Value: "weekly", Desc: "every seven days"}, {Value: "bi-weekly", Desc: "every fourteen days"}, {Value: "monthly", Desc: "every calendar month"}}},
		{Key: "wordlist", Prompt: "Dictionary file", Default: d.Wordlist, Help: "default is downloaded once with a pinned checksum; custom list requires at least 7776 distinct words"},
		{Key: "dir", Prompt: "Private password state directory", Default: d.Dir},
	}
}
func (*Module) Notes() []string {
	return []string{"Set ENABLE_ROTATING_PASSWORD=1 on each participating server and restart it.", "Generated passwords are sent to the enabled alerts webhook, including the first generation; use a private Discord channel.", "Node mode/config changes require a daemon restart; disabling rotation retains each server's last password."}
}

func (m *Module) Run(ctx context.Context, c *core.Core) error {
	m.cfg = Defaults()
	if err := c.Config(m.Name(), &m.cfg); err != nil {
		return err
	}
	if !m.cfg.Enabled {
		return nil
	}
	s, err := Open(m.cfg)
	if err != nil {
		return err
	}
	m.store = s
	c.ProvideStatus(m.Name(), func() any {
		return map[string]any{"mode": m.cfg.Mode, "interval": m.cfg.Interval, "pending_notifications": len(s.Pending())}
	})
	c.Handle(proto.TypePasswordQuery, func(server string, _ proto.Message) { m.send(c, server) })
	downloadCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	if err := Provision(downloadCtx, m.HTTP, m.cfg.Wordlist); err != nil {
		m.Log.Warn("password dictionary unavailable; retaining existing assignments")
	}
	cancel()
	m.Log.Info("password rotation active", "mode", m.cfg.Mode, "interval", m.cfg.Interval)
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	// Retry a saved notification after a daemon restart, including first generation.
	m.notify(ctx, c)
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			for _, server := range c.Containers() {
				if c.Connected(server.Name) && c.PasswordOptIn(server.Name) {
					m.send(c, server.Name)
				}
			}
			m.notify(ctx, c)
		}
	}
}

func (m *Module) send(c *core.Core, server string) {
	if !m.cfg.Enabled || !c.PasswordOptIn(server) {
		c.Send(server, &proto.PasswordUpdate{Err: "password rotation is not enabled for this server"})
		return
	}
	m.withAssignment(server, time.Now(), func(r Record, err error) {
		u := &proto.PasswordUpdate{Password: r.Password}
		if !r.NextAt.IsZero() {
			u.NextAt = r.NextAt.Format(time.RFC3339)
		}
		if err != nil {
			u.Err = "password regeneration unavailable; retaining the last assignment"
			m.Log.Warn("password regeneration unavailable", "server", server)
		}
		c.Send(server, u)
	})
}

// notify deliberately bypasses the generic notice bus and event filters: a
// persisted credential notification must survive dropped notices and restarts.
func (m *Module) notify(ctx context.Context, c *core.Core) {
	cfg := alerts.Defaults()
	cfg.Enabled = false
	if c.Config("alerts", &cfg) != nil || !cfg.Enabled || cfg.Check() != nil {
		return
	}
	for key := range m.store.Pending() {
		if (m.cfg.Mode == "shared") != (key == "shared") {
			continue
		}
		server := strings.TrimPrefix(key, "server:")
		if key == "shared" {
			server = ""
		}
		err := m.store.deliver(key, time.Now(), func(r Record) error {
			st, _ := c.State(server)
			n := core.Notice{Kind: core.NoticePasswordGenerated, Container: server, Server: st.Name, Time: r.GeneratedAt, Fields: map[string]any{"password": r.Password, "mode": m.cfg.Mode, "interval": m.cfg.Interval, "next_at": r.NextAt.Format(time.RFC3339)}}
			postCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
			defer cancel()
			return alerts.Post(postCtx, m.HTTP, cfg, []core.Notice{n})
		})
		if err != nil {
			m.Log.Warn("password Discord delivery not confirmed; will retry", "server", server)
		}
	}
}

func (m *Module) Doctor(ctx context.Context, c *core.Core) []doctor.Check {
	cfg := Defaults()
	if err := c.Config(m.Name(), &cfg); err != nil {
		return []doctor.Check{doctor.Failf("Passwords", "unreadable config")}
	}
	if err := cfg.Check(); err != nil {
		return []doctor.Check{doctor.Failf("Passwords", err.Error())}
	}
	if _, err := readState(cfg.Dir + "/state.json"); err != nil {
		return []doctor.Check{doctor.Failf("Passwords", "invalid saved state")}
	}
	out := []doctor.Check{doctor.Ok("Passwords", cfg.Mode+", "+cfg.Interval)}
	a := alerts.Defaults()
	a.Enabled = false
	if c.Config("alerts", &a) != nil || !a.Enabled || a.Check() != nil {
		out = append(out, doctor.Warnf("Passwords", "Discord delivery needs the alerts module and a valid private-channel webhook"))
	}
	return out
}

func Command(w io.Writer, path string, args []string) error {
	if len(args) != 2 || args[0] != "show" {
		return errors.New("usage: cs2node passwords show <full-server-uuid>")
	}
	cfg, err := nodeconfig.Load(path)
	if err != nil {
		return err
	}
	p := Defaults()
	if _, err = cfg.Section("passwords", &p); err != nil {
		return err
	}
	r, err := Reveal(p, args[1])
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(w, "Password: %s\nNext regeneration: %s\n", r.Password, r.NextAt.Format(time.RFC3339))
	return err
}

// Serialize retrieval and transmission per server so a delayed old reply cannot
// overtake a newly generated assignment on the same socket.
func (m *Module) lockServer(server string) func() {
	value, _ := m.delivery.LoadOrStore(server, &sync.Mutex{})
	lock := value.(*sync.Mutex)
	lock.Lock()
	return lock.Unlock
}

func (m *Module) PrintStatus(w io.Writer, _ *core.Core, st core.Status) {
	var data struct {
		Mode     string
		Interval string
		Pending  int `json:"pending_notifications"`
	}
	if json.Unmarshal(st.Module[m.Name()], &data) != nil {
		return
	}
	ui.Section(w, "Join passwords")
	ui.KV(w, "mode", data.Mode)
	ui.KV(w, "regeneration", data.Interval)
	ui.KV(w, "pending Discord messages", fmt.Sprint(data.Pending))
}

func (m *Module) withAssignment(server string, now time.Time, send func(Record, error)) {
	unlock := m.lockServer(server)
	defer unlock()
	r, _, err := m.store.Get(server, now)
	send(r, err)
}
