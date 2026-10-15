// SPDX-License-Identifier: GPL-3.0-or-later

package install

import (
	"github.com/K4ryuu/CS2-Egg-Go/internal/node/nodeconfig"
	"github.com/K4ryuu/CS2-Egg-Go/internal/node/passwords"
	"testing"
)

func TestPasswordsRequireExplicitInstallerOptIn(t *testing.T) {
	cfg := nodeconfig.Default()
	m := passwords.New(nil, nil)
	if moduleEnabledDefault(true, cfg, m) {
		t.Fatal("fresh installation enables password module")
	}
	cfg.SetSection("passwords", map[string]bool{"enabled": true})
	if !moduleEnabledDefault(false, cfg, m) {
		t.Fatal("existing explicit opt-in lost")
	}
}
