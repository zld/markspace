package pipeline

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"

	"archivecheck/internal/mp4meta"
	"archivecheck/internal/recovery"
	"archivecheck/internal/sample"
)

// metadataResult is the outcome of building and re-reading the MP4 metadata
// document that accompanies the recovered archive.
type metadataResult struct {
	Bytes      int
	SHA256     string
	Tracks     int
	Mismatches []string
}

// buildMetadata encodes an ISO-BMFF metadata document for the recovered
// archive and reads it back. Samples are included only when they lie wholly
// inside a recovered segment: metadata must not claim frames the recovery
// refused to certify. The decoded keyframe timeline is then compared with the
// recovered anchors, so a layer that disagrees with the shared timeline is
// reported rather than silently averaged away.
func buildMetadata(archive sample.Archive, results []recovery.Result) (metadataResult, error) {
	byLayer := map[string]recovery.Result{}
	for _, r := range results {
		byLayer[r.Layer] = r
	}
	mp4doc := mp4meta.Document{}
	for i, l := range archive.Layers {
		r := byLayer[l.Name]
		var times, sizes []int64
		var keys []int
		for _, f := range l.Frames() {
			if !covered(r.Segments, f.Offset, f.Offset+f.Length) {
				continue
			}
			times = append(times, f.Time)
			sizes = append(sizes, f.Length)
			if f.Key {
				keys = append(keys, len(times)-1)
			}
		}
		if len(times) == 0 {
			return metadataResult{}, fmt.Errorf("pipeline: layer %s has no certified samples for metadata", l.Name)
		}
		mp4doc.Tracks = append(mp4doc.Tracks, mp4meta.Track{
			ID: uint32(i + 1), Timescale: l.Timescale, Times: times, Bytes: sizes, Keyframes: keys,
		})
	}

	raw, err := mp4meta.Encode(mp4doc)
	if err != nil {
		return metadataResult{}, fmt.Errorf("pipeline: mp4 metadata encode: %w", err)
	}
	metas, err := mp4meta.Decode(raw)
	if err != nil {
		return metadataResult{}, fmt.Errorf("pipeline: mp4 metadata decode: %w", err)
	}
	if len(metas) != len(archive.Layers) {
		return metadataResult{}, fmt.Errorf("pipeline: metadata has %d tracks for %d layers", len(metas), len(archive.Layers))
	}
	sum := sha256.Sum256(raw)
	out := metadataResult{Bytes: len(raw), SHA256: hex.EncodeToString(sum[:]), Tracks: len(metas)}

	for i, l := range archive.Layers {
		r := byLayer[l.Name]
		meta := metas[i]
		if meta.Timescale != l.Timescale {
			return metadataResult{}, fmt.Errorf("pipeline: metadata layer %s timescale %d != %d", l.Name, meta.Timescale, l.Timescale)
		}
		// Every certified segment begins at a keyframe the metadata must also
		// declare. Anchors whose following interval was retained as a gap are
		// deliberately absent from both artifacts.
		for _, seg := range r.Segments {
			want := seg.StartMs * int64(l.Timescale) / 1000
			found := false
			for _, t := range meta.KeyframeTimes {
				if t == want {
					found = true
					break
				}
			}
			if !found {
				out.Mismatches = append(out.Mismatches,
					fmt.Sprintf("layer=%s recovered_segment_start_ms=%d missing_from_metadata byte_start=%d", l.Name, seg.StartMs, seg.ByteStart))
			}
		}
	}
	return out, nil
}

// covered reports whether [start,end) lies wholly inside one segment.
func covered(segs []recovery.Segment, start, end int64) bool {
	for _, s := range segs {
		if start >= s.ByteStart && end <= s.ByteEnd {
			return true
		}
	}
	return false
}
