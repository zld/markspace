package demux

import (
	"mediapipeline/internal/clock"
	"mediapipeline/internal/fault"
)

// SampleRef pairs an absolute source offset with its resolved sample.
type SampleRef struct {
	TrackID uint32
	Sample  Sample
}

// ScanSegment re-parses one recovered fragment and resolves its samples
// against the track definitions parsed from moov.  It lets the recovery stage
// rebuild a media model from verified fragments without trusting an index.
func ScanSegment(seg Segment, tracks []Track) ([]SampleRef, error) {
	moofSize := seg.MoofSize
	if moofSize <= 8 || moofSize > seg.Size {
		return nil, fault.New(fault.StageDemux, "mp4", fault.OpTruncated, seg.MoofOffset,
			"recovered segment has inconsistent bounds")
	}
	// Re-parse the children from the original input at absolute offsets.
	// Segment.Payload starts after the mdat header, so the moof must come from
	// the full input slice.
	if seg.src == nil {
		return nil, fault.New(fault.StageDemux, "mp4", fault.OpTruncated, seg.MoofOffset,
			"recovered segment lost its source bytes")
	}
	children, _, err := parseBoxes(seg.src, seg.MoofOffset, seg.MoofOffset+moofSize, "mp4", false)
	if err != nil {
		return nil, err
	}
	if len(children) != 1 {
		return nil, fault.New(fault.StageDemux, "mp4", fault.OpBoxHeader, seg.MoofOffset,
			"recovered moof did not parse")
	}
	moof := children[0]
	layout, err := parseFragment(moof, seg.DataOffset, seg.DataOffset+int64(len(seg.Payload)), nil)
	if err != nil {
		return nil, err
	}
	scale := make(map[uint32]uint32, len(tracks))
	for _, tr := range tracks {
		scale[tr.ID] = tr.Timescale
	}
	var out []SampleRef
	for _, id := range layout.trackIDs {
		ts := scale[id]
		if ts == 0 {
			return nil, fault.New(fault.StageDemux, "mp4", fault.OpDemuxInvalid, seg.MoofOffset,
				"recovered fragment references a track missing from moov")
		}
		for _, s := range layout.samples[id] {
			start := int(s.Offset - seg.DataOffset)
			end := start + int(s.Size)
			if start < 0 || end > len(seg.Payload) {
				return nil, fault.New(fault.StageDemux, "mp4", fault.OpTruncated, seg.MoofOffset,
					"recovered sample outside its mdat payload")
			}
			s.Data = seg.Payload[start:end]
			s.DTS = clock.New(s.DTS.Ticks, ts)
			s.CTS = clock.New(s.CTS.Ticks, ts)
			out = append(out, SampleRef{TrackID: id, Sample: s})
		}
	}
	return out, nil
}
