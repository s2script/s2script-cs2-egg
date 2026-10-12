// SPDX-License-Identifier: GPL-3.0-or-later
package guard

import (
	"errors"
	"github.com/s2script/s2script-cs2-egg/internal/config"
	"net/netip"
	"strconv"
	"sync"
	"time"
)

type Allocation struct {
	Address  netip.Addr `json:"address"`
	Port     int        `json:"port"`
	Protocol string     `json:"protocol"`
}
type Policy struct {
	config.GuardPolicy
	ServerID   string     `json:"server_id"`
	Allocation Allocation `json:"allocation"`
}
type Finding struct {
	ServerID, RunID, SessionID, Rule, Name, State, Evidence, Provenance string
	Address                                                             netip.Addr
	UID                                                                 int
	At                                                                  time.Time
	Correlated                                                          bool
	Count                                                               int
}
type Action struct {
	RequestedSeconds                                           int
	IncidentFirst                                              time.Time
	Strikes                                                    int
	Kind, ServerID, RunID, SessionID, Rule, Reason, Provenance string
	Allocation                                                 Allocation
	Address                                                    netip.Addr
	UID                                                        int
	At, Until                                                  time.Time
}

func (a Allocation) Validate() error {
	if !a.Address.Is4() || !a.Address.IsGlobalUnicast() || a.Address.IsPrivate() || a.Address.IsLoopback() || a.Address.IsLinkLocalUnicast() || a.Port < 1 || a.Port > 65535 || a.Protocol != "udp" && a.Protocol != "tcp" {
		return errors.New("guard requires exact external IPv4/protocol/game-port tuple")
	}
	for _, p := range []int{22, 80, 443, 2022, 8080} {
		if a.Port == p {
			return errors.New("protected infrastructure port")
		}
	}
	return nil
}

var privateRanges = []netip.Prefix{netip.MustParsePrefix("0.0.0.0/8"), netip.MustParsePrefix("10.0.0.0/8"), netip.MustParsePrefix("100.64.0.0/10"), netip.MustParsePrefix("127.0.0.0/8"), netip.MustParsePrefix("169.254.0.0/16"), netip.MustParsePrefix("172.16.0.0/12"), netip.MustParsePrefix("192.0.0.0/24"), netip.MustParsePrefix("192.0.2.0/24"), netip.MustParsePrefix("192.168.0.0/16"), netip.MustParsePrefix("198.18.0.0/15"), netip.MustParsePrefix("198.51.100.0/24"), netip.MustParsePrefix("203.0.113.0/24"), netip.MustParsePrefix("224.0.0.0/4"), netip.MustParsePrefix("240.0.0.0/4")}

func Blockable(a netip.Addr, allow []string) bool {
	a = a.Unmap()
	if !a.Is4() || !a.IsGlobalUnicast() {
		return false
	}
	for _, p := range privateRanges {
		if p.Contains(a) {
			return false
		}
	}
	for _, v := range allow {
		p, e := netip.ParsePrefix(v)
		if e != nil || p.Contains(a) {
			return false
		}
	}
	return true
}
func Decide(f Finding, p Policy) Action {
	a := Action{Kind: "log", ServerID: p.ServerID, RunID: f.RunID, SessionID: f.SessionID, Rule: f.Rule, Reason: "observation only", Provenance: f.Provenance, Allocation: p.Allocation, Address: f.Address.Unmap(), UID: f.UID, At: f.At}
	if p.GuardPolicy.Validate() != nil || !p.Enabled || !Blockable(f.Address, p.Allowlist) || f.ServerID != p.ServerID || !f.Correlated || f.UID < 1 || f.UID > 999999999 || f.Rule != "reconnect_stuck" && f.Rule != "move_not_joined" {
		return a
	}
	if p.Action == "kick" {
		a.Kind = "kick"
		a.Reason = "fresh correlated pre-FULL client exceeded policy"
	}
	if p.Action == "block" && p.Delegated && p.Allocation.Validate() == nil && Blockable(f.Address, p.Allowlist) {
		a.Kind = "block"
		a.Until = f.At.Add(time.Duration(p.BlockSeconds) * time.Second)
		a.Reason = "delegated correlated console evidence"
	}
	return a
}

type incident struct {
	First, Until, Last time.Time
	Strikes            int
}
type Incidents struct {
	mu      sync.Mutex
	entries map[string]incident
}

func NewIncidents() *Incidents { return &Incidents{entries: map[string]incident{}} }
func (i *Incidents) Decide(f Finding, p Policy) Action {
	a := Decide(f, p)
	if a.Kind != "block" {
		return a
	}
	p.GuardPolicy.Validate()
	i.mu.Lock()
	defer i.mu.Unlock()
	key := p.ServerID + "/" + p.Allocation.Address.String() + "/" + p.Allocation.Protocol + "/" + strconv.Itoa(p.Allocation.Port) + "/" + a.Address.String()
	for k, v := range i.entries {
		if f.At.Sub(v.Last) > 24*time.Hour && !v.Until.After(f.At) {
			delete(i.entries, k)
		}
	}
	v, ok := i.entries[key]
	if !ok {
		if len(i.entries) >= 4096 {
			a.Kind = "log"
			a.Reason = "guard incident capacity"
			return a
		}
		v = incident{First: f.At, Strikes: 1}
	} else if v.Until.After(f.At) {
		a.Until = v.Until
		a.IncidentFirst = v.First
		a.Strikes = v.Strikes
		return a
	} else if f.At.Sub(v.First) >= time.Duration(p.MaxBlockSeconds)*time.Second {
		if f.At.Sub(v.Last) > time.Hour {
			v.Strikes = 0
		}
		v.Strikes = min(v.Strikes+1, 4)
		v.First = f.At
	}
	seconds := min(p.BlockSeconds*(1<<max(v.Strikes-1, 0)), p.MaxBlockSeconds)
	until := f.At.Add(time.Duration(seconds) * time.Second)
	deadline := v.First.Add(time.Duration(p.MaxBlockSeconds) * time.Second)
	if until.After(deadline) {
		until = deadline
	}
	if !until.After(f.At) {
		a.Kind = "log"
		a.Reason = "incident deadline reached"
		return a
	}
	v.Until = until
	v.Last = f.At
	i.entries[key] = v
	a.Until = until
	a.IncidentFirst = v.First
	a.Strikes = v.Strikes
	return a
}

func (i *Incidents) Restore(a Action) {
	if a.Kind != "block" || a.Rule == "manual" || a.IncidentFirst.IsZero() || a.Strikes < 1 || a.Strikes > 4 || time.Since(a.At) > 24*time.Hour {
		return
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	key := a.ServerID + "/" + a.Allocation.Address.String() + "/" + a.Allocation.Protocol + "/" + strconv.Itoa(a.Allocation.Port) + "/" + a.Address.String()
	if old, ok := i.entries[key]; ok && old.Last.After(a.At) {
		return
	}
	if len(i.entries) >= 4096 {
		return
	}
	i.entries[key] = incident{First: a.IncidentFirst, Until: a.Until, Last: a.At, Strikes: a.Strikes}
}
