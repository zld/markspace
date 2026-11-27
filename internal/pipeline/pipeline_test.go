package pipeline

import (
	"testing"

	"mediacore/internal/bitstream"
)

func TestBuildIndexMatchesStream(t *testing.T) {
	s := &bitstream.Stream{
		Codec: bitstream.CodecH264,
		Size:  300,
		Frames: []bitstream.Frame{
			{Index: 0, Offset: 0, Size: 100, Keyframe: true},
			{Index: 1, Offset: 100, Size: 100},
			{Index: 2, Offset: 200, Size: 100, Keyframe: true},
		},
	}
	idx := buildIndex(s)
	if idx.Version != bitstream.IndexVersion || idx.Codec != bitstream.CodecH264 || idx.Timescale != 90000 {
		t.Fatalf("header = %+v", idx)
	}
	if len(idx.Entries) != 3 {
		t.Fatalf("entries = %d", len(idx.Entries))
	}
	if idx.Entries[1].Timestamp != 3000 {
		t.Fatalf("timestamp = %d want 3000", idx.Entries[1].Timestamp)
	}
	if !idx.Entries[0].Keyframe || idx.Entries[1].Keyframe || !idx.Entries[2].Keyframe {
		t.Fatalf("keyframe flags wrong: %+v", idx.Entries)
	}
}

func TestCorruptIndexPlantsThreeFaults(t *testing.T) {
	s := &bitstream.Stream{
		Codec: bitstream.CodecAV1,
		Size:  400,
		Frames: []bitstream.Frame{
			{Index: 0, Offset: 0, Size: 100, Keyframe: true},
			{Index: 1, Offset: 100, Size: 100},
			{Index: 2, Offset: 200, Size: 100},
			{Index: 3, Offset: 300, Size: 100},
		},
	}
	idx := buildIndex(s)
	corruptIndex(idx)
	report, err := bitstream.ValidateIndex(s, idx)
	if err != nil {
		t.Fatal(err)
	}
	if report.QuarantinedCount() != 3 {
		t.Fatalf("quarantined = %d want 3", report.QuarantinedCount())
	}
	if len(report.AcceptedKeyframeOffsets()) != 1 {
		t.Fatalf("the single genuine keyframe must survive: %v", report.AcceptedKeyframeOffsets())
	}
	first := report.Quarantined[0]
	if !first.Fault.HasOffset {
		t.Fatal("quarantine fault must carry an offset")
	}
}

func TestPacketizeDelayProducesRecoveryCount(t *testing.T) {
	frames := []bitstream.Frame{
		{Index: 0, Offset: 0, Size: 5000, Keyframe: true},
		{Index: 1, Offset: 5000, Size: 5000},
		{Index: 2, Offset: 10000, Size: 5000},
	}
	raw := make([]byte, 15000)
	s := &bitstream.Stream{Codec: bitstream.CodecAV1, Size: 15000, Frames: frames}
	opts := Defaults()
	opts.Loss = 3
	opts.Reorder = 2
	opts.WaitWindow = 8
	packets, delayed, err := packetizeStream(raw, s, opts)
	if err != nil {
		t.Fatal(err)
	}
	if len(delayed) != 3 {
		t.Fatalf("delayed = %d want 3", len(delayed))
	}
	rec, err := recoverPackets(packets, opts)
	if err != nil {
		t.Fatal(err)
	}
	if got := countDelivered(rec, delayed); got != 3 {
		t.Fatalf("recovered = %d want 3", got)
	}
	if rec.Missing != 0 {
		t.Fatalf("missing = %d", rec.Missing)
	}
}

func TestPacketizeRejectsLossBeyondStream(t *testing.T) {
	s := &bitstream.Stream{Codec: bitstream.CodecAV1, Size: 10, Frames: []bitstream.Frame{{Index: 0, Offset: 0, Size: 10}}}
	opts := Defaults()
	opts.Loss = 100
	if _, _, err := packetizeStream(make([]byte, 10), s, opts); err == nil {
		t.Fatal("expected loss range error")
	}
}

func TestLegacyManifestShape(t *testing.T) {
	s := &bitstream.Stream{
		Codec: bitstream.CodecAV1,
		Size:  100,
		Frames: []bitstream.Frame{
			{Index: 0, Offset: 0, Size: 50, Keyframe: true},
			{Index: 1, Offset: 50, Size: 50},
		},
	}
	syn := mediaSynthetic()
	b, err := legacyManifest(s, syn)
	if err != nil {
		t.Fatal(err)
	}
	if len(b) == 0 {
		t.Fatal("empty manifest")
	}
}

func TestRoundHelpers(t *testing.T) {
	if round2(1.005) != 1.01 && round2(1.005) != 1.0 {
		t.Fatalf("round2 = %v", round2(1.005))
	}
	if round3(2.5) != 2.5 {
		t.Fatalf("round3 = %v", round3(2.5))
	}
}
