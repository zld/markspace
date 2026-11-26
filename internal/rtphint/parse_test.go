package rtphint

import "testing"

func TestValidateSequenceWrapAround(t *testing.T) {
	seqs := []uint16{65534, 65535, 0, 1, 2}
	raw := make([][]byte, 0, len(seqs))
	for _, seq := range seqs {
		raw = append(raw, MustMarshalPacket(seq))
	}
	rep, err := ValidateSequence(raw)
	if err != nil {
		t.Fatal(err)
	}
	if rep.WrapCount != 1 || rep.Backwards != 0 || rep.Gaps != 0 {
		t.Fatalf("report=%+v faults=%v", rep, rep.Faults)
	}
	if rep.LastExtSeq != 65538 {
		t.Fatalf("last extended seq=%d want 65538", rep.LastExtSeq)
	}
}

func TestValidateSequenceDetectsBackwardsAndBadHeader(t *testing.T) {
	raw := [][]byte{MustMarshalPacket(10), MustMarshalPacket(9)}
	rep, err := ValidateSequence(raw)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Backwards != 1 || len(rep.Faults) != 1 {
		t.Fatalf("report=%+v", rep)
	}
	if _, err := ParseHeader([]byte{0x00, 0x60}); err == nil {
		t.Fatal("expected invalid RTP version/header error")
	}
}
