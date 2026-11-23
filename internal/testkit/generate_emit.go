// Package testkit builds deterministic fixtures. This file orders and
// interleaves generated blocks into clusters.
package testkit

import (
	"sort"
)

// pendingBlock is one block queued for interleaved emission.
type pendingBlock struct {
	track   TrackSpec
	idx     int
	timeMs  uint32 // stored container counter (may wrap)
	trueMs  int64  // unwrapped time, used for ordering and cluster offsets
	key     bool
	payload []byte
}

// stableSortBlocks sorts by timestamp then track number, stable on
// generation order.
func stableSortBlocks(b []pendingBlock) {
	sort.SliceStable(b, func(i, j int) bool {
		if b[i].trueMs != b[j].trueMs {
			return b[i].trueMs < b[j].trueMs
		}
		return b[i].track.Number < b[j].track.Number
	})
}
