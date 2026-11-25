package fixture

import (
	"testing"

	"mediad/media"
	"mediad/stream"
	"mediad/wav"
)

func TestWAVEValid(t *testing.T) {
	f := wav.ParseChunks(WAVE(32))
	if len(f.Faults) != 0 {
		t.Fatalf("generated WAVE must parse cleanly: %+v", f.Faults)
	}
	res := wav.ValidatePCM(f, f.Faults)
	if media.HasFatal(res.Faults) || res.FrameCount != 8 {
		t.Fatalf("WAVE geometry wrong: frames=%d faults=%+v", res.FrameCount, res.Faults)
	}
}

func TestTruncatedWAVE(t *testing.T) {
	f := wav.ParseChunks(TruncatedWAVE())
	if len(f.Faults) != 1 || f.Faults[0].Category != media.CatTruncatedHeader {
		t.Fatalf("want truncated_header, got %+v", f.Faults)
	}
	if f.Faults[0].ByteStart != 0 || f.Faults[0].ByteEnd != 7 {
		t.Fatalf("truncation offsets wrong: %+v", f.Faults[0])
	}
}

func TestOOBWAVE(t *testing.T) {
	f := wav.ParseChunks(OOBWAVE())
	found := false
	for _, flt := range f.Faults {
		if flt.Category == media.CatChunkOutOfBounds && flt.ByteStart == 44 && flt.ByteEnd == 544 {
			found = true
		}
	}
	if !found {
		t.Fatalf("OOB range [44,544) not found: %+v", f.Faults)
	}
}

func TestPartialFrameWAVE(t *testing.T) {
	f := wav.ParseChunks(PartialFrameWAVE())
	res := wav.ValidatePCM(f, f.Faults)
	if res.Partial != 3 || res.FrameCount != 8 {
		t.Fatalf("partial accounting wrong: %+v", res)
	}
}

func TestRTPWrapFixture(t *testing.T) {
	tb := media.TimeBase{SampleRate: SampleRate, RTPClock: SampleRate}
	d := stream.NewDecoder(tb)
	res := d.Decode(RTPWrap())
	var seqWrap, tsWrap bool
	for _, flt := range res.Faults {
		switch flt.Category {
		case media.CatRTPSeqWrap:
			seqWrap = true
		case media.CatRTPTSWrap:
			tsWrap = true
		}
	}
	if !seqWrap || !tsWrap {
		t.Fatalf("both wraps expected, got %+v", res.Faults)
	}
	// Fourth packet extended sequence must survive the 16-bit wrap.
	if res.Packets[3].ExtendedSequence != 65537 {
		t.Fatalf("extended seq = %d, want 65537", res.Packets[3].ExtendedSequence)
	}
}

func TestRTPReorderFixture(t *testing.T) {
	tb := media.TimeBase{SampleRate: SampleRate, RTPClock: SampleRate}
	d := stream.NewDecoder(tb)
	res := d.Decode(RTPReorder())
	if !media.HasFatal(res.Faults) {
		t.Fatalf("reorder fixture must carry a fatal fault: %+v", res.Faults)
	}
}

func TestDatasetCoversRequiredKinds(t *testing.T) {
	seen := map[string]bool{}
	for _, c := range Dataset() {
		seen[c.Kind] = true
	}
	for _, k := range []string{
		KindOK, KindTruncated, KindOOB, KindPartial, KindRTPWrap, KindGap,
		KindLargeBytes, KindHeavyBudget,
	} {
		if !seen[k] {
			t.Errorf("dataset missing kind %s", k)
		}
	}
}

func TestDatasetDeterministic(t *testing.T) {
	a := Dataset()
	b := Dataset()
	if len(a) != len(b) {
		t.Fatal("dataset length varies between calls")
	}
	for i := range a {
		if a[i].Name != b[i].Name || a[i].SampleStart != b[i].SampleStart ||
			len(a[i].WAV) != len(b[i].WAV) {
			t.Fatalf("case %d differs between calls", i)
		}
	}
}

func TestCancelDatasetBounded(t *testing.T) {
	cases := CancelDataset()
	if len(cases) != 5000 {
		t.Fatalf("cancel dataset size = %d, want 5000", len(cases))
	}
	for i, c := range cases {
		if c.SampleStart != int64(i)*4 {
			t.Fatalf("cancel anchors not contiguous: case %d at %d", i, c.SampleStart)
		}
	}
}
