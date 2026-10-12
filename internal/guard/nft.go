// SPDX-License-Identifier: GPL-3.0-or-later
package guard

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/netip"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"time"
)

const Table = "s2script_guard"

type Block struct {
	ServerID   string     `json:"server_id"`
	Allocation Allocation `json:"allocation"`
	Address    netip.Addr `json:"address"`
	Until      time.Time  `json:"until"`
	Reason     string     `json:"reason"`
}
type Rate struct {
	Allocation Allocation `json:"allocation"`
	PerSecond  int        `json:"per_second"`
	Burst      int        `json:"burst"`
	Allowlist  []string   `json:"allowlist"`
}
type Ruleset struct {
	Allocations []Allocation
	Blocks      []Block
	Rates       []Rate
	Allowlist   []string
	Protected   []Allocation
}
type Firewall interface {
	Apply(context.Context, Ruleset) error
	Block(context.Context, Allocation, netip.Addr, time.Time) error
	Unblock(context.Context, Allocation, netip.Addr) error
}

func tuple(a Allocation) string {
	return fmt.Sprintf("ip daddr %s %s dport %d", a.Address.Unmap(), a.Protocol, a.Port)
}
func Render(r Ruleset, now time.Time) (string, error) {
	if len(r.Allocations) > 256 || len(r.Blocks) > 2048 || len(r.Rates) > 16 || len(r.Allowlist) > 128 || len(r.Protected) > 256 {
		return "", errors.New("guard ruleset quota")
	}
	for _, p := range r.Protected {
		if p.Port < 1 || p.Port > 65535 || p.Protocol != "" && p.Protocol != "tcp" && p.Protocol != "udp" || p.Address.IsValid() && !p.Address.Is4() {
			return "", errors.New("invalid protected endpoint")
		}
	}
	allowed := map[Allocation]int{}
	allocs := append([]Allocation(nil), r.Allocations...)
	sort.Slice(allocs, func(i, j int) bool { return tuple(allocs[i]) < tuple(allocs[j]) })
	for i, a := range allocs {
		if e := a.Validate(); e != nil {
			return "", e
		}
		if _, ok := allowed[a]; ok {
			return "", errors.New("duplicate guard allocation")
		}
		for _, p := range r.Protected {
			if p.Port == a.Port && (p.Protocol == "" || p.Protocol == a.Protocol) && (!p.Address.IsValid() || p.Address.IsUnspecified() || p.Address.Unmap() == a.Address.Unmap()) {
				return "", errors.New("allocation overlaps protected endpoint")
			}
		}
		allowed[a] = i
	}
	for _, v := range r.Allowlist {
		p, e := netip.ParsePrefix(v)
		if e != nil || p != p.Masked() {
			return "", errors.New("invalid guard exemption")
		}
	}
	var b strings.Builder
	b.WriteString("add table inet s2script_guard\ndelete table inet s2script_guard\nadd table inet s2script_guard\nadd chain inet s2script_guard ingress { type filter hook prerouting priority -300; policy accept; }\n")
	exempt := make([]string, 0, len(privateRanges)+len(r.Allowlist))
	for _, p := range privateRanges {
		exempt = append(exempt, p.String())
	}
	for _, p := range r.Allowlist {
		v, _ := netip.ParsePrefix(p)
		if v.Addr().Is4() {
			exempt = append(exempt, p)
		}
	}
	fmt.Fprintf(&b, "add rule inet s2script_guard ingress ip saddr { %s } return\n", strings.Join(exempt, ", "))
	for i, a := range allocs {
		fmt.Fprintf(&b, "add set inet s2script_guard blocked_%d { type ipv4_addr; flags timeout; size 256; }\n", i)
		fmt.Fprintf(&b, "add rule inet s2script_guard ingress %s ip saddr @blocked_%d counter drop\n", tuple(a), i)
	}
	counts := map[Allocation]int{}
	seen := map[string]bool{}
	for _, v := range r.Blocks {
		i, ok := allowed[v.Allocation]
		if !ok || !Blockable(v.Address, r.Allowlist) {
			return "", errors.New("block outside authorized allocation/source")
		}
		remaining := v.Until.Sub(now)
		if remaining <= 0 {
			continue
		}
		if remaining > time.Hour {
			return "", errors.New("block expiry exceeds hard ceiling")
		}
		key := tuple(v.Allocation) + v.Address.Unmap().String()
		if seen[key] {
			return "", errors.New("duplicate block")
		}
		seen[key] = true
		counts[v.Allocation]++
		if counts[v.Allocation] > 256 {
			return "", errors.New("allocation block quota")
		}
		milliseconds := remaining.Milliseconds()
		if milliseconds < 1 {
			continue
		}
		fmt.Fprintf(&b, "add element inet s2script_guard blocked_%d { %s timeout %dms }\n", i, v.Address.Unmap(), milliseconds)
	}
	rateSeen := map[Allocation]bool{}
	for i, rate := range r.Rates {
		if _, ok := allowed[rate.Allocation]; !ok || rateSeen[rate.Allocation] || rate.PerSecond < 1 || rate.PerSecond > 1000 || rate.Burst < 1 || rate.Burst > 2000 || len(rate.Allowlist) > 128 {
			return "", errors.New("invalid per-source rate policy")
		}
		rateSeen[rate.Allocation] = true
		for _, v := range rate.Allowlist {
			p, e := netip.ParsePrefix(v)
			if e != nil || p != p.Masked() {
				return "", errors.New("invalid rate exemption")
			}
			if p.Addr().Is4() {
				fmt.Fprintf(&b, "add rule inet s2script_guard ingress %s ip saddr %s return\n", tuple(rate.Allocation), p)
			}
		}
		fmt.Fprintf(&b, "add set inet s2script_guard meter_%d { type ipv4_addr; flags dynamic,timeout; timeout 30s; size 1024; }\n", i)
		fmt.Fprintf(&b, "add counter inet s2script_guard rate_%d\n", i)
		match := "@th,64,32 0xffffffff @th,96,8 { 0x54, 0x55, 0x56 }"
		if rate.Allocation.Protocol == "tcp" {
			match = "tcp flags & (syn|ack) == syn"
		}
		fmt.Fprintf(&b, "add rule inet s2script_guard ingress %s %s update @meter_%d { ip saddr timeout 30s limit rate over %d/second burst %d packets } counter name rate_%d drop\n", tuple(rate.Allocation), match, i, rate.PerSecond, rate.Burst, i)
	}
	if b.Len() > 1<<20 {
		return "", errors.New("guard generated batch bound")
	}
	return b.String(), nil
}

