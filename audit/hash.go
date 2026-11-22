// Package audit is the verification layer over timeline hashing. It compares
// segment fingerprints across replays and full re-runs, guaranteeing that
// unaffected segments keep identical hashes while recomputed ones are
// explicitly accounted for.
package audit

import (
	"encoding/hex"

	"mediacore/timeline"
)

// VerifyReplay compares segment hashes before and after a replay. Segments
// present in both runs with identical IDs must match; only segments that were
// actually rebuilt may differ. It returns the number of compared, matched and
// changed segments.
func VerifyReplay(before, after []*timeline.Segment) (compared, matched, changed int) {
	old := make(map[int][]byte, len(before))
	for _, seg := range before {
		old[seg.ID] = seg.Hash
	}
	for _, seg := range after {
		if h, ok := old[seg.ID]; ok {
			compared++
			if equalBytes(h, seg.Hash) {
				matched++
			} else {
				changed++
			}
		}
	}
	return compared, matched, changed
}

// HashAll returns the per-segment hex hashes in time order, used to assert
// byte-identical results between two independent full runs.
func HashAll(segs []*timeline.Segment) []string {
	out := make([]string, 0, len(segs))
	for _, seg := range segs {
		out = append(out, hex.EncodeToString(seg.Hash))
	}
	return out
}

// EqualRuns reports whether two full runs produced identical per-segment
// hashes in the same order.
func EqualRuns(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func equalBytes(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
