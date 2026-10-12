// SPDX-License-Identifier: GPL-3.0-or-later
// Console grammar researched/adapted from K4ryuu/CS2-Egg-Go guard.go at
// 2ceeea15825b52856a7ef157e8bd70ffb7ab2cbf; see NOTICE. These are source-derived
// fixtures, not a claim of acceptance against a live CS2 build.
package guard

import (
	"net/netip"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

var connect = regexp.MustCompile(`^CServerSideClientBase::Connect\( name='([^'\r\n]{0,128})', userid=([1-9][0-9]{0,8}),[^'\r\n]*chan->addr=([0-9.]+):([0-9]{1,5})\)?$`)
var signon = regexp.MustCompile(`^Client ([1-9][0-9]{0,8}) '([^'\r\n]{0,128})' signon state SIGNONSTATE_[A-Z]+ -> SIGNONSTATE_([A-Z]+)$`)
var reconnect = regexp.MustCompile(`^(?:SV:  )?Forcing client reconnect [A-Za-z0-9_ .:-]{0,128} for client '([^'\r\n]{1,128})'$`)
var move = regexp.MustCompile(`^\[([^\]\r\n]{1,128})\] sent command that failed delta decode$`)
var badPacket = regexp.MustCompile(`^(?:\[(?:Data|packet)\] )?Ignored bad ([0-9.]+):[0-9]{1,5} (Invalid [A-Za-z_]+ byte.*|Stray data packet from host with no connection.*)$`)
var c2s = regexp.MustCompile(`^Receiving C2S_CONNECT [^\r\n]{0,256} from ([0-9.]+):[0-9]{1,5}$`)
var rcon = regexp.MustCompile(`^Banning ([0-9.]+) for rcon hacking attempts$`)

func Observe(at time.Time, serverID, line string) []Finding {
	if len(line) > 8192 || strings.ContainsAny(line, "\r\n\x00") || strings.HasPrefix(line, "[All Chat]") || strings.HasPrefix(line, "[Team Chat]") {
		return nil
	}
	f := Finding{At: at, ServerID: serverID, Evidence: line[:min(len(line), 1024)], Provenance: "uncorrelated-console"}
	if m := connect.FindStringSubmatch(line); m != nil {
		uid, _ := strconv.Atoi(m[2])
		port, _ := strconv.Atoi(m[4])
		ip, e := netip.ParseAddr(m[3])
		if e != nil || !ip.Is4() || port < 1 || port > 65535 {
			return nil
		}
		f.Rule = "connect"
		f.Name = m[1]
		f.UID = uid
		f.Address = ip
		return []Finding{f}
	}
	if m := signon.FindStringSubmatch(line); m != nil {
		f.Rule = "signon"
		f.UID, _ = strconv.Atoi(m[1])
		f.Name = m[2]
		f.State = m[3]
		return []Finding{f}
	}
	if m := reconnect.FindStringSubmatch(line); m != nil {
		f.Rule = "reconnect_stuck"
		f.Name = m[1]
		return []Finding{f}
	}
	if m := move.FindStringSubmatch(line); m != nil {
		f.Rule = "move_not_joined"
		f.Name = m[1]
		return []Finding{f}
	}
	for _, v := range []struct {
		re   *regexp.Regexp
		rule string
	}{{badPacket, "malformed_packet"}, {c2s, "connect_flood"}, {rcon, "rcon_bruteforce"}} {
		if m := v.re.FindStringSubmatch(line); m != nil {
			ip, e := netip.ParseAddr(m[1])
			if e != nil || !ip.Is4() {
				return nil
			}
			f.Address = ip
			f.Rule = v.rule
			if v.rule == "malformed_packet" && strings.HasPrefix(m[2], "Stray") {
				f.Rule = "stray_no_connection"
			}
			return []Finding{f}
		}
	}
	return nil
}

type binding struct {
	UID                     int
	Name                    string
	Address                 netip.Addr
	Connected               time.Time
	Paired, Full, Ambiguous bool
}
type counter struct {
	Seconds       [60]int64
	Values        [60]int
	Last, Emitted time.Time
}
type detectorState struct {
	run, session string
	clients      map[int]binding
	counters     map[string]*counter
}
type Detector struct {
	mu      sync.Mutex
	servers map[string]*detectorState
}

func NewDetector() *Detector { return &Detector{servers: map[string]*detectorState{}} }
func (d *Detector) Observe(at time.Time, id, run, session, line string, p Policy) []Finding {
	if run == "" || session == "" || p.GuardPolicy.Validate() != nil {
		return nil
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	st := d.servers[id]
	if st == nil || st.run != run || st.session != session {
		if st == nil && len(d.servers) >= 256 {
			return nil
		}
		st = &detectorState{run: run, session: session, clients: map[int]binding{}, counters: map[string]*counter{}}
		d.servers[id] = st
	}
	ttl := time.Duration(p.TTLSeconds) * time.Second
	for uid, c := range st.clients {
		if at.Sub(c.Connected) > ttl {
			delete(st.clients, uid)
		}
	}
	for key, c := range st.counters {
		if at.Sub(c.Last) > time.Duration(max(p.WindowSeconds, p.CooldownSeconds, 30))*time.Second {
			delete(st.counters, key)
		}
	}
	totalClients, totalCounters := 0, 0
	for _, s := range d.servers {
		totalClients += len(s.clients)
		totalCounters += len(s.counters)
	}
	var out []Finding
	for _, f := range Observe(at, id, line) {
		f.RunID = run
		f.SessionID = session
		switch f.Rule {
		case "connect":
			old, exists := st.clients[f.UID]
			if !exists && (len(st.clients) >= 256 || totalClients >= 2048) {
				continue
			}
			ambiguous := exists
			if ambiguous {
				old.Ambiguous = true
				st.clients[f.UID] = old
				continue
			}
			st.clients[f.UID] = binding{UID: f.UID, Name: f.Name, Address: f.Address, Connected: at}
			continue
		case "signon":
			c, ok := st.clients[f.UID]
			if !ok || c.Name != f.Name || c.Ambiguous {
				continue
			}
			if at.Sub(c.Connected) <= time.Duration(p.PairSeconds)*time.Second {
				c.Paired = true
			}
			c.Full = c.Full || f.State == "FULL"
			st.clients[f.UID] = c
			continue
		}
		key := f.Rule + "/" + f.Address.String() + "/" + f.Name
		threshold, window := 20, p.WindowSeconds
		switch f.Rule {
		case "reconnect_stuck":
			threshold = p.ReconnectThreshold
		case "move_not_joined":
			threshold = p.MoveThreshold
		case "stray_no_connection":
			threshold = 50
		case "rcon_bruteforce":
			threshold = 3
			window = 30
		}
		c := st.counters[key]
		if c == nil {
			if len(st.counters) >= 1024 || totalCounters >= 8192 {
				continue
			}
			c = &counter{}
			st.counters[key] = c
		}
		sec := at.Unix()
		slot := int(sec % 60)
		if slot < 0 {
			continue
		}
		if c.Seconds[slot] != sec {
			c.Seconds[slot] = sec
			c.Values[slot] = 0
		}
		c.Values[slot] = min(c.Values[slot]+1, 1000)
		c.Last = at
		count := 0
		for i, v := range c.Values {
			if c.Seconds[i] <= sec && sec-c.Seconds[i] < int64(window) {
				count += v
			}
		}
		if count < threshold || !c.Emitted.IsZero() && at.Sub(c.Emitted) < time.Duration(p.CooldownSeconds)*time.Second {
			continue
		}
		c.Emitted = at
		f.Count = count
		if f.Rule == "reconnect_stuck" || f.Rule == "move_not_joined" {
			var picked binding
			matches := 0
			for _, client := range st.clients {
				if client.Name == f.Name {
					picked = client
					matches++
				}
			}
			addresses := 0
			for _, client := range st.clients {
				if client.Address == picked.Address {
					addresses++
				}
			}
			if matches == 1 && addresses == 1 && picked.Paired && !picked.Full && !picked.Ambiguous && at.Sub(picked.Connected) >= time.Duration(p.GraceSeconds)*time.Second {
				f.UID = picked.UID
				f.Address = picked.Address
				f.Correlated = true
				f.Provenance = "container-console-correlated"
			}
		}
		out = append(out, f)
	}
	return out
}
func (d *Detector) Valid(a Action, at time.Time, p Policy) bool {
	p.GuardPolicy.Validate()
	if at.Before(a.At) || at.Sub(a.At) > time.Duration(p.FreshSeconds)*time.Second {
		return false
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	s := d.servers[a.ServerID]
	if s == nil || s.run != a.RunID || s.session != a.SessionID {
		return false
	}
	c, ok := s.clients[a.UID]
	if !ok || c.Ambiguous || c.Full || !c.Paired || c.Address != a.Address || c.Connected.After(a.At) || at.Sub(c.Connected) < time.Duration(p.GraceSeconds)*time.Second || at.Sub(c.Connected) > time.Duration(p.TTLSeconds)*time.Second {
		return false
	}
	count := 0
	for _, v := range s.clients {
		if v.Name == c.Name || v.Address == c.Address {
			count++
		}
	}
	return count == 1
}

func (d *Detector) Reset() { d.mu.Lock(); defer d.mu.Unlock(); d.servers = map[string]*detectorState{} }
