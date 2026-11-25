package segment

import (
	"fmt"
	"sort"
	"time"

	"rtptrace/internal/manifest"
	"rtptrace/internal/timeline"
)

// ByteRange is a deterministic representation sample byte location.
type ByteRange struct {
	InputSample string `json:"input_sample"`
	Offset      int64  `json:"offset"`
	Size        int64  `json:"size"`
}

// RepresentationManifest is one bitrate ladder output.
type RepresentationManifest struct {
	Representation string      `json:"representation"`
	BitrateBPS     int         `json:"bitrate_bps"`
	TrackID        uint32      `json:"track_id"`
	ByteRanges     []ByteRange `json:"byte_ranges"`
}

// Mapping is the per-input-sample row shared by evidence output.
type Mapping struct {
	InputSample         string `json:"input_sample"`
	TrackID             uint32 `json:"track_id"`
	PresentationStart   string `json:"presentation_start"`
	SegmentID           string `json:"segment_id"`
	Representation      string `json:"representation"`
	TimeErrorTicks      int64  `json:"time_error_ticks"`
	ByteError           int64  `json:"byte_error"`
	PresentationTicks   int64  `json:"presentation_ticks"`
	Timescale           uint32 `json:"timescale"`
	SegmentIndex        int    `json:"segment_index"`
	SegmentTargetStart  string `json:"segment_target_start"`
	BoundaryState       string `json:"boundary_state"`
	DecodeStartSample   uint32 `json:"decode_start_sample"`
	ByteStart           int64  `json:"byte_start"`
	ByteEnd             int64  `json:"byte_end"`
	RepresentationBytes int64  `json:"representation_bytes"`
}

// AlignedReport contains all representation manifests and sample mappings.
type AlignedReport struct {
	SegmentCount      int                      `json:"segment_count"`
	Boundaries        []TrackBoundaries        `json:"boundaries"`
	Representations   []RepresentationManifest `json:"representations"`
	Mappings          []Mapping                `json:"mappings"`
	MaxTimeErrorTicks int64                    `json:"max_time_error_ticks"`
	TotalByteError    int64                    `json:"byte_error_total"`
	FailedBoundaries  []Boundary               `json:"failed_boundaries"`
}

// Align builds deterministic byte ranges and maps every sample to every rep.
func Align(m *manifest.Manifest, segmentDuration time.Duration, trackOrder []uint32) *AlignedReport {
	segmentCount := int(m.Duration / segmentDuration)
	if m.Duration%segmentDuration != 0 {
		segmentCount++
	}
	order := append([]uint32(nil), trackOrder...)
	if len(order) == 0 {
		for _, t := range m.Tracks {
			order = append(order, t.ID)
		}
		sort.Slice(order, func(i, j int) bool { return order[i] < order[j] })
	}
	byID := make(map[uint32]manifest.Track, len(m.Tracks))
	boundaryByTrack := make(map[uint32]TrackBoundaries, len(m.Tracks))
	for _, t := range m.Tracks {
		byID[t.ID] = t
		bp := SelectTrackBoundaries(t, segmentDuration, segmentCount)
		boundaryByTrack[t.ID] = bp
	}

	repOrder := m.Representations
	out := &AlignedReport{SegmentCount: segmentCount, Mappings: []Mapping{}, Representations: []RepresentationManifest{}, FailedBoundaries: []Boundary{}}
	for _, trackID := range order {
		t := byID[trackID]
		bp := boundaryByTrack[trackID]
		out.Boundaries = append(out.Boundaries, bp)
		for _, b := range bp.Boundaries {
			if b.State != BoundaryAligned {
				out.FailedBoundaries = append(out.FailedBoundaries, b)
			}
		}
		for _, rep := range repOrder {
			rm := RepresentationManifest{Representation: rep.ID, BitrateBPS: rep.Bitrate, TrackID: trackID, ByteRanges: []ByteRange{}}
			var offset int64
			for _, s := range t.Samples {
				inputID := InputID(trackID, s.Index)
				size := representationSize(rep.Bitrate, trackID, s)
				br := ByteRange{InputSample: inputID, Offset: offset, Size: size}
				rm.ByteRanges = append(rm.ByteRanges, br)

				pt := PresentationTime(t, s)
				presentationTicks := s.PresentationTicks
				if t.MovieTimescale == 0 {
					presentationTicks = s.PresentationTicks - t.StartTimeTicks
				}
				segIdx := SegmentIndex(pt, segmentDuration, segmentCount)
				segID := SegmentID(segIdx, segmentCount)
				if segIdx < 0 || segIdx >= segmentCount {
					segID = "seg-outside"
				}
				boundary := boundaryAt(bp, segIdx)
				errTicks := int64(0)
				state := BoundaryAligned
				decode := uint32(0)
				if boundary != nil {
					errTicks = BoundaryErrorTicks(TargetTime(boundary.Index, segmentDuration), boundary.PresentationTimescale)
					state = boundary.State
					decode = boundary.DecodeStartSample
				}
				expectedOffset := verifyOffset(rm.ByteRanges, inputID)
				byteError := offset - expectedOffset
				m := Mapping{
					InputSample: inputID, TrackID: trackID, PresentationStart: pt.String(),
					PresentationTicks: presentationTicks,
					Timescale:         presentationScale(t), SegmentIndex: segIdx, SegmentID: segID,
					Representation: rep.ID, TimeErrorTicks: errTicks, ByteError: byteError,
					SegmentTargetStart: TargetTime(max0(segIdx), segmentDuration).String(),
					BoundaryState:      state, DecodeStartSample: decode,
					ByteStart: offset, ByteEnd: offset + size, RepresentationBytes: size,
				}
				if errTicks > out.MaxTimeErrorTicks {
					out.MaxTimeErrorTicks = errTicks
				}
				out.TotalByteError += byteError
				out.Mappings = append(out.Mappings, m)
				offset += size
			}
			out.Representations = append(out.Representations, rm)
		}
	}
	// Failed boundaries can be appended twice if multiple samples share a bad
	// segment; deduplicate while preserving deterministic order.
	out.FailedBoundaries = dedupeBoundaries(out.FailedBoundaries)
	return out
}

