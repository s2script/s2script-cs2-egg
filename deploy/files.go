// SPDX-License-Identifier: GPL-3.0-or-later
// Package deploy provides the same reviewed unit for staging and documentation.
package deploy

import _ "embed"

//go:embed s2node.service
var Unit []byte

//go:embed s2node-helper@.service
var HelperUnit []byte