type Runner func(context.Context, string, []string, []byte) ([]byte, error)
type NFT struct {
	Path  string
	Run   Runner
	mu    sync.Mutex
	rules Ruleset
}

func (n *NFT) runner(ctx context.Context, args []string, input []byte) ([]byte, error) {
	run := n.Run
	if run == nil {
		run = execNFT
	}
	limited, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	return run(limited, n.Path, args, input)
}
func (n *NFT) Apply(ctx context.Context, r Ruleset) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.apply(ctx, r)
}
func (n *NFT) apply(ctx context.Context, r Ruleset) error {
	text, e := Render(r, time.Now())
	if e != nil {
		return e
	}
	if _, e = n.runner(ctx, []string{"-c", "-f", "-"}, []byte(text)); e != nil {
		return errors.New("nft syntax/capability check failed; no aggregate-rate fallback")
	}
	if _, e = n.runner(ctx, []string{"-f", "-"}, []byte(text)); e != nil {
		return errors.New("nft apply failed or outcome uncertain")
	}
	n.rules = r
	return nil
}
func (n *NFT) setIndex(a Allocation) (int, error) {
	allocs := append([]Allocation(nil), n.rules.Allocations...)
	sort.Slice(allocs, func(i, j int) bool { return tuple(allocs[i]) < tuple(allocs[j]) })
	for i, v := range allocs {
		if a == v {
			return i, nil
		}
	}
	return 0, errors.New("unknown initialized firewall allocation")
}
func (n *NFT) Block(ctx context.Context, a Allocation, address netip.Addr, until time.Time) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	now := time.Now()
	index, e := n.setIndex(a)
	if e != nil {
		return e
	}
	r := n.rules
	r.Blocks = nil
	for _, b := range n.rules.Blocks {
		if !b.Until.After(now) {
			continue
		}
		if b.Allocation == a && b.Address == address {
			return nil
		}
		r.Blocks = append(r.Blocks, b)
	}
	r.Blocks = append(r.Blocks, Block{Allocation: a, Address: address, Until: until})
	if _, e = Render(r, now); e != nil {
		return e
	}
	ms := until.Sub(now).Milliseconds()
	if ms < 1 {
		return errors.New("expired block")
	}
	input := fmt.Sprintf("add element inet s2script_guard blocked_%d { %s timeout %dms }\n", index, address.Unmap(), ms)
	if _, e = n.runner(ctx, []string{"-f", "-"}, []byte(input)); e != nil {
		return errors.New("nft block failed or outcome uncertain")
	}
	n.rules = r
	return nil
}
func (n *NFT) Unblock(ctx context.Context, a Allocation, address netip.Addr) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	index, e := n.setIndex(a)
	if e != nil {
		return e
	}
	r := n.rules
	r.Blocks = nil
	found := false
	for _, b := range n.rules.Blocks {
		if b.Allocation == a && b.Address == address {
			found = b.Until.After(time.Now())
			continue
		}
		r.Blocks = append(r.Blocks, b)
	}
	if found {
		input := fmt.Sprintf("delete element inet s2script_guard blocked_%d { %s }\n", index, address.Unmap())
		if _, e = n.runner(ctx, []string{"-f", "-"}, []byte(input)); e != nil {
			return errors.New("nft unblock failed or outcome uncertain")
		}
	}
	n.rules = r
	return nil
}
func (n *NFT) Counters(ctx context.Context) ([]byte, error) {
	return n.runner(ctx, []string{"-j", "list", "counters", "inet", Table}, nil)
}

type boundedOutput struct {
	b        bytes.Buffer
	overflow bool
}

func (w *boundedOutput) Write(b []byte) (int, error) {
	n := len(b)
	if w.b.Len()+len(b) > 64<<10 {
		w.overflow = true
		b = b[:max(0, 64<<10-w.b.Len())]
	}
	w.b.Write(b)
	return n, nil
}
func execNFT(ctx context.Context, path string, args []string, input []byte) ([]byte, error) {
	cmd := exec.CommandContext(ctx, path, args...)
	cmd.Stdin = bytes.NewReader(input)
	out := &boundedOutput{}
	cmd.Stdout = out
	cmd.Stderr = out
	e := cmd.Run()
	if out.overflow {
		return nil, errors.New("nft output exceeds bound")
	}
	return append([]byte(nil), out.b.Bytes()...), e
}
