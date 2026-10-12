// SPDX-License-Identifier: GPL-3.0-or-later
package config

import "testing"

func TestGuardRejectsInvalidExternalMappings(t *testing.T) {
	for _, a := range []GuardAllocation{{IP: "0.0.0.0", Port: 27015, Protocol: "udp"}, {IP: "203.0.113.10", Port: 22, Protocol: "udp"}, {IP: "203.0.113.10", Port: 27015, Protocol: "UDP;drop"}, {IP: "10.0.0.1", Port: 27015, Protocol: "udp"}} {
		g := GuardNode{Allocations: map[string]GuardAllocation{"one": a}}
		if g.Validate() == nil {
			t.Fatal("invalid external mapping accepted", a)
		}
	}
}
