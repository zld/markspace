package stream

import (
	"testing"

	"mediacheck/internal/fault"
)

func TestLocateDamageMergesAdjacentFaults(t *testing.T) {
	res := ScanResult{
		Codec: CodecH264,
		Faults: []fault.Fault{
			{Class: fault.ClassMissingPacket, Seq: fault.SeqRange{First: 5, Last: 5},
				Byte: fault.ByteRange{Offset: 10, Length: 2}, Time: fault.TimeRange{Start: 100, End: 100}},
			{Class: fault.ClassMissingPacket, Seq: fault.SeqRange{First: 6, Last: 6},
				Byte: fault.ByteRange{Offset: 12, Length: 3}, Time: fault.TimeRange{Start: 100, End: 200}},
			{Class: fault.ClassTruncatedNAL, Seq: fault.SeqRange{First: 9, Last: 9},
				Byte: fault.ByteRange{Offset: 40, Length: 1}, Time: fault.TimeRange{Start: 300, End: 300}},
		},
	}
	regions := LocateDamage(res)
	if len(regions) != 2 {
		t.Fatalf("regions = %d, want 2 (classes stay separate): %+v", len(regions), regions)
	}
	r := regions[0]
	if r.Class != fault.ClassMissingPacket || r.Seq != (fault.SeqRange{First: 5, Last: 6}) {
		t.Fatalf("merged region wrong: %+v", r)
	}
	if r.Byte.Offset != 10 || r.Byte.Length != 5 || r.Time.Start != 100 || r.Time.End != 200 {
		t.Fatalf("merged coordinates wrong: %+v", r)
	}
	if len(r.Faults) != 2 {
		t.Fatalf("region must retain contributing faults")
	}
}

func TestLocateDamageEmpty(t *testing.T) {
	if regions := LocateDamage(ScanResult{}); regions != nil {
		t.Fatalf("clean scan returns nil regions, got %+v", regions)
	}
}
