package mp4

import (
	"testing"
)

func testSpec(index bool) RepresentationSpec {
	boolPtr := func(v bool) *bool { return &v }
	return RepresentationSpec{
		ID:          "low",
		Timescale:   90000,
		IncludeSidx: index,
		Segments: []SegmentSpec{
			{
				Sequence: 1, StartTicks: 0, DurationTicks: 180000,
				Samples: []SampleSpec{{Data: []byte("sample-0-a"), Duration: 90000}, {Data: []byte("sample-0-b"), Duration: 90000}},
				EMSG:    &EMSGEvent{Timescale: 90000, PTS: 0, Duration: 180000, ID: 7, Message: []byte("cue-1")},
			},
			{
				Sequence: 2, StartTicks: 180000, DurationTicks: 180000,
				Samples:     []SampleSpec{{Data: []byte("second-segment-sample-payload"), Duration: 180000}},
				IncludeSidx: boolPtr(index),
			},
		},
	}
}

func TestScanHealthyFragmentsAndEMSG(t *testing.T) {
	data, err := BuildRepresentation(testSpec(true))
	if err != nil {
		t.Fatal(err)
	}
	r := Scan(data, "low")
	if len(r.StructuredFaults) != 0 {
		t.Fatalf("faults: %v", r.Faults)
	}
	if r.Timescale != 90000 {
		t.Fatalf("timescale=%d want 90000", r.Timescale)
	}
	if len(r.Segments) != 2 {
		t.Fatalf("segments=%d want 2", len(r.Segments))
	}
	for i, s := range r.Segments {
		if !s.Readable || !s.TimingKnown || !s.SequenceKnown {
			t.Fatalf("segment %d not readable: %+v", i, s)
		}
		if len(s.Samples) == 0 {
			t.Fatalf("segment %d has no samples", i)
		}
	}
	if r.Segments[0].StartTicks != 0 || r.Segments[1].StartTicks != 180000 {
		t.Fatalf("bad starts: %d %d", r.Segments[0].StartTicks, r.Segments[1].StartTicks)
	}
	if len(r.Segments[0].EMSG) != 1 || string(r.Segments[0].EMSG[0].Message) != "cue-1" {
		t.Fatalf("emsg=%+v", r.Segments[0].EMSG)
	}
}

func TestMissingIndexRebuildsFromFragments(t *testing.T) {
	data, err := BuildRepresentation(testSpec(false))
	if err != nil {
		t.Fatal(err)
	}
	r := Scan(data, "low")
	if !r.MissingIndex || r.IndexEntries != 0 {
		t.Fatalf("missing index state: %+v", r)
	}
	if r.RebuiltIndex != 2 {
		t.Fatalf("RebuiltIndex=%d want 2", r.RebuiltIndex)
	}
	for _, s := range r.Segments {
		if !s.RebuiltIndex || !s.Readable {
			t.Fatalf("segment not rebuilt/readable: %+v", s)
		}
	}
}

func TestEarlyTruncationReportsOffsetWithoutPanic(t *testing.T) {
	data, err := BuildRepresentation(testSpec(true))
	if err != nil {
		t.Fatal(err)
	}
	for n := 0; n <= len(data); n++ {
		r := Scan(data[:n], "trunc")
		if n == len(data) {
			if r.Corrupted() {
				t.Fatalf("full input reported corruption: %v", r.Faults)
			}
			continue
		}
		// A cut exactly on a box boundary is a valid prefix. Any reported
		// truncation must nevertheless point at the first absent byte.
		if r.Corrupted() && r.FirstUnreadableOffset != int64(n) {
			t.Fatalf("prefix %d first offset=%d", n, r.FirstUnreadableOffset)
		}
	}
	r13 := Scan(data[:13], "trunc")
	if !r13.Corrupted() || r13.FirstUnreadableOffset != 13 {
		t.Fatalf("offset-13 truncation report=%d faults=%v", r13.FirstUnreadableOffset, r13.Faults)
	}
}

func TestTruncatedMDATIsolatesOnlyAffectedSegment(t *testing.T) {
	data, err := BuildRepresentation(testSpec(false))
	if err != nil {
		t.Fatal(err)
	}
	healthy := Scan(data, "low")
	if len(healthy.Segments) != 2 {
		t.Fatalf("expected 2 segments, got %d", len(healthy.Segments))
	}
	cut := int(healthy.Segments[1].MdatPayloadOffset) + 3
	r := Scan(data[:cut], "low")
	if len(r.Segments) != 2 {
		t.Fatalf("scan segments=%d want 2", len(r.Segments))
	}
	if !r.Segments[0].Readable || r.Segments[0].Corrupt {
		t.Fatalf("first segment should be isolated/healthy: %+v", r.Segments[0])
	}
	if !r.Segments[1].Corrupt || r.Segments[1].Readable {
		t.Fatalf("second segment should be corrupt but present: %+v", r.Segments[1])
	}
	if r.Segments[1].FirstUnreadableOffset != int64(cut) {
		t.Fatalf("offset=%d want %d", r.Segments[1].FirstUnreadableOffset, cut)
	}
}
