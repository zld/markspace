package ingest

import (
	"testing"

	"mediapipeline/internal/clock"
	"mediapipeline/internal/fault"
)

func buildH264Units(t *testing.T) []Unit {
	t.Helper()
	stream := append(h264NAL(5, []byte{1, 2}), h264NAL(1, []byte{3})...)
	var rep fault.Report
	units, err := Scan(CodecH264, stream, DefaultLimits, &rep)
	if err != nil || rep.Count() != 0 {
		t.Fatalf("setup: %v", rep.All())
	}
	return units
}

func TestIndexRoundTrip(t *testing.T) {
	units := buildH264Units(t)
	orig := &IndexFile{
		Codec: CodecH264,
		Base:  clock.MustRational(1, 90000),
		Entries: []IndexEntry{
			{UnitIndex: 0, Tick: 0, Offset: units[0].Offset, Size: uint32(len(units[0].Payload))},
		},
	}
	container, err := WriteIndex(orig)
	if err != nil {
		t.Fatal(err)
	}
	var rep fault.Report
	parsed, err := ParseIndex(container, units, DefaultLimits, &rep)
	if err != nil || rep.Count() != 0 {
		t.Fatalf("round trip: %v %v", err, rep.All())
	}
	if parsed.Codec != CodecH264 || len(parsed.Entries) != 1 ||
		parsed.Entries[0].Tick != 0 || parsed.Entries[0].Seconds.Den != 1 {
		t.Fatalf("bad parsed index: %+v", parsed)
	}
}

func TestIndexOutOfBoundsMissingFrame(t *testing.T) {
	units := buildH264Units(t)
	bad := &IndexFile{
		Codec:   CodecH264,
		Base:    clock.MustRational(1, 90000),
		Entries: []IndexEntry{{UnitIndex: 99, Tick: 0, Offset: 1, Size: 1}},
	}
	container, _ := WriteIndex(bad)
	var rep fault.Report
	f, err := ParseIndex(container, units, DefaultLimits, &rep)
	if err != nil {
		t.Fatal(err)
	}
	if len(f.Entries) != 0 || !rep.Has(fault.CodeIndexOutOfBounds) {
		t.Fatalf("missing frame must be flagged and dropped: %v", rep.All())
	}
}

func TestUnifyTimeBaseConflict(t *testing.T) {
	units := buildH264Units(t)
	// 1/90000 base; tick 1 cannot be expressed in 1/1000 ticks uniquely.
	// tick 1 (1/90000 s) has no unique 1/1000 s representation;
	// tick 90 is exactly 1/1000 s and converts cleanly.
	f := &IndexFile{
		Codec: CodecH264,
		Base:  clock.MustRational(1, 90000),
		Entries: []IndexEntry{
			{UnitIndex: 0, Tick: 1, Offset: units[0].Offset, Size: uint32(len(units[0].Payload))},
			{UnitIndex: 1, Tick: 90, Offset: units[1].Offset, Size: uint32(len(units[1].Payload))},
		},
	}
	container, _ := WriteIndex(f)
	var rep fault.Report
	parsed, _ := ParseIndex(container, units, DefaultLimits, &rep)
	if rep.Count() != 0 {
		t.Fatalf("setup faults: %v", rep.All())
	}
	unified := UnifyTimeBase(parsed, clock.MustRational(1, 1000), &rep)
	if len(unified) != 1 {
		t.Fatalf("only the convertible entry survives, got %d", len(unified))
	}
	if !rep.Has(fault.CodeIndexBadTimeBase) {
		t.Fatal("non-unique conversion must be a time-base conflict")
	}
}
