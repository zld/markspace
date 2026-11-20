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
