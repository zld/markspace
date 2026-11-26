package timebase

import (
	"testing"

	"example.com/archiveverify/internal/cmaf"
	"example.com/archiveverify/internal/model"
)

func oneTrackFragment(seq, tfdt uint32, version uint8, durations ...uint32) *cmaf.Fragment {
	samples := make([]cmaf.Sample, 0, len(durations))
	for i, d := range durations {
		samples = append(samples, cmaf.Sample{Index: i, Duration: d, Sync: i == 0})
	}
	return &cmaf.Fragment{
		Source: "src-a", Sequence: seq, HasSequence: true,
		Tracks: []*cmaf.TrackFragment{{
			TrackID: 1, Timescale: 30_000, BaseDecodeTime: uint64(tfdt),
			TFDTVersion: version, Samples: samples,
		}},
	}
}

func TestUnitsToUsRoundsAndAudioInverse(t *testing.T) {
	if got := UnitsToUs(30_000, 30_000); got != 1_000_000 {
		t.Fatalf("exact second=%d", got)
	}
	// 1024/48000 s rounds to 21333 us.
	if got := UnitsToUs(1024, 48_000); got != 21_333 {
		t.Fatalf("aac frame us=%d", got)
	}
	if got := UsToAudioUnits(2_000_000, 48_000); got != 96_000 {
		t.Fatalf("audio units=%d", got)
	}
}

func TestExtendV0WraparoundAtUint32Boundary(t *testing.T) {
	prev := int64(0xfffffff0)
	got := extendV0(0x0000000a, prev)
	want := int64(1<<32 + 10)
	if got != want {
		t.Fatalf("extended tfdt=%d want %d", got, want)
	}
}

func TestSameSourceSmallBackwardIsRecordedAndClamped(t *testing.T) {
	m := NewMapper()
	f1 := oneTrackFragment(1, 60_000, 0, 30_000)
	if _, err := m.MapFragment("src-a", 0, f1); err != nil {
		t.Fatal(err)
	}
	f2 := oneTrackFragment(2, 57_000, 0, 30_000)
	rep, err := m.MapFragment("src-a", 1, f2)
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Repairs) != 1 || m.Repairs[0].Kind != "tfdt_backward_monotonic_clamp" {
		t.Fatalf("repairs=%+v", m.Repairs)
	}
	if len(m.Issues) != 1 {
		t.Fatalf("expected reported MediaError issue, got %+v", m.Issues)
	}
	if m.Repairs[0].OrigRaw != 57_000 || m.Repairs[0].FixedRaw != 60_000 {
		t.Fatalf("repair original/fixed raw wrong: %+v", m.Repairs[0])
	}
	if rep.Samples[0].StartUs != 3_000_000 || rep.Samples[0].EndUs != 4_000_000 {
		t.Fatalf("clamped interval=[%d,%d)", rep.Samples[0].StartUs, rep.Samples[0].EndUs)
	}
}

func TestSourceSwitchContinuesAtPreviousEnd(t *testing.T) {
	m := NewMapper()
	f1 := oneTrackFragment(1, 0, 1, 30_000)
	if _, err := m.MapFragment("src-a", 0, f1); err != nil {
		t.Fatal(err)
	}
	f2 := oneTrackFragment(1, 0, 1, 30_000)
	rep, err := m.MapFragment("src-b", 1, f2)
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Repairs) != 1 || m.Repairs[0].Kind != "source_switch_continue" {
		t.Fatalf("repairs=%+v", m.Repairs)
	}
	r := m.Repairs[0]
	if r.OldSource != "src-a" || r.Source != "src-b" || r.OffsetUs != 1_000_000 {
		t.Fatalf("source switch repair=%+v", r)
	}
	if rep.Samples[0].StartUs != model.Us(1_000_000) {
		t.Fatalf("new source start=%d want previous end", rep.Samples[0].StartUs)
	}
}

func TestAnchorMapsRTPClockToIntegerMicroseconds(t *testing.T) {
	a := ClockAnchor{RTPStart: 180_000, RTPHz: 90_000, MediaStart: 2_000_000}
	points := a.MapRTPPoints([]RTPPointInput{
		{Sequence: 100, Timestamp: 180_000},
		{Sequence: 101, Timestamp: 183_000},
	})
	if points[0].StartUs != 2_000_000 {
		t.Fatalf("anchor point=%d", points[0].StartUs)
	}
	if points[1].StartUs != 2_033_333 {
		t.Fatalf("3000 RTP ticks at 90k mapped to %d", points[1].StartUs)
	}
}

func TestObserveMFHDGapDoesNotChangeDuration(t *testing.T) {
	m := NewMapper()
	m.ObserveSequence("src-a", 1, 0, 8)
	m.ObserveSequence("src-a", 3, 1, 100)
	if len(m.Gaps) != 1 || m.Gaps[0].MissingCount != 1 {
		t.Fatalf("gaps=%+v", m.Gaps)
	}
}
