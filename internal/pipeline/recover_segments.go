package pipeline

import (
	"crypto/sha256"
	"encoding/hex"
	"os"

	"mediapipeline/internal/demux"
)

func segmentStates(scan *demux.FragmentScan) []SegmentState {
	states := make([]SegmentState, 0, len(scan.Segments))
	for _, seg := range scan.Segments {
		d := sha256.Sum256(seg.Payload)
		states = append(states, SegmentState{
			ID: seg.ID, MoofOffset: seg.MoofOffset, Size: seg.Size,
			Digest: hex.EncodeToString(d[:]), Committed: true,
		})
	}
	return states
}

func countDuplicates(states []SegmentState) int {
	seen := make(map[string]int, len(states))
	dups := 0
	for _, s := range states {
		key := s.Digest
		seen[key]++
		if seen[key] > 1 {
			dups++
		}
	}
	return dups
}

func containsAll(committed, recovered []SegmentState) bool {
	index := make(map[string]SegmentState, len(committed))
	for _, s := range committed {
		index[s.Digest] = s
	}
	for _, s := range recovered {
		if _, ok := index[s.Digest]; !ok {
			return false
		}
	}
	return true
}

func committedOutputMatches(path, wantDigest string) (string, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", false
	}
	got := OutputPayloadDigest(data)
	if wantDigest != "" && got != wantDigest {
		return "", false
	}
	return got, true
}