func presentationScale(t manifest.Track) uint32 {
	if t.MovieTimescale != 0 {
		return t.MovieTimescale
	}
	return t.SourceTimescale
}

// InputID is the stable source sample locator.
func InputID(trackID, sample uint32) string {
	return fmt.Sprintf("track-%d/sample-%d", trackID, sample)
}

// SegmentIndex floors exact presentation time by the segment duration.
func SegmentIndex(pt timeline.Rational, segment time.Duration, count int) int {
	seg := timeline.Rat(segment.Nanoseconds(), int64(time.Second))
	qNum := pt.Num * seg.Den
	qDen := pt.Den * seg.Num
	if qDen == 0 {
		return 0
	}
	q := qNum / qDen
	rem := qNum % qDen
	if rem != 0 && qNum < 0 {
		q--
	}
	idx := int(q)
	if idx < 0 {
		return -1
	}
	if idx >= count {
		return count
	}
	return idx
}

// BoundaryErrorTicks returns 0 for an integral target and 1 for a fractional
// destination tick, which is the conservative one-tick/audio-sample bound.
func BoundaryErrorTicks(target timeline.Rational, scale uint32) int64 {
	num := target.Num * int64(scale)
	den := target.Den
	rem := num % den
	if rem < 0 {
		rem += den
	}
	if rem == 0 {
		return 0
	}
	return 1
}

func representationSize(bitrate int, trackID uint32, s manifest.Sample) int64 {
	// Deterministic ladder-sized byte count. The low bitrate retains at least
	// a small positive payload; keyframes remain larger than delta samples.
	factor := int64(bitrate) / 800_000
	if factor < 1 {
		factor = 1
	}
	base := s.SizeBytes
	if base <= 0 {
		base = 16
	}
	hashBias := int64((trackID*131 + s.Index*17) % 31)
	size := base*factor + hashBias
	if s.Sync {
		size += 64 * factor
	}
	return size
}

func verifyOffset(ranges []ByteRange, inputID string) int64 {
	var expected int64
	for _, br := range ranges {
		if br.InputSample == inputID {
			return expected
		}
		expected = br.Offset + br.Size
	}
	return -1
}

func boundaryAt(bp TrackBoundaries, index int) *Boundary {
	for i := range bp.Boundaries {
		if bp.Boundaries[i].Index == index {
			return &bp.Boundaries[i]
		}
	}
	return nil
}

func max0(v int) int {
	if v < 0 {
		return 0
	}
	return v
}

func dedupeBoundaries(in []Boundary) []Boundary {
	seen := make(map[string]bool, len(in))
	out := make([]Boundary, 0, len(in))
	for _, b := range in {
		key := fmt.Sprintf("%d:%d:%s", b.TrackID, b.Index, b.SegmentID)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, b)
	}
	return out
}
