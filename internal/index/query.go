package index

import (
	"sort"

	"mediakernel/internal/clock"
	"mediakernel/internal/wav"
)

// LocateBlock returns the entry whose block covers frameIndex, i.e. the
// anchor with the greatest FrameStart <= frameIndex.
func (x *Index) LocateBlock(frameIndex int64) (BlockEntry, error) {
	if frameIndex < 0 || frameIndex >= x.TotalFrames {
		return BlockEntry{}, clock.NewError("index", clock.CatBadLength,
			"frame %d outside [0,%d)", frameIndex, x.TotalFrames)
	}
	i := sort.Search(len(x.Entries), func(i int) bool {
		return x.Entries[i].FrameStart > frameIndex
	}) - 1
	if i < 0 {
		return BlockEntry{}, clock.NewError("index", clock.CatNotFound,
			"no block covers frame %d", frameIndex)
	}
	return x.Entries[i], nil
}

// NanosToFrames converts a half-open nanosecond interval into a half-open
// frame interval using the shared sample time base. The start floor yields
// the sample covering that instant; the end floor bounds complete frames.
func (x *Index) NanosToFrames(startNanos, endNanos int64, tl *clock.Timeline) (wav.FrameRange, error) {
	if startNanos < 0 || endNanos <= startNanos {
		return wav.FrameRange{}, clock.NewError("index", clock.CatInvalidParam,
			"invalid half-open ns range [%d,%d)", startNanos, endNanos)
	}
	start, err := tl.NanosToSample(startNanos)
	if err != nil {
		return wav.FrameRange{}, err
	}
	end, err := tl.NanosToSample(endNanos)
	if err != nil {
		return wav.FrameRange{}, err
	}
	if end <= start {
		return wav.FrameRange{}, clock.NewError("index", clock.CatBadLength,
			"ns range [%d,%d) contains no complete sample frame", startNanos, endNanos)
	}
	if end > x.TotalFrames {
		// Clamp the end to the last readable frame boundary first; the WAV
		// layer will still report the truncation offset if bytes are missing.
		end = x.TotalFrames
	}
	if start >= end {
		return wav.FrameRange{}, clock.NewError("index", clock.CatTruncated,
			"range starts at frame %d at or beyond readable end %d", start, end)
	}
	return wav.FrameRange{Start: start, End: end}, nil
}

// QueryResult is the materialized answer to one interval query: the anchor
// block covering the start, the exact frame interval, and its raw bytes.
type QueryResult struct {
	StartBlock BlockEntry
	Range      wav.FrameRange
	Bytes      []byte
}

// ReadRange resolves a half-open nanosecond interval against the index and
// reads the exact frames from the WAV file. If the file ends mid-frame the
// WAV reader reports the truncation byte offset and no partial bytes return.
func (x *Index) ReadRange(r *wav.Reader, tl *clock.Timeline, startNanos, endNanos int64) (*QueryResult, error) {
	fr, err := x.NanosToFrames(startNanos, endNanos, tl)
	if err != nil {
		return nil, err
	}
	block, err := x.LocateBlock(fr.Start)
	if err != nil {
		return nil, err
	}
	// The entry must agree with the WAV geometry; a mismatch means the index
	// was built against different metadata and the read is refused.
	h := r.Header()
	if h.DataOffset != x.DataOffset || h.FrameSize() != x.FrameSize ||
		h.SampleRate != x.SampleRate || h.Channels != x.Channels {
		return nil, clock.NewError("index", clock.CatLayoutMismatch,
			"index geometry does not match opened WAV; interval read refused")
	}
	buf, err := r.ReadFrameRange(fr)
	if err != nil {
		return nil, err
	}
	return &QueryResult{StartBlock: block, Range: fr, Bytes: buf}, nil
}

// FrameAtNanos is a convenience accessor returning the floored frame index
// for one media-time instant and the block that covers it.
func (x *Index) FrameAtNanos(nanos int64, tl *clock.Timeline) (int64, BlockEntry, error) {
	frame, err := tl.NanosToSample(nanos)
	if err != nil {
		return 0, BlockEntry{}, err
	}
	block, err := x.LocateBlock(frame)
	if err != nil {
		return 0, BlockEntry{}, err
	}
	return frame, block, nil
}
