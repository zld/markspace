package cli

import (
	"mediakey/internal/manifest"
	"mediakey/internal/report"
)

// emitReport builds the stable JSON document from chunk results and prints it.
func emitReport(assetID string, rotations int, results []manifest.ChunkResult) int {
	rep := report.Build(assetID, rotations, results)
	return emit(rep)
}
