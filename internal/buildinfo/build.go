// SPDX-License-Identifier: GPL-3.0-or-later
// Package buildinfo exposes linker-provided release identity. Development builds
// deliberately have no release baseline for automatic replacement.
package buildinfo

var Version = "dev"
var Revision = "unknown"
