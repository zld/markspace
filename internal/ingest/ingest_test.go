package ingest

import (
	"errors"
	"testing"

	"github.com/pion/rtp"

	"continuity/internal/timeline"
)

func datagram(t *testing.T, seq uint16) []byte {
	t.Helper()
	p := &rtp.Packet{
		Header:  rtp.Header{Version: 2, SequenceNumber: seq, Timestamp: uint32(seq) * 960},
		Payload: []byte{0xF8, 0x01}, // Opus TOC config 31, code 0
	}
	b, err := p.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestWraparoundDedupGap(t *testing.T) {
	// Arrival order: 65534, 65535, 0 (wrap), 2 (gap at 1), 0 (dup), 2 (dup).
	seqs := []uint16{65534, 65535, 0, 2, 0, 2}
	var dgs [][]byte
	for _, s := range seqs {
		dgs = append(dgs, datagram(t, s))
	}
	res := IngestRTP("audio/rtp", dgs, 8)
	if len(res.Packets) != 4 {
		t.Fatalf("got %d unique packets, want 4", len(res.Packets))
	}
	if res.Duplicates != 2 {
		t.Fatalf("got %d duplicates, want 2", res.Duplicates)
	}
	if res.Packets[2].Seq != 1<<16 {
		t.Fatalf("wrap not unfolded: seq[2]=%d", res.Packets[2].Seq)
	}
	if len(res.Gaps) != 1 || res.Gaps[0].StartSeq != (1<<16)+1 || res.Gaps[0].EndSeq != (1<<16)+1 {
		t.Fatalf("gaps=%+v, want single gap at 65537", res.Gaps)
	}
}

func TestTruncatedDatagramIsFaultNotFabricated(t *testing.T) {
	res := IngestRTP("audio/rtp", [][]byte{datagram(t, 5), {0x80, 0x60}}, 8)
	if len(res.Faults) != 1 {
		t.Fatalf("got %d faults, want 1", len(res.Faults))
	}
	var f *timeline.Fault
	if !errors.As(res.Faults[0], &f) || f.Offset == 0 {
		t.Fatalf("fault missing offset: %+v", res.Faults[0])
	}
	if len(res.Packets) != 1 || res.Packets[0].Seq != 5 {
		t.Fatalf("packets=%+v", res.Packets)
	}
	if len(res.Gaps) != 0 {
		t.Fatalf("truncated datagram must not open a gap: %+v", res.Gaps)
	}
}

func TestOpusDurations(t *testing.T) {
	cases := []struct {
		payload []byte
		want    int
	}{
		{[]byte{0xF8}, 960},        // config 31 (CELT 20ms), code 0
		{[]byte{0x01}, 960},        // config 0 (SILK 10ms), code 1 -> 2 frames
		{[]byte{0x7B, 0x03}, 2880}, // config 15 (HYBRID 20ms), code 3, 3 frames
		{[]byte{0x82}, 240},        // config 16 (CELT 2.5ms), code 2
	}
	for i, c := range cases {
		got, err := OpusPacketSamples("audio/rtp", int64(i), c.payload)
		if err != nil {
			t.Fatalf("case %d: %v", i, err)
		}
		if got != c.want {
			t.Fatalf("case %d: got %d want %d", i, got, c.want)
		}
	}
}

func TestOpusTruncated(t *testing.T) {
	if _, err := OpusPacketSamples("audio/rtp", 9, nil); err == nil {
		t.Fatal("expected fault for empty payload")
	} else {
		var f *timeline.Fault
		if !errors.As(err, &f) || f.Offset != 9 || f.Stage != timeline.StageIngest {
			t.Fatalf("fault not structured: %v", err)
		}
	}
	if _, err := OpusPacketSamples("audio/rtp", 0, []byte{0x03}); err == nil {
		t.Fatal("expected fault for missing frame-count byte")
	}
}

func TestAACFrameDuration(t *testing.T) {
	d, err := AACFrameDuration(timeline.Clock{Timescale: 48000}, 48000)
	if err != nil {
		t.Fatal(err)
	}
	if d != 1024 {
		t.Fatalf("got %d want 1024", d)
	}
	d, err = AACFrameDuration(timeline.Clock{Timescale: 90000}, 48000)
	if err != nil {
		t.Fatal(err)
	}
	if d != 1920 {
		t.Fatalf("got %d want 1920", d)
	}
}
