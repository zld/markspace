package pipeline

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"math"
	"sort"

	"mediapipe/internal/clock"
	"mediapipe/internal/ingest"
	"mediapipe/internal/manifest"
)

// SegmentResult pairs an ingest record with its normalized audio.
type SegmentResult struct {
	Record  ingest.Record
	Samples []float64 // normalized PCM at the output rate; nil for gaps
	Frames  int64
}

// TrackMerge is the deterministic, manifest-ordered merge of one track.
type TrackMerge struct {
	TrackID    string
	Records    []ingest.Record // one per manifest slot, in manifest order
	Samples    []float64       // concatenated audio, gaps zero-filled
	Frames     int64
	Violations []string
}

// MergeTrack assembles one track in manifest order regardless of the
// completion order of the worker pool. Every manifest slot must have
// exactly one record; gaps are filled with silence so the output timeline
// always matches the manifest timeline.
func MergeTrack(pl *manifest.Playlist, results []SegmentResult, channels int) (*TrackMerge, error) {
	tm := &TrackMerge{TrackID: pl.TrackID}
	byKey := map[string]SegmentResult{}
	for _, r := range results {
		k := keyOf(r.Record.Ref)
		if _, dup := byKey[k]; dup {
			return nil, &clock.StageError{Track: pl.TrackID, Stage: clock.StageMerge, Op: "dedupe",
				Err: fmt.Errorf("duplicate result for segment %d %s", r.Record.Ref.Index, r.Record.Ref.URI)}
		}
		byKey[k] = r
	}
	var prevEnd clock.Ticks = -1
	for _, ref := range pl.Refs {
		if prevEnd >= 0 && ref.Interval.Start < prevEnd {
			tm.Violations = append(tm.Violations,
				fmt.Sprintf("interval regression at index %d: %d < %d", ref.Index, ref.Interval.Start, prevEnd))
		}
		prevEnd = ref.Interval.End

		r, ok := byKey[keyOf(ref)]
		if !ok {
			return nil, &clock.StageError{Track: pl.TrackID, Stage: clock.StageMerge, Op: "coverage",
				Err: fmt.Errorf("no record for segment %d %s", ref.Index, ref.URI)}
		}
		delete(byKey, keyOf(ref))
		tm.Records = append(tm.Records, r.Record)

		want := int64(ref.Interval.Dur()) // 1 tick == 1 output frame at 48 kHz
		switch r.Record.Status {
		case ingest.OK, ingest.Recovered:
			got := r.Frames
			if got != want {
				tm.Violations = append(tm.Violations,
					fmt.Sprintf("frame count at index %d: got %d want %d", ref.Index, got, want))
			}
			tm.Samples = append(tm.Samples, fitFrames(r.Samples, want, channels)...)
			tm.Frames += want
		default: // Missing or Unrecoverable: silence preserves the timeline
			tm.Samples = append(tm.Samples, make([]float64, want*int64(channels))...)
			tm.Frames += want
		}
	}
	if len(byKey) != 0 {
		return nil, &clock.StageError{Track: pl.TrackID, Stage: clock.StageMerge, Op: "coverage",
			Err: fmt.Errorf("%d results without manifest slots", len(byKey))}
	}
	return tm, nil
}

// fitFrames adjusts a sample buffer to exactly want frames per channel by
// truncating or zero-padding, keeping merge output explainable.
func fitFrames(in []float64, want int64, channels int) []float64 {
	n := want * int64(channels)
	out := make([]float64, n)
	copy(out, in)
	if int64(len(in)) > n {
		return out[:n:n]
	}
	return out
}

func keyOf(ref manifest.SegmentRef) string {
	return fmt.Sprintf("%d/%s", ref.Index, ref.URI)
}

// AudioHash hashes interleaved float64 samples bit-exactly.
func AudioHash(samples []float64) [32]byte {
	h := sha256.New()
	var buf [8]byte
	for _, s := range samples {
		binary.LittleEndian.PutUint64(buf[:], math.Float64bits(s))
		h.Write(buf[:])
	}
	var sum [32]byte
	copy(sum[:], h.Sum(nil))
	return sum
}

// ManifestHash hashes the canonical merge manifest: one line per record
// with track, index, interval, status and reason, in manifest order.
func ManifestHash(tracks []*TrackMerge) [32]byte {
	h := sha256.New()
	for _, tm := range tracks {
		for _, r := range tm.Records {
			fmt.Fprintf(h, "%s|%d|%d|%d|%s|%s\n",
				r.Ref.TrackID, r.Ref.Index, r.Ref.Interval.Start, r.Ref.Interval.End,
				r.Status, r.Reason)
		}
	}
	var sum [32]byte
	copy(sum[:], h.Sum(nil))
	return sum
}

// SortTracks orders track merges by track ID for deterministic hashing.
func SortTracks(tracks []*TrackMerge) {
	sort.Slice(tracks, func(i, j int) bool { return tracks[i].TrackID < tracks[j].TrackID })
}
