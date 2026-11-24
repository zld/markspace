package pipeline

import (
	"fmt"
	"os"
	"path/filepath"

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
		summary := provenance.SegmentSummary{
			ID:         fmt.Sprintf("seg-%03d-kf-%d", i, start.SampleNumber),
			Index:      i,
			StartTicks: start.Time.Ticks,
			EndTicks:   endTicks,
			Timebase:   report.Index.Timebase,
			ByteRange:  br,
			SHA256:     hash,
			Errors:     []*media.Error{},
		}
		for _, e := range report.Index.Errors {
			if e != nil && e.ByteRange != nil && rangesOverlap(br, *e.ByteRange) {
				summary.Errors = append(summary.Errors, e)
			}
		}
		out = append(out, summary)
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

// WriteSegments writes deterministic byte-range fragments to dir and verifies
// each written range against its recorded SHA-256.
func WriteSegments(dir string, mp4Data []byte, segments []provenance.SegmentSummary) ([]string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, media.NewError("pipeline.segment", media.CodeIOError, "cannot create segment directory", err)
	}
	paths := make([]string, 0, len(segments))
	for _, seg := range segments {
		br := seg.ByteRange
		if br.Start < 0 || br.End <= br.Start || br.End > int64(len(mp4Data)) {
			return nil, media.NewError("pipeline.segment", media.CodeInvalidArgument,
				fmt.Sprintf("segment %d has invalid range %d..%d", seg.Index, br.Start, br.End), nil)
		}
		b := mp4Data[br.Start:br.End]
		if got := provenance.HashBytes(b); got != seg.SHA256 {
			return nil, media.NewError("pipeline.segment", media.CodeHashMismatch,
				fmt.Sprintf("segment %d hash changed before write", seg.Index), nil)
		}
		path := filepath.Join(dir, seg.ID+".m4s")
		if err := os.WriteFile(path, b, 0o644); err != nil {
			return nil, media.NewError("pipeline.segment", media.CodeIOError, "cannot write segment", err)
		}
		paths = append(paths, path)
	}
	return paths, nil
}

func rangesOverlap(a, b media.ByteRange) bool {
	return a.Start < b.End && b.Start < a.End
}
