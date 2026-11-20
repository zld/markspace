// Package validate cross-checks an HLS output tree against its index and
// against itself. Every recoverable problem is collected: a run reports the
// full failure list rather than stopping at the first item.
package validate

import "hlscheck/internal/model"

// Stable failure codes emitted by the checker; scripts may switch on them.
const (
	CodeMissingSegment    = "MISSING_SEGMENT"
	CodeDuplicateSegment  = "DUPLICATE_SEGMENT"
	CodeOrderMismatch     = "ORDER_MISMATCH"
	CodeDanglingReference = "DANGLING_REFERENCE"
	CodeDurationDeviation = "DURATION_DEVIATION"
	CodeBoundaryMisalign  = "BOUNDARY_MISALIGNED"
	CodeCountMismatch     = "SEGMENT_COUNT_MISMATCH"
	CodePlaylistDrift     = "PLAYLIST_DRIFT"
	CodeCCError           = "CONTINUITY_ERROR"
	CodeIndexUnreadable   = "INDEX_UNREADABLE"
)

// Report is the complete validation outcome.
type Report struct {
	OK         bool                    `json:"ok"`
	Renditions []model.RenditionResult `json:"renditions"`
	Findings   []model.Finding         `json:"findings"`
}
