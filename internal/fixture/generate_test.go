package fixture

import (
	"testing"

	"mediapipeline/internal/fault"
	"mediapipeline/internal/ingest"
	"mediapipeline/internal/transport"
)

func TestCleanSampleScansAndParses(t *testing.T) {
	for _, codec := range []ingest.Codec{ingest.CodecH264, ingest.CodecH265, ingest.CodecAV1} {
		s, err := Generate(codec, 17, nil)
		if err != nil {
			t.Fatalf("%s: %v", codec, err)
		}
		var rep fault.Report
		units, err := ingest.Scan(codec, s.Stream, ingest.DefaultLimits, &rep)
		if err != nil || rep.Count() != 0 {
			t.Fatalf("%s scan: %v %v", codec, err, rep.All())
		}
		if len(units) != FrameCount {
			t.Fatalf("%s units = %d, want %d", codec, len(units), FrameCount)
		}
		// Units must be tightly packed for the independent boundary check.
		var expect int64
		for _, u := range units {
			if u.Offset != expect {
				t.Fatalf("%s unit offset %d != expected %d", codec, u.Offset, expect)
			}
			expect = u.PayloadOff + int64(len(u.Payload))
		}
		if expect != int64(len(s.Stream)) {
			t.Fatalf("%s packed length %d != stream %d", codec, expect, len(s.Stream))
		}
		var idxRep fault.Report
		idx, err := ingest.ParseIndex(s.IndexBytes, units, ingest.DefaultLimits, &idxRep)
		if err != nil || idxRep.Count() != 0 || len(idx.Entries) != 3 {
			t.Fatalf("%s index: %v %v entries=%d", codec, err, idxRep.All(), len(idx.Entries))
		}
		if len(s.Versions) != 3 || len(s.Versions[0].Entries) != 3 {
			t.Fatalf("%s versions/entries wrong: %+v", codec, s.Versions)
		}
	}
}

func TestSeedDeterminism(t *testing.T) {
	a, _ := Generate(ingest.CodecAV1, 17, nil)
	b, _ := Generate(ingest.CodecAV1, 17, nil)
	if string(a.Stream) != string(b.Stream) {
		t.Fatal("same seed must produce identical stream bytes")
	}
	c, _ := Generate(ingest.CodecAV1, 18, nil)
	if string(a.Stream) == string(c.Stream) {
		t.Fatal("different seeds must differ")
	}
}

func TestTruncateOffsetWithinOneByte(t *testing.T) {
	cases := []struct {
		codec ingest.Codec
		codes []fault.Code
	}{
		{ingest.CodecH264, []fault.Code{fault.CodeNALTruncated}},
		{ingest.CodecH265, []fault.Code{fault.CodeNALTruncated}},
		{ingest.CodecAV1, []fault.Code{fault.CodeOBUTruncated, fault.CodeOBUIllegalLength}},
	}
	for _, tc := range cases {
		s, err := Generate(tc.codec, 17, []string{InjectTruncate})
		if err != nil {
			t.Fatalf("%s: %v", tc.codec, err)
		}
		var rep fault.Report
		if _, err := ingest.Scan(s.Codec, s.Stream, ingest.DefaultLimits, &rep); err != nil {
			t.Fatalf("%s: %v", tc.codec, err)
		}
		trunc := faultsOf(rep, tc.codes...)
		if len(trunc) == 0 {
			t.Fatalf("%s: want a truncation fault, got %v", tc.codec, rep.All())
		}
		got := trunc[0].Offset
		delta := got - s.TruncateOffset
		if delta < 0 {
			delta = -delta
		}
		if delta > 1 {
			t.Fatalf("%s: truncation at %d, injected at %d (delta %d)",
				tc.codec, got, s.TruncateOffset, delta)
		}
	}
}

func TestIndexOOBOffsetWithinOneByte(t *testing.T) {
	s, err := Generate(ingest.CodecH264, 17, []string{InjectIndexOOB})
	if err != nil {
		t.Fatal(err)
	}
	var scanRep fault.Report
	units, _ := ingest.Scan(s.Codec, s.FullStream, ingest.DefaultLimits, &scanRep)
	var rep fault.Report
	if _, err := ingest.ParseIndex(s.IndexBytes, units, ingest.DefaultLimits, &rep); err != nil {
		t.Fatal(err)
	}
	oob := faultsOf(rep, fault.CodeIndexOutOfBounds)
	if len(oob) != 1 {
		t.Fatalf("want one OOB fault, got %v", rep.All())
	}
	delta := oob[0].Offset - s.IndexOOBOffset
	if delta < 0 {
		delta = -delta
	}
	if delta > 1 {
		t.Fatalf("index OOB at %d, injected at %d (delta %d)", oob[0].Offset, s.IndexOOBOffset, delta)
	}
}

func TestTransportInjections(t *testing.T) {
	s, err := Generate(ingest.CodecH264, 17,
		[]string{InjectDuplicate, InjectReorder, InjectBurstLoss})
	if err != nil {
		t.Fatal(err)
	}
	var rep fault.Report
	ro, err := transport.NewReorder(&rep)
	if err != nil {
		t.Fatal(err)
	}
	for arrival, raw := range s.Packets {
		if err := ro.Push(raw, arrival); err != nil {
			t.Fatal(err)
		}
	}
	for _, ssrc := range ro.SSRCs() {
		ro.Flush(ssrc)
	}
	if ro.Duplicates(s.SSRC) != 1 {
		t.Fatalf("duplicates = %d, want 1", ro.Duplicates(s.SSRC))
	}
	gaps := ro.Gaps(s.SSRC)
	if len(gaps) != 1 || gaps[0].Count() != uint64(len(s.LostSequences)) {
		t.Fatalf("gaps = %+v, want one gap of %d packets", gaps, len(s.LostSequences))
	}
	if ro.TotalMissing() != uint64(len(s.LostSequences)) {
		t.Fatalf("missing = %d, want %d", ro.TotalMissing(), len(s.LostSequences))
	}
	if !s.Reordered {
		t.Fatal("reorder bookkeeping not set")
	}
}

func TestUnknownInjectionRejected(t *testing.T) {
	if _, err := Generate(ingest.CodecAV1, 1, []string{"nope"}); err == nil {
		t.Fatal("unknown injection must fail")
	}
	if _, err := Generate(ingest.Codec("mystery"), 1, nil); err == nil {
		t.Fatal("unknown codec must fail")
	}
}

func faultsOf(rep fault.Report, codes ...fault.Code) []fault.Fault {
	var out []fault.Fault
	for _, f := range rep.All() {
		for _, c := range codes {
			if f.Code == c {
				out = append(out, f)
			}
		}
	}
	return out
}
