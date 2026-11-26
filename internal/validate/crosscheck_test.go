package validate

import (
	"testing"

	"mediacheck/internal/gap"
	"mediacheck/internal/timebase"
	"mediacheck/internal/track"
	"mediacheck/internal/wav"
)

func testTrack(t *testing.T) *track.Track {
	t.Helper()
	tr, err := track.New("cam", 48000, 2, 16, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := tr.AppendChunk(0, make([]int32, 48*2), 44); err != nil {
		t.Fatal(err)
	}
	tr.TimelineEnd = 48
	return tr
}

func TestFragmentAgainstTrackAcceptsAligned(t *testing.T) {
	tr := testTrack(t)
	f := FragmentView{
		Index: 0, TrackID: "cam", StartSample: 0, FrameCount: 48,
		SampleRate: 48000, Channels: 2, Bits: 16,
		StartTick: 0, EndTick: 1_000_000, PayloadBytes: 192,
	}
	if err := FragmentAgainstTrack(f, tr); err != nil {
		t.Fatal(err)
	}
}

func TestFragmentRejectsBadLengthTimestampAndMissingSamples(t *testing.T) {
	tr := testTrack(t)
	base := FragmentView{
		TrackID: "cam", StartSample: 0, FrameCount: 48,
		SampleRate: 48000, Channels: 2, Bits: 16,
		StartTick: 0, EndTick: 1_000_000, PayloadBytes: 192,
	}
	badLen := base
	badLen.PayloadBytes = 191
	if !timebase.IsCode(FragmentAgainstTrack(badLen, tr), timebase.CodeFragment) {
		t.Fatal("bad length accepted")
	}
	badTime := base
	badTime.EndTick++
	if FragmentAgainstTrack(badTime, tr) == nil {
		t.Fatal("bad timestamp accepted")
	}
	missing := base
	missing.FrameCount = 96
	missing.PayloadBytes = 384
	missing.EndTick = 2_000_000
	if FragmentAgainstTrack(missing, tr) == nil {
		t.Fatal("fragment referencing absent samples accepted")
	}
}

func TestFragmentCannotCrossGap(t *testing.T) {
	tr := testTrack(t)
	tr.TimelineEnd = 96
	g, err := gap.NewLedger("cam", 48000).RecordMissing(5, 5, 1, 48, 96, "loss")
	if err != nil {
		t.Fatal(err)
	}
	tr.AddGaps([]gap.Gap{g})
	f := FragmentView{
		TrackID: "cam", StartSample: 0, FrameCount: 96,
		SampleRate: 48000, Channels: 2, Bits: 16,
		StartTick: 0, EndTick: 2_000_000, PayloadBytes: 384,
	}
	if !timebase.IsCode(FragmentAgainstTrack(f, tr), timebase.CodeFragment) {
		t.Fatalf("err=%v want fragment_invalid", FragmentAgainstTrack(f, tr))
	}
}

func TestUnionGapSamplesDoesNotDoubleCountLateOverlap(t *testing.T) {
	gaps := []gap.Gap{
		{StartSample: 100, EndSample: 200},
		{StartSample: 120, EndSample: 140}, // late audit interval inside missing run
		{StartSample: 300, EndSample: 310},
	}
	if got := unionGapSamples(gaps); got != 110 {
		t.Fatalf("union missing samples=%d want 110", got)
	}
}

func TestSourceTrackCompleteness(t *testing.T) {
	tr := testTrack(t)
	meta := wav.Metadata{SampleRate: 48000, Channels: 2, BitsPerSample: 16, FrameCount: 48}
	if err := SourceTrack(meta, tr, false); err != nil {
		t.Fatal(err)
	}
	meta.FrameCount = 49
	if SourceTrack(meta, tr, false) == nil {
		t.Fatal("frame count mismatch accepted")
	}
}
