// SPDX-License-Identifier: GPL-3.0-or-later

package conntrace

import (
	"net/netip"
	"regexp"
	"strings"
)

var (
	channel  = regexp.MustCompile(`^\[[A-Za-z0-9_. -]{1,20}\]\s*`)
	accept   = regexp.MustCompile(`^Accepting Steam Net connection (?:#([0-9]+) )?[^@\r\n]*@([^\s]+)`)
	c2s      = regexp.MustCompile(`^Receiving C2S_CONNECT \[[^\]\r\n]*\] from ([^\s]+)`)
	connect  = regexp.MustCompile(`^CServerSideClientBase::Connect\( name='(.*)', userid=([0-9]+), fake=0, [^\r\n]*chan->addr=([^\s]+)`)
	signon   = regexp.MustCompile(`^Client ([0-9]+) '(.*)' signon state SIGNONSTATE_[A-Z]+ -> SIGNONSTATE_([A-Z]+)\s*$`)
	dropped  = regexp.MustCompile(`^SV:\s+Dropped client '(.*)' from server(?:\([0-9]+\))?:\s*(.*)$`)
	forced   = regexp.MustCompile(`^(?:SV:\s+)?Forcing client reconnect \([^)]*\) for client '(.*)'\s*$`)
	rejected = regexp.MustCompile(`^(?:SV:\s+)?Rejecting connection from (\[[0-9a-fA-F:]+\]:[0-9]+|[0-9.]+:[0-9]+):?\s*(.*)$`)
)

type parsed struct {
	stage, detail, uid, name, signon, transport string
	endpoint                                    netip.AddrPort
}

func endpoint(s string) (netip.AddrPort, bool) {
	ep, err := netip.ParseAddrPort(s)
	if err != nil || ep.Addr().Zone() != "" || ep.Port() == 0 {
		return netip.AddrPort{}, false
	}
	return netip.AddrPortFrom(ep.Addr().Unmap(), ep.Port()), true
}

func parse(line string) (parsed, bool) {
	if strings.HasPrefix(line, "[") {
		line = line[len(channel.FindString(line)):]
	}
	var p parsed
	var address string
	switch {
	case strings.HasPrefix(line, "Accepting Steam Net connection "):
		if m := accept.FindStringSubmatch(line); m != nil {
			p.stage, p.transport, address = "transport_accepted", m[1], m[2]
		}
	case strings.HasPrefix(line, "Receiving C2S_CONNECT "):
		if m := c2s.FindStringSubmatch(line); m != nil {
			p.stage, address = "c2s_connect", m[1]
		}
	case strings.HasPrefix(line, "CServerSideClientBase::Connect("):
		if m := connect.FindStringSubmatch(line); m != nil {
			p.stage, p.name, p.uid, address = "client_connected", m[1], m[2], m[3]
		}
	case strings.HasPrefix(line, "Client "):
		if m := signon.FindStringSubmatch(line); m != nil {
			// Only engine-defined states may enter diagnostic output.
			switch m[3] {
			case "NONE", "CHALLENGE", "CONNECTED", "NEW", "PRESPAWN", "SPAWN", "FULL", "CHANGELEVEL":
				p.stage, p.uid, p.name, p.signon = "signon", m[1], m[2], m[3]
			}
		}
	default:
		if m := dropped.FindStringSubmatch(line); m != nil {
			p.stage, p.name, p.detail = "dropped", m[1], reason(m[2])
		} else if m := forced.FindStringSubmatch(line); m != nil {
			p.stage, p.name = "forced_reconnect", m[1]
		} else if m := rejected.FindStringSubmatch(line); m != nil {
			p.stage, address, p.detail = "rejected", m[1], reason(m[2])
		}
	}
	if p.stage == "" {
		return p, false
	}
	if address != "" {
		var ok bool
		p.endpoint, ok = endpoint(address)
		if !ok {
			return parsed{}, false
		}
	}
	return p, true
}

// Reasons are categories, never user-controlled free text: plugin kick reasons
// can contain credentials, addresses, names or terminal escapes.
func reason(s string) string {
	s = strings.ToUpper(strings.TrimSpace(s))
	switch {
	case strings.HasPrefix(s, "NETWORK_DISCONNECT_TIMEDOUT"):
		return "timeout"
	case strings.HasPrefix(s, "NETWORK_DISCONNECT_DISCONNECT_BY_USER"):
		return "client_disconnect"
	case strings.HasPrefix(s, "NETWORK_DISCONNECT_KICKED"):
		return "kicked"
	case strings.Contains(s, "PASSWORD"):
		return "password"
	case strings.Contains(s, "BANNED"):
		return "banned"
	case strings.Contains(s, "SERVER FULL"):
		return "server_full"
	default:
		return "other_redacted"
	}
}
