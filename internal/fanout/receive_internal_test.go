package fanout

import (
	"testing"

	"github.com/pion/rtp"

	"mediapack/internal/render"
)

func mkPacket(seq uint16, seg uint32, marker bool) *rtp.Packet {
	hdr := payloadHeader{
		segment: seg, frame: seg, tick: seg * 100,
		sourceTrack: 2, sourceSample: 7,
	}
	return &rtp.Packet{
		Header: rtp.Header{
			Version: 2, PayloadType: PayloadType,
			SequenceNumber: seq, Timestamp: seg * 100, SSRC: 1, Marker: marker,
		},
		Payload: append(encodeHeader(hdr), []byte("media")...),
	}
}

func TestHeaderRoundTripsSourceFields(t *testing.T) {
	hdr := payloadHeader{
		segment: 3, frame: 9, tick: 12345,
		sourceTrack: 2, sourceSample: 7,
	}
	dec, err := decodeHeader(encodeHeader(hdr))
	if err != nil {
		t.Fatal(err)
	}
	if dec != hdr {
		t.Fatalf("header round trip lost fields: got %+v want %+v", dec, hdr)
	}
}

// buildSent constructs 6 packets across 2 segments (3 each), every third
// packet marked so frame/segment bookkeeping has markers to observe.
func buildSent() []*rtp.Packet {
	pkts := make([]*rtp.Packet, 0, 6)
	for i := uint16(0); i < 6; i++ {
		seg := uint32(i / 3)
		pkts = append(pkts, mkPacket(i, seg, i%3 == 2))
	}
	return pkts
}

func TestReceiveReordersAndDeduplicates(t *testing.T) {
	sent := buildSent()
	seqToSeg := []int{0, 0, 0, 1, 1, 1}
	// Delivery: 1, 0, 2, 2(dup), 3, 5, 4 (reordered + one duplicate, no loss).
	stream := []*rtp.Packet{
		sent[1], sent[0], sent[2], sent[2], sent[3], sent[5], sent[4],
	}
	rec := receive(stream, len(sent), seqToSeg)
	if rec.duplicates != 1 {
		t.Fatalf("duplicates = %d, want 1", rec.duplicates)
	}
	if !rec.reordered {
		t.Fatal("reordering not detected")
	}
	if len(rec.ordered) != 6 {
		t.Fatalf("ordered packets = %d, want 6", len(rec.ordered))
	}
	for i, p := range rec.ordered {
		if int(p.SequenceNumber) != i {
			t.Fatalf("position %d holds seq %d, want %d", i, p.SequenceNumber, i)
		}
	}
	if len(rec.gaps) != 0 {
		t.Fatalf("no-loss stream must have no gaps, got %+v", rec.gaps)
	}
}

func TestReceiveRecordsGaps(t *testing.T) {
	sent := buildSent()
	seqToSeg := []int{0, 0, 0, 1, 1, 1}
	// Drop seqs 1 and 4: one gap per segment.
	stream := []*rtp.Packet{sent[0], sent[2], sent[3], sent[5]}
	rec := receive(stream, len(sent), seqToSeg)
	if len(rec.gaps) != 2 {
		t.Fatalf("gaps = %d, want 2: %+v", len(rec.gaps), rec.gaps)
	}
	if rec.gaps[0].SegmentIndex != 0 || rec.gaps[0].StartSeq != 1 ||
		rec.gaps[0].EndSeq != 1 || rec.gaps[0].Count != 1 {
		t.Fatalf("first gap wrong: %+v", rec.gaps[0])
	}
	if rec.gaps[1].SegmentIndex != 1 || rec.gaps[1].StartSeq != 4 ||
		rec.gaps[1].EndSeq != 4 {
		t.Fatalf("second gap wrong: %+v", rec.gaps[1])
	}
	if len(rec.ordered) != 4 {
		t.Fatalf("ordered = %d, want 4", len(rec.ordered))
	}
}

func TestReceiveWholeSegmentLossAttributed(t *testing.T) {
	sent := buildSent()
	seqToSeg := []int{0, 0, 0, 1, 1, 1}
	// Entire segment 1 (seqs 3,4,5) lost.
	stream := []*rtp.Packet{sent[0], sent[1], sent[2]}
	rec := receive(stream, len(sent), seqToSeg)
	if len(rec.gaps) != 1 || rec.gaps[0].SegmentIndex != 1 {
		t.Fatalf("whole-segment loss must attribute to segment 1: %+v", rec.gaps)
	}
	if rec.gaps[0].StartSeq != 3 || rec.gaps[0].EndSeq != 5 || rec.gaps[0].Count != 3 {
		t.Fatalf("tail gap range wrong: %+v", rec.gaps[0])
	}
}

func TestBuildSegmentsFlagsCompleteness(t *testing.T) {
	sent := buildSent()
	seqToSeg := []int{0, 0, 0, 1, 1, 1}
	stream := []*rtp.Packet{sent[0], sent[1], sent[2], sent[3], sent[5]}
	rec := receive(stream, len(sent), seqToSeg)
	layer := LayerSpec{Name: "low", FramesPerSeg: 3}
	rep, err := buildSegments(layer, renderFramesForTest(6), rec)
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Segments) != 2 {
		t.Fatalf("segments = %d, want 2", len(rep.Segments))
	}
	if !rep.Segments[0].Complete || rep.Segments[1].Complete {
		t.Fatalf("completeness flags wrong: %+v", rep.Segments)
	}
	// Marker packets (every third) reassemble into frame refs carrying source.
	if len(rep.Frames) != 2 {
		t.Fatalf("frame refs = %d, want 2 markers", len(rep.Frames))
	}
	for _, f := range rep.Frames {
		if f.SourceTrack != 2 || f.SourceSample != 7 {
			t.Fatalf("frame ref lost source fields: %+v", f)
		}
	}
}

// renderFramesForTest builds minimal render frames; only Index and Tick are
// read by buildSegments for segment tick ranges.
func renderFramesForTest(n int) []render.Frame {
	out := make([]render.Frame, n)
	for i := range out {
		out[i] = render.Frame{Index: i, Tick: int64(i * 100), Hash: "h"}
	}
	return out
}
