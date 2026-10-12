// SPDX-License-Identifier: GPL-3.0-or-later
package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

type Allocation struct {
	IP   string `json:"ip"`
	Port int    `json:"port"`
}
type NodeServer struct {
	HelperSocket string     `json:"helper_socket,omitempty"`
	ID           string     `json:"id"`
	Volume       string     `json:"volume"`
	PeerUID      uint32     `json:"peer_uid"`
	AccessGID    int        `json:"access_gid"`
	Allocation   Allocation `json:"allocation"`
}
type NodeLimits struct {
	Pending int `json:"pending"`
	Records int `json:"records"`
	Runs    int `json:"runs"`
}
type SharedNode struct {
	Enabled      bool   `json:"enabled"`
	Root         string `json:"root"`
	SteamCMD     string `json:"steamcmd"`
	ReserveBytes uint64 `json:"reserve_bytes"`
}
type Node struct {
	Shared        SharedNode   `json:"shared"`
	SchemaVersion int          `json:"schema_version"`
	ID            string       `json:"id"`
	StateDir      string       `json:"state_dir"`
	AdminSocket   string       `json:"admin_socket"`
	Servers       []NodeServer `json:"servers"`
	Limits        NodeLimits   `json:"limits"`
	Webhook       Webhook      `json:"webhook"`
}

// Format uses the existing Webhook JSON redactor even for nested settings.
func (n Node) Format(f fmt.State, verb rune) { b, _ := json.Marshal(n); _, _ = f.Write(b) }

var nodeID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,63}$`)

func LoadNode(path string) (Node, error) {
	var n Node
	f, e := os.Open(path)
	if e != nil {
		return n, e
	}
	defer f.Close()
	b, e := io.ReadAll(io.LimitReader(f, MaxNodeConfig+1))
	if e != nil || len(b) > MaxNodeConfig {
		return n, errors.New("node configuration exceeds 1 MiB")
	}
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if d.Decode(&n) != nil || d.Decode(new(any)) != io.EOF {
		return n, errors.New("invalid node configuration")
	}
	e = n.Validate()
	return n, e
}

const MaxNodeConfig = 1 << 20

func (n *Node) Validate() error {
	if n.SchemaVersion != 1 || !nodeID.MatchString(n.ID) {
		return errors.New("invalid node schema or identity")
	}
	if n.AdminSocket == "" {
		n.AdminSocket = "/run/s2node/admin.sock"
	}
	if !cleanAbsolute(n.StateDir) || !cleanAbsolute(n.AdminSocket) || len(n.AdminSocket) > 100 {
		return errors.New("node state and admin socket must be absolute clean paths")
	}
	if len(n.Servers) > 256 {
		return errors.New("at most 256 servers supported")
	}
	if n.Limits.Pending == 0 {
		n.Limits.Pending = 1024
	}
	if n.Limits.Records == 0 {
		n.Limits.Records = 8192
	}
	if n.Limits.Runs == 0 {
		n.Limits.Runs = 4096
	}
	if n.Limits.Pending < 1 || n.Limits.Pending > 1024 || n.Limits.Records < n.Limits.Pending || n.Limits.Records > 8192 || n.Limits.Runs < 1 || n.Limits.Runs > 4096 {
		return errors.New("invalid node delivery limits")
	}
	if n.Shared.Enabled {
		if n.Shared.Root == "" {
			n.Shared.Root = "/srv/s2node/shared"
		}
		if !cleanAbsolute(n.Shared.Root) || !cleanAbsolute(n.Shared.SteamCMD) || overlaps(n.Shared.Root, n.StateDir) || overlaps(n.Shared.Root, filepath.Dir(n.AdminSocket)) {
			return errors.New("shared root/SteamCMD must be absolute and separate from node private paths")
		}
		if n.Shared.ReserveBytes == 0 {
			n.Shared.ReserveBytes = 64 << 30
		}
	}
	ids := map[string]bool{}
	allocations := map[string]bool{}
	for i, s := range n.Servers {
		if !nodeID.MatchString(s.ID) || ids[s.ID] || !cleanAbsolute(s.Volume) || s.AccessGID < 0 || len(filepath.Join(s.Volume, "egg/s2node.sock")) > 107 || net.ParseIP(s.Allocation.IP) == nil || s.Allocation.Port < 1 || s.Allocation.Port > 65535 {
			return errors.New("invalid or duplicate registered server")
		}
		if n.Shared.Enabled && overlaps(s.Volume, n.Shared.Root) {
			return errors.New("shared store overlaps private volume")
		}
		if s.HelperSocket != "" {
			if !cleanAbsolute(s.HelperSocket) || len(s.HelperSocket) > 100 || overlaps(s.HelperSocket, s.Volume) || n.Shared.Enabled && overlaps(s.HelperSocket, n.Shared.Root) {
				return errors.New("helper socket must be a host-only absolute path outside volumes/store")
			}
			for _, other := range n.Servers {
				if overlaps(s.HelperSocket, other.Volume) {
					return errors.New("helper socket overlaps a registered volume")
				}
			}
		}
		ids[s.ID] = true
		a := net.JoinHostPort(s.Allocation.IP, strconv.Itoa(s.Allocation.Port))
		if allocations[a] {
			return errors.New("duplicate game allocation")
		}
		allocations[a] = true
		for _, p := range []string{n.StateDir, filepath.Dir(n.AdminSocket)} {
			if overlaps(s.Volume, p) {
				return errors.New("node private paths overlap a server volume")
			}
		}
		for _, other := range n.Servers[:i] {
			if overlaps(s.Volume, other.Volume) {
				return errors.New("registered volumes overlap")
			}
		}
	}
	return nil
}
func cleanAbsolute(s string) bool { return filepath.IsAbs(s) && s == filepath.Clean(s) && s != "/" }
func overlaps(a, b string) bool {
	return a == b || strings.HasPrefix(a, b+"/") || strings.HasPrefix(b, a+"/")
}
