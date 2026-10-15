// SPDX-License-Identifier: GPL-3.0-or-later

package boot

import (
	"context"
	"io"
	"testing"

	"github.com/K4ryuu/CS2-Egg-Go/internal/egg/passwords"
	"github.com/K4ryuu/CS2-Egg-Go/internal/logx"
	"github.com/K4ryuu/CS2-Egg-Go/internal/proto"
)

func TestPasswordEnrollmentFailsClosedButCachedRestartWorksWithoutNode(t *testing.T) {
	env := Env{Root: t.TempDir(), Getenv: func(key string) string {
		if key == "ENABLE_ROTATING_PASSWORD" {
			return "1"
		}
		return ""
	}}
	log := logx.New("test", logx.Info, io.Discard)
	if _, err := preparePassword(context.Background(), env, nil, log); err == nil {
		t.Fatal("unprotected first enrollment was permitted")
	}
	m, _ := passwords.Open(env.Root, true)
	if err := m.Accept(proto.PasswordUpdate{Password: "lantern-meadow-copper", NextAt: "2026-11-15T12:00:00Z"}); err != nil {
		t.Fatal(err)
	}
	loaded, err := preparePassword(context.Background(), env, nil, log)
	if err != nil || !loaded.Ready() {
		t.Fatal("cached node-outage restart failed", err)
	}
	if !nodeHello(env).RotatingPassword {
		t.Fatal("opt-in not sent in hello")
	}
	env.Getenv = func(string) string { return "0" }
	if nodeHello(env).RotatingPassword {
		t.Fatal("disabled server advertised opt-in")
	}
	if _, err := preparePassword(context.Background(), env, nil, log); err != nil {
		t.Fatal("opt-out failed", err)
	}
}
