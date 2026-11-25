package fault

import "testing"

func TestRecorderAndRanges(t *testing.T) {
	r := NewRecorder()
	if r.Total() != 0 {
		t.Fatal("new recorder must be empty")
	}
	r.Add(Fault{Class: ClassMissingPacket, Seq: SeqRange{First: 5, Last: 7}})
	r.Add(Fault{Class: ClassMissingPacket, Seq: SeqRange{First: 9, Last: 9}})
	r.Add(Fault{Class: ClassTruncatedNAL, Byte: ByteRange{Offset: 10, Length: 4}})
	if r.Total() != 3 {
		t.Fatalf("Total = %d, want 3", r.Total())
	}
	counts := r.CountByClass()
	if counts[ClassMissingPacket] != 2 || counts[ClassTruncatedNAL] != 1 {
		t.Fatalf("unexpected counts: %v", counts)
	}
	if got := (SeqRange{First: 5, Last: 7}).Len(); got != 3 {
		t.Fatalf("seq len = %d, want 3", got)
	}
	if got := (ByteRange{Offset: 10, Length: 4}).End(); got != 14 {
		t.Fatalf("byte end = %d, want 14", got)
	}
	all := r.All()
	all[0].Class = ClassUnknownNAL
	if r.All()[0].Class != ClassMissingPacket {
		t.Fatal("All() must return a defensive copy")
	}
}
