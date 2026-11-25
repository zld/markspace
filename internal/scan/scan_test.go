package scan_test

import (
	"math"
	"testing"

	"example.com/assetcheck/internal/clock"
	"example.com/assetcheck/internal/codec"
	"example.com/assetcheck/internal/fault"
	"example.com/assetcheck/internal/fixture"
	rtpseq "example.com/assetcheck/internal/rtp"
	"example.com/assetcheck/internal/scan"
)

func TestClockFromUnits(t *testing.T) {
	cases := []struct {
		units     uint64
		timescale uint32
		want      clock.Nanos
	}{
		{0, 48000, 0},
		{48000, 48000, clock.Second},
		{960, 48000, 20_000_000},
		{44100, 44100, clock.Second},
		{1, 3, 333_333_333}, // 0.3333... s, rounded half up
		{2, 3, 666_666_667},
	}
	for _, c := range cases {
		got, err := clock.FromUnits(c.units, c.timescale)
		if err != nil {
			t.Fatalf("FromUnits(%d,%d): %v", c.units, c.timescale, err)
		}
		if got != c.want {
			t.Errorf("FromUnits(%d,%d) = %v, want %v", c.units, c.timescale, got, c.want)
		}
	}
	if _, err := clock.FromUnits(1, 0); err == nil {
		t.Error("expected zero timescale error")
	}
	if _, err := clock.FromUnits(math.MaxUint64, 1); err == nil {
		t.Error("expected overflow error")
	}
}

func TestSequenceWrapDuplicateAndGap(t *testing.T) {
	tr := rtpseq.NewTracker()
	// Start just below the 16-bit ceiling so the stream wraps at seq 2.
	for _, seq := range []uint16{65533, 65534, 65535, 0, 1, 2} {
		evs := tr.Accept(seq, int64(seq))
		if len(evs) != 1 || evs[0].Kind != rtpseq.KindPacket {
			t.Fatalf("seq %d: want one packet event, got %+v", seq, evs)
		}
		if evs[0].SeqExt != uint64(65533)+(uint64(seq)-65533+65536)%65536 {
			t.Fatalf("seq %d extended to %d", seq, evs[0].SeqExt)
		}
	}
	if next, _ := tr.Next(); next != 3+65536 {
		t.Fatalf("next extended = %d, want %d", next, 3+65536)
	}
	// Duplicate must not advance media time or the expected-next pointer.
	if evs := tr.Accept(1, 99); len(evs) != 1 || evs[0].Kind != rtpseq.KindDuplicate {
		t.Fatalf("want duplicate, got %+v", evs)
	}
	if next, _ := tr.Next(); next != 3+65536 {
		t.Fatalf("duplicate moved next to %d", next)
	}
	// A jump of three leaves exactly two missing packets.
	evs := tr.Accept(5, 123)
	if len(evs) != 2 || evs[0].Kind != rtpseq.KindPacket || evs[1].Kind != rtpseq.KindGap {
		t.Fatalf("want packet+gap, got %+v", evs)
	}
	if evs[1].Count != 2 {
		t.Fatalf("gap count = %d, want 2", evs[1].Count)
	}
	sum := rtpseq.Summarize(evs)
	if sum.Gaps != 1 || sum.Missing != 2 {
		t.Fatalf("summary = %+v", sum)
	}
}

