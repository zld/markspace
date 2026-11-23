package rtp

import (
	"testing"
)

// wire builds a batch of real RTP packets for sequences, skipping seq 0.
func wire(t *testing.T, seqs []uint16) [][]byte {
	t.Helper()
	out := make([][]byte, 0, len(seqs))
	for _, s := range seqs {
		b, err := BuildPacket(s, []byte{0xDE, 0xAD, 0xBE, 0xEF}, false)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, b)
	}
	return out
}

func TestInOrderStream(t *testing.T) {
	var seqs []uint16
	for i := 1; i <= 100; i++ {
		seqs = append(seqs, uint16(i))
	}
	got, stats, err := Recover(wire(t, seqs), Window)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 100 {
		t.Fatalf("delivered %d want 100", len(got))
	}
	if stats.Lost != 0 || stats.Recovered != 0 {
		t.Fatalf("unexpected stats: %s", stats)
	}
	if rate := stats.PacketRecoveryRate(); rate != 1 {
		t.Fatalf("rate = %f", rate)
	}
	for i, p := range got {
		if p.Sequence != uint16(i+1) {
			t.Fatalf("packet %d seq = %d", i, p.Sequence)
		}
	}
}

func TestReorderingWithinWindow(t *testing.T) {
	// Swap neighbours in groups of four: a small reorder distance.
	var seqs []uint16
	for i := 1; i <= 64; i += 4 {
		seqs = append(seqs, uint16(i+1), uint16(i), uint16(i+3), uint16(i+2))
	}
	got, stats, err := Recover(wire(t, seqs), Window)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Lost != 0 {
		t.Fatalf("lost = %d, want 0 (%s)", stats.Lost, stats)
	}
	if stats.Recovered == 0 {
		t.Fatal("expected some out-of-order packets")
	}
	for i := 1; i < len(got); i++ {
		if got[i-1].Sequence >= got[i].Sequence {
			t.Fatalf("delivery not sorted at %d: %d then %d", i, got[i-1].Sequence, got[i].Sequence)
		}
	}
}

func TestGapBeyondWindowIsUnrecoverable(t *testing.T) {
	// Deliver 1..10, then jump to 200. The hole is far beyond the window.
	var seqs []uint16
	for i := 1; i <= 10; i++ {
		seqs = append(seqs, uint16(i))
	}
	seqs = append(seqs, 200, 201, 202)
	got, stats, err := Recover(wire(t, seqs), Window)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Lost != 189 {
		t.Fatalf("lost = %d, want 189 (%s)", stats.Lost, stats)
	}
	if len(got) != 13 {
		t.Fatalf("delivered %d want 13", len(got))
	}
	if rate := stats.PacketRecoveryRate(); rate >= 0.1 {
		t.Fatalf("rate = %f, expected low", rate)
	}
}

func TestLossBelowThresholdRecoversLater(t *testing.T) {
	// Sequence 5 arrives late but within the window, so nothing is lost.
	seqs := []uint16{1, 2, 3, 4, 6, 7, 8, 9, 10, 5}
	got, stats, err := Recover(wire(t, seqs), Window)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Lost != 0 {
		t.Fatalf("lost = %d: %s", stats.Lost, stats)
	}
	if stats.Recovered == 0 {
		t.Fatal("late packet not counted as recovered")
	}
	if len(got) != 10 {
		t.Fatalf("delivered %d want 10", len(got))
	}
	for i, p := range got {
		if p.Sequence != uint16(i+1) {
			t.Fatalf("delivery[%d] = %d", i, p.Sequence)
		}
	}
}

func TestPacketLossIsCounted(t *testing.T) {
	// Drop every 17th packet: exactly like the demo's --loss-every 17.
	var seqs []uint16
	var dropped int
	for i := 1; i <= 340; i++ {
		if i%17 == 0 {
			dropped++
			continue
		}
		seqs = append(seqs, uint16(i))
	}
	got, stats, err := Recover(wire(t, seqs), Window)
	if err != nil {
		t.Fatal(err)
	}
	// Only holes inside the observed sequence range can be counted: a packet
	// dropped after the last delivered one is indistinguishable from the
	// stream simply having ended.
	wantLost := dropped - 1
	if stats.Lost != wantLost {
		t.Fatalf("lost = %d want %d", stats.Lost, wantLost)
	}
	if len(got) != 340-dropped {
		t.Fatalf("delivered %d want %d", len(got), 340-dropped)
	}
	if rate := stats.PacketRecoveryRate(); rate < 0.90 {
		t.Fatalf("rate = %.3f below 0.90 target", rate)
	}
}
