package pipeline

import (
	"fmt"

	"example.com/media-kernel/index"
	"example.com/media-kernel/media"
	"example.com/media-kernel/provenance"
)

// BuildSegments hashes byte ranges between verified keyframe boundaries. It
// never invents a boundary from an unverified stss entry or corrupt marker.
func BuildSegments(mp4Data []byte, report *index.MP4Report) ([]provenance.SegmentSummary, error) {
	if report == nil || report.Index == nil {
		return nil, media.NewError("pipeline.segment", media.CodeIndexMissing, "MP4 index is required", nil)
	}
	bounds := report.Index.ConfirmedBoundaries()
	if len(bounds) == 0 {
		return nil, media.NewError("pipeline.segment", media.CodeIndexMissing, "no confirmed keyframe boundaries", nil)
	}

	out := make([]provenance.SegmentSummary, 0, len(bounds))
	for i, start := range bounds {
		endByte := int64(len(mp4Data))
		endTicks := start.Time.Ticks + start.DurationTicks
		if i+1 < len(bounds) {
			next := bounds[i+1]
			endByte = next.ByteRange.Start
			if next.Time.Timebase == start.Time.Timebase {
				endTicks = next.Time.Ticks
			} else if ticks, err := next.Time.Timebase.Convert(next.Time.Ticks, report.Index.Timebase); err == nil {
				endTicks = ticks
			}
		}
		if endByte <= start.ByteRange.Start || endByte > int64(len(mp4Data)) {
			return nil, media.NewError("pipeline.segment", media.CodeInvalidArgument,
				fmt.Sprintf("segment %d has invalid byte end %d from start %d",
					i, endByte, start.ByteRange.Start), nil)
		}
		br := media.ByteRange{Start: start.ByteRange.Start, End: endByte}
		hash, err := provenance.HashRange(mp4Data, br)
		if err != nil {
			return nil, err
		}
		out = append(out, provenance.SegmentSummary{
			ID:         fmt.Sprintf("seg-%03d-kf-%d", i, start.SampleNumber),
			Index:      i,
			StartTicks: start.Time.Ticks,
			EndTicks:   endTicks,
			Timebase:   report.Index.Timebase,
			ByteRange:  br,
			SHA256:     hash,
		})
	}
	return out, nil
}

// SegmentHashes returns index-ordered hash strings.
func SegmentHashes(segments []provenance.SegmentSummary) []string {
	out := make([]string, len(segments))
	for i, s := range segments {
		out[i] = s.SHA256
	}
	return out
}
