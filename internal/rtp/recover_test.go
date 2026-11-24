package rtp

import (
	"testing"

	pionrtp "github.com/pion/rtp"
)

func pkt(seq uint16, payload string) *Packet {
	return &pionrtp.Packet{
		Header:  pionrtp.Header{SequenceNumber: seq, Timestamp: uint32(seq) * 9000},
		Payload: []byte(payload),
	}
}

func sequences(packets []*Packet) []uint16 {
	out := make([]uint16, 0, len(packets))
	for _, p := range packets {
		out = append(out, p.SequenceNumber)
	}
	return out
}

func TestInOrderAndDuplicates(t *testing.T) {
	r := NewRecoverer(Config{ReorderWindow: 8})
	for _, seq := range []uint16{10, 11, 12, 12, 13} {
		if err := r.Add(pkt(seq, "x")); err != nil {
			t.Fatal(err)
		}
	}
	got := sequences(r.Flush())
	want := []uint16{10, 11, 12, 13}
	if len(got) != len(want) {
		t.Fatalf("sequences=%v want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("sequences=%v want %v", got, want)
		}
	}
	if st := r.Stats(); st.Duplicates != 1 || st.UnrecoveredGaps != 0 {
		t.Fatalf("stats=%+v", st)
	}
}

func TestGapFilledInsideWindowIsRecovered(t *testing.T) {
	r := NewRecoverer(Config{ReorderWindow: 8})
	for _, p := range []*Packet{pkt(0, "a"), pkt(2, "c"), pkt(1, "b")} {
		if err := r.Add(p); err != nil {
			t.Fatal(err)
		}
	}
	out := r.Flush()
	if got := sequences(out); len(got) != 3 || got[0] != 0 || got[1] != 1 || got[2] != 2 {
		t.Fatalf("ordered=%v", got)
	}
	for i, want := range []string{"a", "b", "c"} {
		if string(out[i].Payload) != want {
			t.Fatalf("payload %d=%q want %q (must not fabricate data)", i, out[i].Payload, want)
		}
	}
	st := r.Stats()
	if st.GapsDetected != 1 || st.RecoveredPackets != 1 || st.UnrecoveredGaps != 0 {
		t.Fatalf("stats=%+v", st)
	}
	if rate := st.RecoveryRate(); rate != 1 {
		t.Fatalf("rate=%v want 1", rate)
	}
}

func TestGapOutsideWindowUnrecoveredAndLateNotInserted(t *testing.T) {
	r := NewRecoverer(Config{ReorderWindow: 3})
	if err := r.Add(pkt(100, "start")); err != nil {
		t.Fatal(err)
	}
	if err := r.Add(pkt(104, "far")); err != nil {
		t.Fatal(err)
	}
	// base is now 101. maxExt=104, so 101 has age 3 and is expired immediately.
	if err := r.Add(pkt(101, "late")); err != nil {
		t.Fatal(err)
	}
	out := r.Flush()
	got := sequences(out)
	want := []uint16{100, 104}
	if len(got) != len(want) {
		t.Fatalf("ordered=%v want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("ordered=%v want %v", got, want)
		}
	}
	st := r.Stats()
	if st.UnrecoveredGaps != 3 || st.RecoveredPackets != 0 || st.LateAfterWindow != 1 {
		t.Fatalf("stats=%+v", st)
	}
	if rate := st.RecoveryRate(); rate != 0 {
		t.Fatalf("rate=%v want 0", rate)
	}
}

func TestSequenceWrap(t *testing.T) {
	r := NewRecoverer(Config{ReorderWindow: 4})
	for _, seq := range []uint16{65534, 65535, 2, 1, 0} {
		if err := r.Add(pkt(seq, "w")); err != nil {
			t.Fatal(err)
		}
	}
	got := sequences(r.Flush())
	want := []uint16{65534, 65535, 0, 1, 2}
	if len(got) != len(want) {
		t.Fatalf("wrapped order=%v want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("wrapped order=%v want %v", got, want)
		}
	}
	if st := r.Stats(); st.RecoveredPackets != 2 || st.UnrecoveredGaps != 0 {
		t.Fatalf("wrap stats=%+v", st)
	}
}

func TestRecoveryRateAlwaysUnitInterval(t *testing.T) {
	for recovered := 0; recovered <= 5; recovered++ {
		for unrecovered := 0; unrecovered <= 5; unrecovered++ {
			st := Stats{RecoveredPackets: recovered, UnrecoveredGaps: unrecovered}
			rate := st.RecoveryRate()
			if rate < 0 || rate > 1 {
				t.Fatalf("rate out of range: %v", rate)
			}
		}
	}
}
