// Package dejima holds build-time embedded assets shared by the binaries.
package dejima

import "embed"

// IslandImageFS is the island image build context — the image/ directory of
// this source tree — embedded so dejimad can (re)build dejima/island:latest
// on demand without a source checkout on the host (`dejima image build`).
//
//go:embed all:image
var IslandImageFS embed.FS
