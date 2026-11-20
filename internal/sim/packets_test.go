package sim

import (
	"testing"

	"rtspingest/internal/rtp"
)

// TestFixtureThroughPipeline drives the generated packets through the real
// reorder + reassembly pipeline, without any network, to lock the fixture's
// expected counters before the end-to-end test is added.
func TestFixtureThroughPipeline(t *testing.T) {
	fx := BuildFixture(17)

	reorder := rtp.NewReorderBuffer()
	h264 := rtp.NewReassembler()

	var reassembled, discarded uint64
	for _, pkt := range fx.Packets {
		for _, ordered := range reorder.Push(pkt) {
			frame, err := h264.Process(ordered)
			if err != nil {
				continue
			}
			if frame != nil {
				_ = frame
			}
		}
	}
	for _, pkt := range reorder.Flush() {
		if _, err := h264.Process(pkt); err != nil {
			continue
		}
	}
	h264.Flush()

	rs := reorder.Stats()
	hs := h264.Stats()
	reassembled, discarded = hs.ReassembledFrames, hs.DiscardedFrames

	if rs.Gaps != 1 {
		t.Errorf("gaps = %d, want 1 (missing seq %d)", rs.Gaps, fx.MissingSeq)
	}
	if rs.Duplicates != 1 {
		t.Errorf("duplicates = %d, want 1", rs.Duplicates)
	}
	if reassembled != 2 {
		t.Errorf("reassembled frames = %d, want 2", reassembled)
	}
	if discarded != 1 {
		t.Errorf("discarded frames = %d, want 1", discarded)
	}
}

// TestFixtureStructure locks the required scenario elements from the task:
// a stream starting at 65534 (wraparound), an out-of-order pair, exactly one
// missing sequence, one complete single-NAL frame, one complete FU-A frame,
// one FU-A frame damaged by the missing fragment, and one duplicate.
func TestFixtureStructure(t *testing.T) {
	fx := BuildFixture(17)

	// first sequence number is just below the 16-bit wrap point
	if fx.Packets[0].SequenceNumber != firstSeq {
		t.Fatalf("first seq = %d, want %d", fx.Packets[0].SequenceNumber, firstSeq)
	}

	// the transmitted set crosses 65535 -> 0 and contains the wrap to 0/1
	seen := map[uint16]int{}
	var hasZero, has65535 bool
	for _, pkt := range fx.Packets {
		seen[pkt.SequenceNumber]++
		if pkt.SequenceNumber == 0 {
			hasZero = true
		}
		if pkt.SequenceNumber == 65535 {
			has65535 = true
		}
	}
	if !hasZero || !has65535 {
		t.Fatal("fixture must cross the 65535->0 wraparound")
	}

	// exactly one sequence number is duplicated
	var dupCount int
	for _, n := range seen {
		if n == 2 {
			dupCount++
		}
		if n > 2 {
			t.Fatalf("a sequence number appears %d times, max expected 2", n)
		}
	}
	if dupCount != 1 {
		t.Fatalf("duplicate sequence numbers = %d, want 1", dupCount)
	}

	// exactly one sequence number from the contiguous range is absent. The
	// logical stream has as many entries as transmitted packets (the
	// duplicate and the single missing cancel out).
	logicalCount := uint16(len(fx.Packets))
	var missing int
	for seq := firstSeq; seq != firstSeq+logicalCount; seq++ {
		if seen[seq] == 0 {
			missing++
			if seq != fx.MissingSeq {
				t.Fatalf("missing seq %d differs from fixture metadata %d", seq, fx.MissingSeq)
			}
		}
	}
	if missing != 1 {
		t.Fatalf("missing sequences = %d, want 1", missing)
	}

	// out-of-order delivery: transmission order is not sorted by sequence
	inOrder := true
	for i := 1; i < len(fx.Packets); i++ {
		if int16(fx.Packets[i].SequenceNumber-fx.Packets[i-1].SequenceNumber) < 0 {
			inOrder = false
		}
	}
	if inOrder {
		t.Fatal("fixture must contain an out-of-order packet pair")
	}

	if fx.CompleteFrames != 2 || fx.DamagedFrames != 1 {
		t.Fatalf("frame counts = %d/%d, want 2 complete, 1 damaged",
			fx.CompleteFrames, fx.DamagedFrames)
	}
}

// TestFixtureDeterministic ensures the same seed yields byte-identical
// payloads and sequence layout across runs.
func TestFixtureDeterministic(t *testing.T) {
	a := BuildFixture(17)
	b := BuildFixture(17)
	if len(a.Packets) != len(b.Packets) {
		t.Fatalf("packet count differs: %d vs %d", len(a.Packets), len(b.Packets))
	}
	for i := range a.Packets {
		pa, pb := a.Packets[i], b.Packets[i]
		if pa.SequenceNumber != pb.SequenceNumber || pa.Timestamp != pb.Timestamp {
			t.Fatalf("packet %d header differs", i)
		}
		if string(pa.Payload) != string(pb.Payload) {
			t.Fatalf("packet %d payload differs", i)
		}
	}
}
