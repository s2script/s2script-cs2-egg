// SPDX-License-Identifier: GPL-3.0-or-later
package guard

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type fixtureFirewall struct {
	mu                     sync.Mutex
	rules                  Ruleset
	failBlock, failUnblock bool
	calls                  int
}

func (f *fixtureFirewall) Apply(_ context.Context, r Ruleset) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rules = r
	return nil
}
func (f *fixtureFirewall) Block(_ context.Context, a Allocation, ip netip.Addr, u time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.failBlock {
		return errors.New("fixture failure")
	}
	f.rules.Blocks = append(f.rules.Blocks, Block{Allocation: a, Address: ip, Until: u})
	return nil
}
func (f *fixtureFirewall) Unblock(_ context.Context, a Allocation, ip netip.Addr) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failUnblock {
		return errors.New("fixture failure")
	}
	var kept []Block
	for _, b := range f.rules.Blocks {
		if b.Allocation != a || b.Address != ip {
			kept = append(kept, b)
		}
	}
	f.rules.Blocks = kept
	return nil
}
func engineFixture(t *testing.T) (*Engine, *fixtureFirewall) {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("ordinary nonroot helper fixture")
	}
	p := fixturePolicy()
	p.Action = "block"
	p.Delegated = true
	p.GuardPolicy.Validate()
	c := HelperConfig{Socket: filepath.Join(t.TempDir(), "guard.sock"), StateDir: t.TempDir(), NFT: "/usr/sbin/nft", RuntimeUID: uint32(os.Geteuid()), NodeUID: uint32(os.Geteuid() + 1), AccessGID: os.Getegid(), Policies: map[string]Policy{"one": p}}
	os.Chmod(c.StateDir, 0700)
	fw := &fixtureFirewall{}
	e := &Engine{Config: c, Firewall: fw}
	if err := e.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	return e, fw
}
func TestHelperDoesNotCallFailedBlockSuccessfulOnRetry(t *testing.T) {
	e, fw := engineFixture(t)
	fw.failBlock = true
	r := Request{Operation: "block", ServerID: "one", Address: netip.MustParseAddr("8.8.8.8"), Until: time.Now().Add(time.Minute), Provenance: "console", Reason: "reconnect_stuck"}
	first, err := e.Handle(context.Background(), r)
	if err == nil || first.State != "uncertain" {
		t.Fatal(first, err)
	}
	second, err := e.Handle(context.Background(), r)
	if err == nil || second.State != "uncertain" || fw.calls != 1 {
		t.Fatal("uncertain retry claimed success or reapplied", second, err, fw.calls)
	}
}
func TestUnblockFailureCannotResurrectBlockAfterRestart(t *testing.T) {
	e, fw := engineFixture(t)
	r := Request{Operation: "block", ServerID: "one", Address: netip.MustParseAddr("8.8.8.8"), Until: time.Now().Add(time.Minute), Provenance: "manual", Reason: "manual"}
	if _, err := e.Handle(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	fw.failUnblock = true
	r.Operation = "unblock"
	if _, err := e.Handle(context.Background(), r); err == nil {
		t.Fatal("expected uncertain removal")
	}
	nextFW := &fixtureFirewall{}
	next := &Engine{Config: e.Config, Firewall: nextFW}
	if err := next.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(nextFW.rules.Blocks) != 0 {
		t.Fatal("restart resurrected explicitly removed block")
	}
}
func TestReloadUsesRemainingExpiryAndCurrentAllowlist(t *testing.T) {
	e, _ := engineFixture(t)
	until := time.Now().Add(10 * time.Second)
	r := Request{Operation: "block", ServerID: "one", Address: netip.MustParseAddr("8.8.8.8"), Until: until, Provenance: "manual", Reason: "manual"}
	if _, err := e.Handle(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	fw := &fixtureFirewall{}
	next := &Engine{Config: e.Config, Firewall: fw}
	if err := next.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(fw.rules.Blocks) != 1 || !fw.rules.Blocks[0].Until.Equal(until) {
		t.Fatal("expiry renewed", fw.rules.Blocks)
	}
	next.Config.Allowlist = []string{"8.8.8.0/24"}
	if err := next.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(fw.rules.Blocks) != 0 {
		t.Fatal("newly exempt source kept blocked")
	}
}
func TestWrongHelperPeerRejectedWhileIdle(t *testing.T) {
	e, _ := engineFixture(t)
	l, err := net.Listen("unix", e.Config.Socket)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- ServeHelper(ctx, l, e) }()
	defer func() { cancel(); l.Close(); <-done }()
	_, err = Call(ctx, e.Config.Socket, uint32(os.Geteuid()), Request{Operation: "info"})
	if err == nil {
		t.Fatal("unauthorized peer accepted on idle helper")
	}
}
func TestNFTSingleBlockPreservesMetersAndNeverUsesShell(t *testing.T) {
	var inputs []string
	n := &NFT{Path: "/fixed/nft", Run: func(_ context.Context, path string, args []string, in []byte) ([]byte, error) {
		if path != "/fixed/nft" || len(args) < 2 || args[len(args)-2] != "-f" || args[len(args)-1] != "-" {
			t.Fatal("nonfixed invocation", path, args)
		}
		inputs = append(inputs, string(in))
		return nil, nil
	}}
	a := fixturePolicy().Allocation
	r := Ruleset{Allocations: []Allocation{a}, Rates: []Rate{{Allocation: a, PerSecond: 20, Burst: 40}}}
	if err := n.Apply(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	if err := n.Block(context.Background(), a, netip.MustParseAddr("8.8.8.8"), time.Now().Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	last := inputs[len(inputs)-1]
	if strings.Contains(last, "delete table") || !strings.Contains(last, "add element inet s2script_guard blocked_0") {
		t.Fatal("single block reset meters", last)
	}
	if !strings.Contains(inputs[0], "size 1024") || !strings.Contains(inputs[0], "@th,96,8 { 0x54, 0x55, 0x56 }") {
		t.Fatal("unbounded or generic UDP rate", inputs[0])
	}
}

func TestUnrelatedSuccessDoesNotClearUncertainKernelState(t *testing.T) {
	e, fw := engineFixture(t)
	r := Request{Operation: "block", ServerID: "one", Address: netip.MustParseAddr("8.8.8.8"), Until: time.Now().Add(time.Minute), Provenance: "manual", Reason: "manual"}
	fw.failBlock = true
	if _, err := e.Handle(context.Background(), r); err == nil {
		t.Fatal("expected uncertain block")
	}
	fw.failBlock = false
	other := r
	other.Address = netip.MustParseAddr("9.9.9.9")
	e.Handle(context.Background(), other)
	info, err := e.Handle(context.Background(), Request{Operation: "info"})
	if err != nil || info.State != "kernel_state_uncertain" {
		t.Fatal("unrelated operation erased uncertainty", info, err)
	}
	if _, err := e.Handle(context.Background(), r); err == nil {
		t.Fatal("unknown old block claimed applied")
	}
}
func TestCancelledGuardRequestCannotMutate(t *testing.T) {
	e, fw := engineFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := e.Handle(ctx, Request{Operation: "block", ServerID: "one", Address: netip.MustParseAddr("8.8.8.8"), Until: time.Now().Add(time.Minute), Provenance: "manual", Reason: "manual"})
	if err == nil || fw.calls != 0 || len(e.blocks) != 0 {
		t.Fatal("cancelled request mutated", err, fw.calls, e.blocks)
	}
}
