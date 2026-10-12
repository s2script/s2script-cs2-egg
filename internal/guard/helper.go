// SPDX-License-Identifier: GPL-3.0-or-later
package guard

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"github.com/s2script/s2script-cs2-egg/internal/artifact"
	"github.com/s2script/s2script-cs2-egg/internal/protocol"
	"github.com/s2script/s2script-cs2-egg/internal/storeio"
	"github.com/s2script/s2script-cs2-egg/internal/volumehelper"
	"io"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"syscall"
	"time"
)

type HelperConfig struct {
	Socket        string            `json:"socket"`
	StateDir      string            `json:"state_dir"`
	NFT           string            `json:"nft"`
	NodeUID       uint32            `json:"node_uid"`
	RuntimeUID    uint32            `json:"runtime_uid"`
	AccessGID     int               `json:"access_gid"`
	ContainerUIDs []uint32          `json:"container_uids"`
	Policies      map[string]Policy `json:"policies"`
	Allowlist     []string          `json:"allowlist"`
	Protected     []Allocation      `json:"protected"`
	Rates         []Rate            `json:"rates"`
}
type Request struct {
	Operation  string     `json:"operation"`
	ServerID   string     `json:"server_id,omitempty"`
	Address    netip.Addr `json:"address,omitempty"`
	Until      time.Time  `json:"until,omitempty"`
	Provenance string     `json:"provenance,omitempty"`
	Reason     string     `json:"reason,omitempty"`
	Offset     int        `json:"offset,omitempty"`
}
type CounterValue struct {
	Name           string
	Packets, Bytes uint64
}
type Reply struct {
	Error        string         `json:"error,omitempty"`
	State        string         `json:"state"`
	Until        time.Time      `json:"until,omitempty"`
	Blocks       []Block        `json:"blocks,omitempty"`
	Next         int            `json:"next,omitempty"`
	Rates        []CounterValue `json:"rates,omitempty"`
	At           time.Time      `json:"at"`
	Capabilities []string       `json:"capabilities,omitempty"`
}
type Engine struct {
	Config   HelperConfig
	Firewall Firewall
	mu       sync.Mutex
	blocks   []Block
	healthy  bool
}

