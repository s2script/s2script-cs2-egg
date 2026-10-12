// SPDX-License-Identifier: GPL-3.0-or-later
package plugins

import "github.com/s2script/s2script-cs2-egg/internal/catalog"

// Builtin is a plugin shipped in the matched S2Script release.
type Builtin = catalog.Builtin

// Bundled exposes the shared, dependency-free catalog to plugin consumers.
func Bundled() []Builtin { return catalog.Bundled() }
