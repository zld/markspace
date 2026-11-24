package rtp

import "testing"

// TestVeryLatePacketAfterWatermarkIsNotResurrected ensures an old packet that
// arrives long after the watermark advanced is recorded as late, not shifted a
// whole modulus forward into the future.
func TestVeryLatePacketAfterWatermarkIsNotResurrected(t *testing.T) {
	var in []Packet
	for i := 0; i < 200; i++ {
		if i == 4 {
			continue // skip seq 5 entirely
		}
		in = append(in, Packet{Seq: uint16(i + 1), Timestamp: uint32(i * 100), Payload: []byte{1}})
	}
	// The skipped packet arrives long after the watermark passed it.
	in = append(in, Packet{Seq: 5, Timestamp: 400, Payload: []byte{9}})
	rec, err := Reorder(in)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range rec.Ordered {
		if p.SeqExt > 200 {
			t.Fatalf("late packet resurrected as future seq %d", p.SeqExt)
		}
	}
	if rec.Late != 1 {
		t.Fatalf("late = %d, want 1", rec.Late)
	}
	found := false
	for _, g := range rec.Gaps {
		if g.Start == 5 && g.Reason == "late_dropped" {
			found = true
		}
	}
	if !found {
		t.Fatalf("late packet not marked: %+v", rec.Gaps)
	}
	if len(rec.Ordered) != 199 {
		t.Fatalf("delivered = %d", len(rec.Ordered))
	}
}
