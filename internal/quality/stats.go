package quality

import (
	"archivecheck/internal/media"
)

// FrameStats summarizes decode quality over the whole timeline. Error
// inputs are counted explicitly: missing packets, truncated segments
// and corrupt frames are never silently dropped from the report.
type FrameStats struct {
	DecodedFrames int   `json:"decoded_frames"`
	CorruptFrames int   `json:"corrupt_frames"`
	TotalFrames   int   `json:"total_frames"`
	MissingSegments int `json:"segments_with_loss"`
	TruncatedSegments int `json:"truncated_segments"`
	LostPackets   int   `json:"lost_packets"`
	MeanLuma      int   `json:"mean_luma"` // 0..255, integer average
	MeanLumaQ2    int   `json:"mean_luma_q2"`
	LumaSum       int64 `json:"-"`
}

// CollectFrames walks every segment of the composed timeline and counts
// decodable vs corrupt frames, loss/truncation events and mean luma.
// Mean luma is computed over decodable frames only; a fully failed
// segment still contributes its error counters.
func CollectFrames(segments []*media.Segment) FrameStats {
	var st FrameStats
	var lumaSum int64
	var lumaCount int64
	for _, seg := range segments {
		if seg.LostPackets > 0 {
			st.MissingSegments++
			st.LostPackets += seg.LostPackets
		}
		if seg.Truncated {
			st.TruncatedSegments++
		}
		for _, f := range seg.Frames {
			st.TotalFrames++
			if f.Corrupt || seg.Truncated {
				st.CorruptFrames++
				continue
			}
			st.DecodedFrames++
			lumaSum += int64(f.Luma)
			lumaCount++
		}
	}
	st.LumaSum = lumaSum
	if lumaCount > 0 {
		st.MeanLuma = int(lumaSum / lumaCount)
		// Two fractional digits of the exact mean.
		st.MeanLumaQ2 = int(lumaSum*100 / lumaCount)
	}
	return st
}

// HasBlockingErrors reports whether quality results contain conditions
// that must produce a non-zero process exit code.
func (s FrameStats) HasBlockingErrors() bool {
	return s.LostPackets > 0 || s.TruncatedSegments > 0
}
