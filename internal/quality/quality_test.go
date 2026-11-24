package quality

import (
	"testing"

	"archivecheck/internal/media"
)

func seg(track, seq uint32, bytes int, ticks int64, frames ...media.Frame) *media.Segment {
	return &media.Segment{
		TrackID: track, Seq: seq, MediaBytes: bytes, DurationTicks: ticks, Frames: frames,
	}
}

func frame(luma uint8, corrupt bool) media.Frame {
	return media.Frame{Luma: luma, Corrupt: corrupt, Width: 160, Height: 90, Duration: 3000}
}

func TestActualBitrateInteger(t *testing.T) {
	// 1000 bytes over 1 second (90000 ticks): 1000*8 = 8000 bps exactly.
	if got := ActualBitrate(1000, media.ClockRate); got != 8000 {
		t.Fatalf("actual bps = %d, want 8000", got)
	}
	if got := ActualBitrate(0, 100); got != 0 {
		t.Fatalf("zero bytes must yield zero bitrate, got %d", got)
	}
}

func TestMeasureBitrateVaryingBandwidth(t *testing.T) {
	// Two segments, each 2s (180000 ticks). Target 800 kbps.
	// Segment 1 delivers full bytes, segment 2 about half -> deviation
	// must be computed per segment and the max must reflect the dip.
	full := int(int64(800_000) * 180_000 / (8 * media.ClockRate)) // ~20000 bytes
	half := full / 2
	segments := []*media.Segment{
		seg(1, 1, full, 180_000),
		seg(1, 2, half, 180_000),
	}
	rows, summary := MeasureBitrate(segments, map[uint32]int{1: 800_000})
	if len(rows) != 2 || summary.Samples != 2 {
		t.Fatalf("rows=%d summary=%+v", len(rows), summary)
	}
	if rows[0].DeviationBPS >= rows[1].DeviationBPS {
		t.Fatalf("half-bandwidth segment must deviate more: %+v", rows)
	}
	if summary.MaxDevBP != rows[1].DeviationBPS {
		t.Fatalf("max deviation not reported: %+v", summary)
	}
	if summary.MeanDevBP <= 0 || summary.TargetKbps != 800 {
		t.Fatalf("summary wrong: %+v", summary)
	}
}

func TestFrameStatsCountErrors(t *testing.T) {
	segments := []*media.Segment{
		seg(1, 1, 100, 90_000, frame(100, false), frame(120, false)),
		{
			TrackID: 1, Seq: 2, DurationTicks: 90_000, LostPackets: 3,
			Frames: []media.Frame{frame(0, true)},
		},
		{
			TrackID: 1, Seq: 3, DurationTicks: 90_000, Truncated: true,
			Frames: []media.Frame{frame(200, false)},
		},
	}
	st := CollectFrames(segments)
	if st.DecodedFrames != 2 || st.CorruptFrames != 2 || st.TotalFrames != 4 {
		t.Fatalf("frame counts wrong: %+v", st)
	}
	if st.MissingSegments != 1 || st.LostPackets != 3 || st.TruncatedSegments != 1 {
		t.Fatalf("error counters wrong: %+v", st)
	}
	// Mean luma over the two decodable frames (100,120) = 110.
	if st.MeanLuma != 110 || st.MeanLumaQ2 != 11000 {
		t.Fatalf("luma stats wrong: %+v", st)
	}
	if !st.HasBlockingErrors() {
		t.Fatal("loss/truncation must be flagged as blocking")
	}
}

func TestFrameStatsCleanInput(t *testing.T) {
	segments := []*media.Segment{
		seg(1, 1, 10, 90_000, frame(50, false), frame(70, false)),
	}
	st := CollectFrames(segments)
	if st.HasBlockingErrors() || st.CorruptFrames != 0 || st.MeanLuma != 60 {
		t.Fatalf("clean input misreported: %+v", st)
	}
}