func TestOpusDurations(t *testing.T) {
	cases := []struct {
		name    string
		payload []byte
		samples uint64
		wantErr bool
	}{
		{"20ms", []byte{19 << 3, 1, 2, 3}, 960, false},
		{"2x20ms", []byte{19<<3 | 1, 0, 1, 2, 3, 4}, 1920, false},
		{"2.5ms", []byte{16 << 3, 9}, 120, false},
		{"empty", nil, 0, true},
		{"truncated code1", []byte{19<<3 | 1}, 0, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f, err := codec.AnalyzeOpus(c.payload)
			if c.wantErr {
				if err == nil {
					t.Fatalf("expected error")
				}
				var fe *fault.Error
				if !faultAs(err, &fe) || fe.Stage != fault.StageCodec {
					t.Fatalf("error is not a codec fault: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if f.Samples != c.samples || f.Kind != codec.KindOpus {
				t.Fatalf("frame = %+v, want %d samples", f, c.samples)
			}
		})
	}
}

func TestAACTruncated(t *testing.T) {
	// ADTS header declaring a 41-byte frame in a 10-byte payload.
	hdr := []byte{0xFF, 0xF1, 0x4C, 0x80, 0x05, 0x20, 0x1F, 0xFC, 0x00, 0x00}
	f, err := codec.AnalyzeAAC(hdr, 48000, 1024)
	if err == nil {
		t.Fatalf("expected truncation error")
	}
	if !f.Truncated {
		t.Fatalf("frame should be marked truncated: %+v", f)
	}
	var fe *fault.Error
	if !faultAs(err, &fe) || fe.Code != "codec.aac.truncated" {
		t.Fatalf("want codec.aac.truncated, got %v", err)
	}
}

func TestTimestampWrapAndRegression(t *testing.T) {
	tr := codec.NewTimestampTracker(0x80000000)
	first, ok := tr.Accept(math.MaxUint32 - 100)
	if !ok || first != math.MaxUint32-100 {
		t.Fatalf("first accept = %d,%v", first, ok)
	}
	// A forward step of 300 that crosses 2^32 is a wrap, not a regression.
	var nextSeq uint32
	nextSeq = uint32(math.MaxUint32 - 100)
	nextSeq += 300 // wraps to 199
	wrapped, ok := tr.Accept(nextSeq)
	if !ok || wrapped != uint64(0x100000000)+199 {
		t.Fatalf("wrap accept = %d,%v", wrapped, ok)
	}
	// Dropping back inside the same cycle is a genuine regression.
	if _, ok := tr.Accept(50); ok {
		t.Fatalf("a regression after the wrap should be non-monotonic")
	}
}

func TestScannerScenarios(t *testing.T) {
	for _, a := range fixture.Build(7) {
		t.Run(string(a.Scenario), func(t *testing.T) {
			res := scan.Run(scan.Input{MP4: a.MP4, RTP: a.RTP})
			switch a.Scenario {
			case fixture.ScenarioNormal:
				if !res.Ok() {
					t.Fatalf("normal scan not clean: %v", faultStrings(res.Faults))
				}
				if len(res.Index.Tracks) != 3 || len(res.Index.Fragments) != 3 {
					t.Fatalf("tracks/fragments = %d/%d", len(res.Index.Tracks), len(res.Index.Fragments))
				}
				assertMonotonic(t, res)
			case fixture.ScenarioDuplicate:
				if !res.Ok() {
					t.Fatalf("duplicate scan not clean: %v", faultStrings(res.Faults))
				}
				dups := 0
				for _, p := range res.Index.Packets {
					if p.Kind == "duplicate" {
						dups++
						if p.Duration != 0 {
							t.Fatalf("duplicate advanced time by %v", p.Duration)
						}
					}
				}
				if dups != 1 {
					t.Fatalf("duplicates = %d, want 1", dups)
				}
			case fixture.ScenarioMissing:
				gaps := 0
				var missing uint64
				for _, p := range res.Index.Packets {
					if p.Kind == "gap" {
						gaps++
						missing += p.GapCount
					}
				}
				if gaps != 1 || missing != 1 {
					t.Fatalf("gaps=%d missing=%d, want 1/1", gaps, missing)
				}
			case fixture.ScenarioTruncated:
				if res.Ok() {
					t.Fatal("truncated packet should be reported")
				}
				assertLocated(t, res.Faults)
			case fixture.ScenarioCorrupt:
				if res.Ok() {
					t.Fatal("corrupt fragment should be reported")
				}
				assertLocated(t, res.Faults)
			}
		})
	}
}

func TestScannerDeterministic(t *testing.T) {
	for _, a := range fixture.Build(7) {
		first := scan.Run(scan.Input{MP4: a.MP4, RTP: a.RTP})
		second := scan.Run(scan.Input{MP4: a.MP4, RTP: a.RTP})
		if first.Index.Digest.Value != second.Index.Digest.Value {
			t.Fatalf("%s: digest changed between runs", a.Scenario)
		}
		if got, want := faultStrings(first.Faults), faultStrings(second.Faults); !equal(got, want) {
			t.Fatalf("%s: fault order changed: %v vs %v", a.Scenario, got, want)
		}
	}
}

func TestScannerRebuildMarksIndex(t *testing.T) {
	a := fixture.Build(7)[0]
	res := scan.Rebuild(scan.Input{MP4: a.MP4, RTP: a.RTP})
	if !res.Index.Rebuilt {
		t.Fatal("rebuilt index should be marked")
	}
}
