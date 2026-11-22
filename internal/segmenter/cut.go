// Package segmenter cuts normalized tracks into keyframe-aligned segments and
// emits multi-bitrate manifests whose per-segment hashes match their content.
package segmenter

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"

	"archivist/internal/model"
	"archivist/internal/timeline"
)

// Segment is one cut of one representation. Byte ranges reference the source
// container; no media is re-encoded or re-written.
type Segment struct {
	Index        int
	TrackID      uint32
	Representation string
	StartDTS     uint64 // native track timescale
	EndDTS       uint64
	CommonStart  uint64 // common media clock
	CommonEnd    uint64
	ByteRanges   []ByteRange
	SampleCount  int
	Hash         string // sha256 over the concatenated segment bytes
}

// ByteRange is one contiguous [start, end) slice of the source file.
type ByteRange struct {
	Offset uint64
	Length uint64
}

// CutPlan binds aligned boundaries to the concrete samples of each track.
type CutPlan struct {
	CommonTimescale uint64
	SegmentMS       uint32
	Segments        map[uint32][]*Segment // track id -> ordered segments
	BoundaryCount   int
}

// Representation describes one bitrate layer of the asset.
type Representation struct {
	Label   string
	Bitrate uint32
	TrackID uint32
	Clock   timeline.Clock
}

// Cut walks each track's samples and assigns them to the aligned boundaries.
// A segment only starts on a real keyframe; if a boundary cannot be honored
// on a track the plan is returned with an error and no partial segments.
func Cut(
	tracks []*model.Track,
	reps []Representation,
	alignment *timeline.AlignmentResult,
	segmentMS uint32,
	data []byte,
) (*CutPlan, error) {
	if !alignment.Aligned {
		return nil, model.AtTrack(model.StageSegment, model.ErrNoCommonKeyframe, 0,
			"refusing to cut on unaligned keyframe boundaries")
	}
	plan := &CutPlan{
		CommonTimescale: alignment.CommonTimescale,
		SegmentMS:       segmentMS,
		Segments:        map[uint32][]*Segment{},
		BoundaryCount:   len(alignment.Boundaries),
	}
	for _, rep := range reps {
		tr := findTrack(tracks, rep.TrackID)
		if tr == nil {
			return nil, model.AtTrack(model.StageSegment, model.ErrCorruptSampleTable, rep.TrackID,
				"representation has no track")
		}
		segs, err := cutTrack(tr, rep, alignment, data)
		if err != nil {
			return nil, err
		}
		if err := verifyDurationMonotonic(segs); err != nil {
			return nil, err
		}
		plan.Segments[rep.TrackID] = segs
	}
	return plan, nil
}

func cutTrack(
	tr *model.Track,
	rep Representation,
	alignment *timeline.AlignmentResult,
	data []byte,
) ([]*Segment, error) {
	nativePoints := make([]uint64, len(alignment.Boundaries))
	for i, b := range alignment.Boundaries {
		nativePoints[i] = b.PerTrackTicks[tr.ID]
	}
	var segs []*Segment
	keyIdx := 0
	for i := 0; i < len(nativePoints); i++ {
		start := nativePoints[i]
		var end uint64
		if i+1 < len(nativePoints) {
			end = nativePoints[i+1]
		} else {
			last := tr.Samples[len(tr.Samples)-1]
			end = last.DTS + uint64(last.Duration)
		}
		seg := &Segment{
			Index:          i,
			TrackID:        tr.ID,
			Representation: rep.Label,
			StartDTS:       start,
			EndDTS:         end,
			CommonStart:    alignment.Boundaries[i].CommonTicks,
		}
		if i+1 < len(alignment.Boundaries) {
			seg.CommonEnd = alignment.Boundaries[i+1].CommonTicks
		} else {
			seg.CommonEnd = alignment.CommonTimescale * end / uint64(tr.Timescale)
		}
		// Collect samples with DTS in [start, end).
		var covered []model.Sample
		for keyIdx < len(tr.Samples) && tr.Samples[keyIdx].DTS < start {
			keyIdx++
		}
		j := keyIdx
		for j < len(tr.Samples) && tr.Samples[j].DTS < end {
			covered = append(covered, tr.Samples[j])
			j++
		}
		if len(covered) == 0 {
			return nil, model.AtTrack(model.StageSegment, model.ErrNoCommonKeyframe, tr.ID,
				fmt.Sprintf("segment %d covers no samples", i))
		}
		if !covered[0].IsKeyframe {
			return nil, model.AtTrack(model.StageSegment, model.ErrNoCommonKeyframe, tr.ID,
				fmt.Sprintf("segment %d does not start on a keyframe", i))
		}
		seg.SampleCount = len(covered)
		seg.ByteRanges = coalesceRanges(covered)
		seg.Hash = hashSamples(covered, data)
		keyIdx = j
		segs = append(segs, seg)
	}
	return segs, nil
}

// coalesceRanges merges adjacent sample byte spans into minimal ranges.
func coalesceRanges(samples []model.Sample) []ByteRange {
	if len(samples) == 0 {
		return nil
	}
	var out []ByteRange
	cur := ByteRange{Offset: samples[0].Offset, Length: uint64(samples[0].Size)}
	for _, s := range samples[1:] {
		end := cur.Offset + cur.Length
		if s.Offset == end {
			cur.Length += uint64(s.Size)
			continue
		}
		out = append(out, cur)
		cur = ByteRange{Offset: s.Offset, Length: uint64(s.Size)}
	}
	out = append(out, cur)
	return out
}

// hashSamples hashes the exact concatenated media bytes of a segment.
func hashSamples(samples []model.Sample, data []byte) string {
	h := sha256.New()
	for _, s := range samples {
		end := s.Offset + uint64(s.Size)
		if end > uint64(len(data)) {
			return "" // validated earlier; empty hash surfaces as mismatch
		}
		h.Write(data[s.Offset:end])
	}
	return hex.EncodeToString(h.Sum(nil))
}

func verifyDurationMonotonic(segs []*Segment) error {
	for i := 1; i < len(segs); i++ {
		if segs[i].StartDTS <= segs[i-1].StartDTS {
			return model.AtSample(model.StageSegment, model.ErrNonMonotonic, segs[i].TrackID,
				uint32(i), model.TimeRange{Start: segs[i-1].StartDTS, End: segs[i].StartDTS})
		}
		if segs[i].CommonEnd <= segs[i].CommonStart {
			return model.AtSample(model.StageSegment, model.ErrNonMonotonic, segs[i].TrackID,
				uint32(i), model.TimeRange{Start: segs[i].CommonStart, End: segs[i].CommonEnd})
		}
	}
	return nil
}

func findTrack(tracks []*model.Track, id uint32) *model.Track {
	for _, tr := range tracks {
		if tr.ID == id {
			return tr
		}
	}
	return nil
}
