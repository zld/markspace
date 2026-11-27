package rtpreplay

import "testing"

// TestBuildStreamStateMP4IndexFollowsSequencePosition proves the MP4 sample
// table is indexed by sequence position, not delivery order. Here sample i's
// declared duration is unique per position, so shifting by one at a gap would
// trip the disagreement check.
func TestBuildStreamStateMP4IndexFollowsSequencePosition(t *testing.T) {
	tr := &Trace{
		Version: "1", Seed: 1,
		Clock: TraceClock{RTPRateHz: 48000, EventRateHz: 1000},
		Streams: []Stream{{
			Track: "opus", Codec: CodecOpus, ClockHz: 48000,
			Packets: []TracePacket{},
		}},
	}
	// Seqs 1,2,3,6,7; positions 4 and 5 missing. Each Opus packet is a
	// single 20ms frame (960 ticks); the MP4 table repeats 960, which only
	// stays consistent if the index tracks the sequence position.
	for i, seq := range []uint16{1, 2, 3, 6, 7} {
		tr.Streams[0].Packets = append(tr.Streams[0].Packets, TracePacket{
			RTPHex:    marshalRTP(t, seq, uint32((int(seq)-1)*960), false, opusPayload(1, 0)),
			ArrivalMs: int64(20 + i*20),
		})
	}
	mp4durs := make([]uint64, 7)
	for i := range mp4durs {
		mp4durs[i] = 960
	}
	ss, err := BuildStreamState(parsedStream(t, tr), 2, mp4durs)
	if err != nil {
		t.Fatal(err)
	}
	// No delivered packet may read past the table: a naive delivery-order
	// index would leave index 5 unused and index 6 unreachable.
	for _, st := range ss.States {
		if st.Kind == "packet" && st.DurationTicks != 960 {
			t.Fatalf("state %d duration=%d", st.ExtSeq, st.DurationTicks)
		}
	}
}
