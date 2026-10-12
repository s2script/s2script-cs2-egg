// SPDX-License-Identifier: GPL-3.0-or-later
package artifact

import (
	"context"
	"github.com/s2script/s2script-cs2-egg/internal/protocol"
)

// SelectedFetcher transports an independently selected verified archive. The
// caller retains extraction, compatibility, policy and transaction ownership.
type SelectedFetcher interface {
	FetchArtifact(context.Context, protocol.ArtifactSelection, string) error
}

// SelectedValidator applies node fleet/source policy even when a caller can reuse
// an already installed or locally cached artifact without transporting its bytes.
type SelectedValidator interface {
	ValidateArtifact(context.Context, protocol.ArtifactSelection) error
}
