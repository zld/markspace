package ingest

import (
	"testing"

	"github.com/pion/rtp"
)

func pkt(seq uint16) *rtp.Packet {
	return &rtp.Packet{Header: rtp.Header{Version: 2, SequenceNumber: seq, Timestamp: uint32(seq) * 3000}, Payload: []byte{byte(seq)}}
}

func seqs(packets []*rtp.Packet) []uint16 {
	out := make([]uint16, 0, len(packets))
	for _, p := range packets {
		out = append(out, p.SequenceNumber)
	}
	return out
}

func TestRTPReorderWithinWindowAndDuplicates(t *testing.T) {
	r := NewRTPReorder("video", 4, 100)

	out, err := r.Push(pkt(102))
	if err != nil || len(out) != 0 {
		t.Fatalf("future packet should buffer: out=%v err=%v", seqs(out), err)
	}
	out, err = r.Push(pkt(101))
	if err != nil || len(out) != 0 {
		t.Fatalf("second reordered packet should still wait for 100: %v", seqs(out))
	}
	out, err = r.Push(pkt(102))
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 0 || r.Stats().DuplicatePackets != 1 {
		t.Fatalf("buffered duplicate not counted once: out=%v stats=%+v", seqs(out), r.Stats())
	}

	out, err = r.Push(pkt(100))
	if err != nil {
		t.Fatal(err)
	}
	got := seqs(out)
	if len(got) != 3 || got[0] != 100 || got[1] != 101 || got[2] != 102 {
		t.Fatalf("expected restored [100 101 102], got %v", got)
	}

	out, _ = r.Push(pkt(100))
	if len(out) != 0 {
		t.Fatalf("delivered duplicate should not emit again: %v", seqs(out))
	}
	st := r.Stats()
	if st.DuplicatePackets != 2 {
		t.Fatalf("expected two duplicates, got %d", st.DuplicatePackets)
	}
	if st.ReorderedRecovered != 2 || st.ReorderRecoveryRate != 1 {
		t.Fatalf("expected full reorder recovery: %+v", st)
	}
}

func TestRTPOutsideWindowGapContinues(t *testing.T) {
	r := NewRTPReorder("video", 2, 0)
	for _, seq := range []uint16{1, 2} {
		if _, err := r.Push(pkt(seq)); err != nil {
			t.Fatal(err)
		}
	}
	out, err := r.Push(pkt(5))
	if err != nil {
		t.Fatal(err)
	}
	got := seqs(out)
	want := []uint16{1, 2, 5}
	if len(got) != len(want) {
		t.Fatalf("expected continuation %v, got %v", want, got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("expected continuation %v, got %v", want, got)
		}
	}
	st := r.Stats()
	if st.UnrecoveredPackets != 3 {
		t.Fatalf("expected sequences 0,3,4 missing, got %+v gaps=%+v", st.UnrecoveredPackets, st.Gaps)
	}
	if len(st.Gaps) != 1 || st.Gaps[0].Start != 0 || st.Gaps[0].End != 4 {
		t.Fatalf("unexpected gaps: %+v", st.Gaps)
	}
}

func TestRTPFinishTrailingGap(t *testing.T) {
	r := NewRTPReorder("audio", 2, 0)
	for _, seq := range []uint16{0, 1, 2, 5} {
		out, err := r.Push(pkt(uint16(seq)))
		if err != nil {
			t.Fatal(err)
		}
		_ = out
	}
	out := r.Finish()
	got := seqs(out)
	if len(got) != 1 || got[0] != 5 {
		t.Fatalf("expected buffered suffix [5], got %v", got)
	}
	st := r.Stats()
	if st.UnrecoveredPackets != 2 {
		t.Fatalf("expected trailing 3,4 unrecovered, got %d", st.UnrecoveredPackets)
	}
}

func TestRTPSequenceWrap(t *testing.T) {
	r := NewRTPReorder("video", 2, 65534)
	var got []uint16
	for _, seq := range []uint16{65534, 65535, 0, 1} {
		out, err := r.Push(pkt(seq))
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, seqs(out)...)
	}
	if len(got) != 4 || got[0] != 65534 || got[2] != 0 || got[3] != 1 {
		t.Fatalf("wrap order incorrect: %v", got)
	}
}

func TestRTPLatePacketAfterGapIsNotDuplicate(t *testing.T) {
	r := NewRTPReorder("video", 1, 0)
	if _, err := r.Push(pkt(5)); err != nil {
		t.Fatal(err)
	}
	st := r.Stats()
	if st.UnrecoveredPackets != 5 {
		t.Fatalf("expected 0..4 gap before seq5, got %+v", st.Gaps)
	}
	if _, err := r.Push(pkt(2)); err != nil {
		t.Fatal(err)
	}
	st = r.Stats()
	if st.LateOutsideWindow != 1 || st.DuplicatePackets != 0 {
		t.Fatalf("late packet should not be counted duplicate: %+v", st)
	}
}

func TestParseRTPWithPion(t *testing.T) {
	original := pkt(7)
	raw, err := original.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := ParseRTP(raw)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.SequenceNumber != 7 || parsed.Header.Timestamp != original.Header.Timestamp {
		t.Fatalf("parsed packet mismatch: %+v", parsed.Header)
	}
}
