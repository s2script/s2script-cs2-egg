// SPDX-License-Identifier: GPL-3.0-or-later
package config

import (
	"errors"
	"net/netip"
)

type GuardPolicy struct {
	Enabled            bool     `json:"enabled"`
	Action             string   `json:"action"`
	Delegated          bool     `json:"delegated"`
	Allowlist          []string `json:"allowlist"`
	WindowSeconds      int      `json:"window_seconds"`
	GraceSeconds       int      `json:"grace_seconds"`
	PairSeconds        int      `json:"pair_seconds"`
	TTLSeconds         int      `json:"ttl_seconds"`
	FreshSeconds       int      `json:"fresh_seconds"`
	CooldownSeconds    int      `json:"cooldown_seconds"`
	BlockSeconds       int      `json:"block_seconds"`
	MaxBlockSeconds    int      `json:"max_block_seconds"`
	ReconnectThreshold int      `json:"reconnect_threshold"`
	MoveThreshold      int      `json:"move_threshold"`
}

func (p *GuardPolicy) Validate() error {
	if p.Action == "" {
		p.Action = "log"
	}
	if p.Action != "log" && p.Action != "kick" && p.Action != "block" {
		return errors.New("guard action must be log/kick/block")
	}
	for _, v := range []struct {
		p           *int
		def, lo, hi int
	}{{&p.WindowSeconds, 10, 1, 60}, {&p.GraceSeconds, 30, 10, 120}, {&p.PairSeconds, 30, 1, 60}, {&p.TTLSeconds, 120, 30, 300}, {&p.FreshSeconds, 5, 1, 10}, {&p.CooldownSeconds, 30, 5, 300}, {&p.BlockSeconds, 60, 1, 600}, {&p.MaxBlockSeconds, 600, 1, 3600}, {&p.ReconnectThreshold, 5, 2, 1000}, {&p.MoveThreshold, 8, 2, 1000}} {
		if *v.p == 0 {
			*v.p = v.def
		}
		if *v.p < v.lo || *v.p > v.hi {
			return errors.New("guard numeric policy outside bounds")
		}
	}
	if p.GraceSeconds >= p.TTLSeconds || p.PairSeconds > p.TTLSeconds || p.BlockSeconds > p.MaxBlockSeconds || len(p.Allowlist) > 128 {
		return errors.New("inconsistent guard policy")
	}
	for _, v := range p.Allowlist {
		p, e := netip.ParsePrefix(v)
		if e != nil || p != p.Masked() {
			return errors.New("guard allowlist requires canonical CIDRs")
		}
	}
	return nil
}

type GuardAllocation struct {
	IP       string `json:"ip"`
	Port     int    `json:"port"`
	Protocol string `json:"protocol"`
}
type GuardNode struct {
	Allocations  map[string]GuardAllocation `json:"allocations"`
	Enabled      bool                       `json:"enabled"`
	HelperSocket string                     `json:"helper_socket"`
	HelperUID    uint32                     `json:"helper_uid"`
	Policies     map[string]GuardPolicy     `json:"policies"`
}

func (g *GuardNode) Validate() error {
	if len(g.Policies) > 256 || len(g.Allocations) > 256 {
		return errors.New("guard policy count")
	}
	if g.HelperSocket != "" && (!cleanAbsolute(g.HelperSocket) || len(g.HelperSocket) > 100) {
		return errors.New("guard helper socket must be host-only absolute path")
	}
	for id, a := range g.Allocations {
		ip, err := netip.ParseAddr(a.IP)
		if !nodeID.MatchString(id) || err != nil || !ip.Is4() || !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() || a.Port < 1 || a.Port > 65535 || a.Protocol != "tcp" && a.Protocol != "udp" {
			return errors.New("guard external mapping requires exact public IPv4/protocol/game-port tuple")
		}
		for _, port := range []int{22, 80, 443, 2022, 8080} {
			if port == a.Port {
				return errors.New("guard mapping uses protected infrastructure port")
			}
		}
	}
	for id, p := range g.Policies {
		if !nodeID.MatchString(id) {
			return errors.New("invalid guard server ID")
		}
		if e := p.Validate(); e != nil {
			return e
		}
		if p.Action == "kick" {
			return errors.New("host guard supports log/block; configure runtime kick locally")
		}
		g.Policies[id] = p
	}
	return nil
}