func (c *HelperConfig) Validate() error {
	if len(c.Policies) > 256 || len(c.ContainerUIDs) > 256 || c.RuntimeUID == 0 || c.NodeUID == c.RuntimeUID || c.AccessGID < 0 {
		return errors.New("guard helper needs distinct nonroot UID and fixed peer/group")
	}
	for _, uid := range c.ContainerUIDs {
		if uid == c.RuntimeUID {
			return errors.New("guard helper UID must differ from container UIDs")
		}
	}
	for _, p := range []string{c.Socket, c.StateDir, c.NFT} {
		if !filepath.IsAbs(p) || filepath.Clean(p) != p || p == "/" {
			return errors.New("fixed absolute guard helper paths required")
		}
	}
	if len(c.Socket) > 100 {
		return errors.New("guard socket path too long")
	}
	for id, p := range c.Policies {
		if id == "" || len(id) > 64 || p.ServerID != id {
			return errors.New("fixed guard server identity required")
		}
		if e := p.GuardPolicy.Validate(); e != nil {
			return e
		}
		if p.Action != "log" && p.Action != "block" {
			return errors.New("host helper policy supports log or block")
		}
		c.Policies[id] = p
	}
	_, e := Render(c.rules(nil), time.Now())
	return e
}
func (c HelperConfig) rules(blocks []Block) Ruleset {
	r := Ruleset{Blocks: blocks, Rates: c.Rates, Allowlist: c.Allowlist, Protected: c.Protected}
	for _, p := range c.Policies {
		if p.Enabled && p.Action == "block" {
			r.Allocations = append(r.Allocations, p.Allocation)
		}
	}
	return r
}
func operatorFile(path string, executable bool) error {
	d, e := artifact.OpenDirectory(filepath.Dir(path))
	if e != nil {
		return e
	}
	defer d.Close()
	f, e := artifact.OpenRegular(d, filepath.Base(path), syscall.O_RDONLY, 0)
	if e != nil {
		return e
	}
	i, e := f.Stat()
	f.Close()
	if e != nil {
		return e
	}
	if i.Sys().(*syscall.Stat_t).Uid != 0 || i.Mode().Perm()&022 != 0 || executable && i.Mode().Perm()&0111 == 0 {
		return errors.New("guard executable/configuration must be root-owned and not group/world writable")
	}
	for p := filepath.Dir(path); p != "/"; p = filepath.Dir(p) {
		d, e := artifact.OpenDirectory(p)
		if e != nil {
			return e
		}
		i, e := d.Stat()
		d.Close()
		if e != nil || i.Sys().(*syscall.Stat_t).Uid != 0 || i.Mode().Perm()&022 != 0 {
			return errors.New("guard policy/executable ancestors must be root-owned and nonwritable")
		}
	}
	return nil
}
func LoadHelperConfig(path string) (HelperConfig, error) {
	var c HelperConfig
	if filepath.Dir(path) != "/etc/s2node/guard" {
		return c, errors.New("guard policy must be in /etc/s2node/guard")
	}
	if e := operatorFile(path, false); e != nil {
		return c, e
	}
	b, e := storeio.Read(filepath.Dir(path), filepath.Base(path), 64<<10)
	if e != nil {
		return c, e
	}
	if e = protocol.Decode(b, &c); e != nil {
		return c, e
	}
	if e = c.Validate(); e != nil {
		return c, e
	}
	if e = operatorFile(c.NFT, true); e != nil {
		return c, e
	}
	binary, e := os.Executable()
	if e != nil {
		return c, e
	}
	if e = operatorFile(binary, true); e != nil {
		return c, e
	}
	return c, nil
}
func (e *Engine) Start(ctx context.Context) error {
	if err := e.Config.Validate(); err != nil {
		return err
	}
	d, err := artifact.OpenDirectory(e.Config.StateDir)
	if err != nil {
		return err
	}
	i, err := d.Stat()
	d.Close()
	if err != nil || i.Mode().Perm() != 0700 || i.Sys().(*syscall.Stat_t).Uid != e.Config.RuntimeUID {
		return errors.New("guard state must be helper-owned0700")
	}
	b, err := storeio.Read(e.Config.StateDir, "blocks.json", 4<<20)
	if err == nil {
		if err = protocol.Decode(b, &e.blocks); err != nil {
			return err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if len(e.blocks) > 2048 {
		return errors.New("guard saved block quota")
	}
	now := time.Now()
	var allowed []Block
	for _, v := range e.blocks {
		p, ok := e.Config.Policies[v.ServerID]
		if !ok || !p.Enabled || p.Action != "block" || p.Allocation != v.Allocation || !v.Until.After(now) || !Blockable(v.Address, append(append([]string{}, e.Config.Allowlist...), p.Allowlist...)) {
			continue
		}
		ceiling := now.Add(time.Duration(p.MaxBlockSeconds) * time.Second)
		if v.Until.After(ceiling) {
			v.Until = ceiling
		}
		allowed = append(allowed, v)
	}
	if err = e.Firewall.Apply(ctx, e.Config.rules(allowed)); err != nil {
		return err
	}
	e.blocks = allowed
	e.healthy = true
	return storeio.Save(e.Config.StateDir, "blocks.json", e.blocks)
}
func (e *Engine) Handle(ctx context.Context, r Request) (Reply, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return Reply{}, err
	}
	now := time.Now()
	reply := Reply{At: now.UTC()}
	switch r.Operation {
	case "info":
		reply.State = "ready"
		if !e.healthy {
			reply.State = "kernel_state_uncertain"
		}
		reply.Capabilities = []string{"guard-v1", "block", "unblock", "list", "rates", "refresh"}
		return reply, nil
	case "list":
		if r.Offset < 0 || r.Offset > 2048 {
			return reply, errors.New("invalid block page")
		}
		var live []Block
		for _, v := range e.blocks {
			if v.Until.After(now) && (r.ServerID == "" || v.ServerID == r.ServerID) {
				live = append(live, v)
			}
		}
		sort.Slice(live, func(i, j int) bool { return live[i].Until.Before(live[j].Until) })
		if r.Offset > len(live) {
			return reply, errors.New("invalid block offset")
		}
		end := min(r.Offset+32, len(live))
		reply.Blocks = live[r.Offset:end]
		if end < len(live) {
			reply.Next = end
		}
		reply.State = "saved_authorized_expiries"
		if !e.healthy {
			reply.State = "kernel_state_uncertain"
		}
		return reply, nil
	case "rates":
		n, ok := e.Firewall.(*NFT)
		if !ok {
			return reply, errors.New("kernel counters unavailable")
		}
		b, err := n.Counters(ctx)
		if err != nil {
			return reply, errors.New("kernel rates unavailable; not zero")
		}
		var data struct {
			NFTables []struct {
				Counter *struct {
					Name           string `json:"name"`
					Packets, Bytes uint64
				} `json:"counter"`
			} `json:"nftables"`
		}
		if json.Unmarshal(b, &data) != nil || len(data.NFTables) > 256 {
			return reply, errors.New("invalid bounded kernel counter reply")
		}
		for _, v := range data.NFTables {
			if v.Counter != nil {
				reply.Rates = append(reply.Rates, CounterValue{v.Counter.Name, v.Counter.Packets, v.Counter.Bytes})
			}
		}
		reply.State = "kernel_counters"
		return reply, nil
	case "refresh":
		var live []Block
		for _, v := range e.blocks {
			if v.Until.After(now) {
				live = append(live, v)
			}
		}
		if err := e.Firewall.Apply(ctx, e.Config.rules(live)); err != nil {
			e.healthy = false
			return reply, err
		}
		e.blocks = live
		e.healthy = true
		reply.State = "applied"
		return reply, storeio.Save(e.Config.StateDir, "blocks.json", e.blocks)
	case "block", "unblock":
	default:
		return reply, errors.New("invalid guard helper operation")
	}
	p, ok := e.Config.Policies[r.ServerID]
	if !ok || !p.Enabled || p.Action != "block" {
		return reply, errors.New("host enforcement not authorized for server")
	}
	if r.Provenance != "manual" && r.Provenance != "console" || r.Provenance == "console" && !p.Delegated {
		return reply, errors.New("reported enforcement not delegated")
	}
	address := r.Address.Unmap()
	if !Blockable(address, append(append([]string{}, e.Config.Allowlist...), p.Allowlist...)) {
		return reply, errors.New("protected or invalid source")
	}
	if r.Reason != "manual" && r.Reason != "reconnect_stuck" && r.Reason != "move_not_joined" {
		return reply, errors.New("unsupported enforcement evidence")
	}
	var live []Block
	var previous *Block
	count := 0
	for _, v := range e.blocks {
		if !v.Until.After(now) {
			continue
		}
		if v.ServerID == r.ServerID {
			count++
		}
		if v.ServerID == r.ServerID && v.Address == address {
			copy := v
			previous = &copy
			continue
		}
		live = append(live, v)
	}
	if r.Operation == "block" {
		if previous != nil {
			if !e.healthy {
				return Reply{State: "uncertain", Until: previous.Until, At: now}, errors.New("kernel block state uncertain; inspect/refresh helper")
			}
			reply.State = "already_applied"
			reply.Until = previous.Until
			return reply, nil
		}
		until := r.Until
		ceiling := now.Add(time.Duration(p.MaxBlockSeconds) * time.Second)
		if until.After(ceiling) {
			until = ceiling
		}
		if !until.After(now) {
			return reply, errors.New("expired block request")
		}
		if count >= 256 || len(live) >= 2048 {
			return reply, errors.New("live block quota; no eviction")
		}
		v := Block{ServerID: r.ServerID, Allocation: p.Allocation, Address: address, Until: until, Reason: r.Reason}
		live = append(live, v)
		if err := storeio.Save(e.Config.StateDir, "blocks.json", live); err != nil {
			return reply, err
		}
		e.blocks = live
		if err := e.Firewall.Block(ctx, p.Allocation, address, until); err != nil {
			e.healthy = false
			return Reply{State: "uncertain", Until: until, At: now}, err
		}
		reply.Until = until
	} else {
		if previous == nil {
			if !e.healthy {
				return Reply{State: "uncertain", At: now}, errors.New("kernel removal state uncertain; inspect/refresh helper")
			}
			reply.State = "absent_or_expired"
			return reply, nil
		}
		if err := storeio.Save(e.Config.StateDir, "blocks.json", live); err != nil {
			return reply, err
		}
		e.blocks = live
		if err := e.Firewall.Unblock(ctx, p.Allocation, address); err != nil {
			e.healthy = false
			return Reply{State: "uncertain", At: now}, err
		}
	}
	// Only a complete refresh or restart reconciles earlier uncertain operations.
	reply.State = "applied"
	return reply, nil
}

const helperFrame = 16 << 10

func readFrame(c net.Conn, v any) error {
	var h [4]byte
	if _, e := io.ReadFull(c, h[:]); e != nil {
		return e
	}
	n := binary.BigEndian.Uint32(h[:])
	if n == 0 || n > helperFrame {
		return errors.New("guard helper frame bound")
	}
	b := make([]byte, n)
	if _, e := io.ReadFull(c, b); e != nil {
		return e
	}
	return protocol.Decode(b, v)
}
func writeFrame(c net.Conn, v any) error {
	b, e := json.Marshal(v)
	if e != nil {
		return e
	}
	if len(b) > helperFrame {
		return errors.New("guard helper reply bound")
	}
	var h [4]byte
	binary.BigEndian.PutUint32(h[:], uint32(len(b)))
	if _, e = c.Write(h[:]); e != nil {
		return e
	}
	_, e = c.Write(b)
	return e
}
func RunHelper(ctx context.Context, c HelperConfig) error {
	if c.Socket != "/run/s2node-guard/helper.sock" || c.StateDir != "/var/lib/s2node-guard" {
		return errors.New("guard service requires fixed host runtime/state roots")
	}
	if uint32(os.Geteuid()) != c.RuntimeUID {
		return errors.New("guard helper requires its configured nonroot UID")
	}
	engine := &Engine{Config: c, Firewall: &NFT{Path: c.NFT}}
	if e := engine.Start(ctx); e != nil {
		return e
	}
	l, e := net.Listen("unix", c.Socket)
	if e != nil {
		return e
	}
	defer l.Close()
	defer os.Remove(c.Socket)
	if e = os.Chown(c.Socket, -1, c.AccessGID); e != nil {
		return e
	}
	if e = os.Chmod(c.Socket, 0660); e != nil {
		return e
	}
	return ServeHelper(ctx, l, engine)
}
func ServeHelper(ctx context.Context, l net.Listener, e *Engine) error {
	stop := context.AfterFunc(ctx, func() { l.Close() })
	defer stop()
	slots := make(chan struct{}, 8)
	var wg sync.WaitGroup
	defer wg.Wait()
	for {
		c, err := l.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		select {
		case slots <- struct{}{}:
		default:
			c.Close()
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-slots }()
			defer c.Close()
			uid, err := volumehelper.Peer(c)
			if err != nil || uid != e.Config.NodeUID {
				return
			}
			c.SetDeadline(time.Now().Add(2 * time.Second))
			request, cancel := context.WithTimeout(ctx, 2*time.Second)
			defer cancel()
			stop := context.AfterFunc(request, func() { c.Close() })
			defer stop()
			var wire struct {
				Version int     `json:"version"`
				Request Request `json:"request"`
			}
			if readFrame(c, &wire) != nil || wire.Version != 1 {
				return
			}
			reply, err := e.Handle(request, wire.Request)
			if err != nil {
				reply.Error = err.Error()
			}
			writeFrame(c, reply)
		}()
	}
}
func Call(ctx context.Context, socket string, uid uint32, r Request) (Reply, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	c, e := volumehelper.Dial(ctx, socket, uid)
	if e != nil {
		return Reply{}, e
	}
	defer c.Close()
	stop := context.AfterFunc(ctx, func() { c.Close() })
	defer stop()
	c.SetDeadline(time.Now().Add(2 * time.Second))
	if e = writeFrame(c, struct {
		Version int     `json:"version"`
		Request Request `json:"request"`
	}{1, r}); e != nil {
		return Reply{}, e
	}
	var reply Reply
	if e = readFrame(c, &reply); e != nil {
		return reply, e
	}
	if reply.Error != "" {
		return reply, errors.New(reply.Error)
	}
	return reply, nil
}

func (c HelperConfig) Preview() (string, error) {
	if e := c.Validate(); e != nil {
		return "", e
	}
	return Render(c.rules(nil), time.Now())
}
