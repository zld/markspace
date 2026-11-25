package rtp

import (
	"testing"

	"github.com/pion/rtp"

	"continuity/internal/media"
)

const (
	testRate     = 48000
	testChannels = 2
	testBits     = 16
	testFrames   = 8 // samples per packet (stereo 16-bit => 32-byte payload)
	testBlock    = 16
)

func marshalPkt(seq uint16, ts uint32, payload []byte) []byte {
	p := &rtp.Packet{
		Header: rtp.Header{
			Version:        2,
			PayloadType:    96,
			SequenceNumber: seq,
			Timestamp:      ts,
			SSRC:           4242,
		},
		Payload: payload,
	}
	raw, err := p.Marshal()
	if err != nil {
		panic(err)
	}
	return raw
}

func payloadFor(seq uint16) []byte {
	b := make([]byte, testFrames*testChannels*(testBits/8))
	for i := range b {
		b[i] = byte(int(seq)*13 + i)
	}
	return b
}

func newTestIngest() *Ingest {
	c := media.NewClock(testRate)
	return NewIngest(c, Config{
		ClockRate:     testRate,
		Channels:      testChannels,
		BitsPerSample: testBits,
		WindowWidth:   8,
		BlockFrames:   testBlock,
	})
}

func TestIngestReordersAndRebuildsPCM(t *testing.T) {
	in := newTestIngest()
	// Order: 0,1,3,2,4 — packet 2 arrives after 3.
	order := []uint16{0, 1, 3, 2, 4}
	for _, seq := range order {
		if _, err := in.PushPacket(marshalPkt(seq, uint32(seq)*testFrames, payloadFor(seq))); err != nil {
			t.Fatal(err)
		}
	}
	blocks, stats, err := in.Flush()
	if err != nil {
		t.Fatal(err)
	}
	if stats.Reordered != 1 || stats.Duplicates != 0 || stats.Gaps != 0 {
		t.Fatalf("stats=%+v", stats)
	}
	// 5 packets * 8 samples = 40 samples => 2 full + 1 partial block.
	if len(blocks) != 3 {
		t.Fatalf("blocks=%d", len(blocks))
	}
	// Verify payload for packet 2 landed at its true global offset
	// (samples 16..23), proving reordering restored the timeline.
	want := payloadFor(2)
	got := blocks[1].PCM[:len(want)] // block 1 starts at sample 16
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("PCM at packet-2 offset byte %d: got %d want %d", i, got[i], want[i])
		}
	}
}

func TestIngestBurstLossMarksGapWithoutFakeSamples(t *testing.T) {
	in := newTestIngest()
	// Drop packets 2 and 3 (samples 16..31).
	for seq := uint16(0); seq < 6; seq++ {
		if seq == 2 || seq == 3 {
			continue
		}
		if _, err := in.PushPacket(marshalPkt(seq, uint32(seq)*testFrames, payloadFor(seq))); err != nil {
			t.Fatal(err)
		}
	}
	blocks, stats, err := in.Flush()
	if err != nil {
		t.Fatal(err)
	}
	if stats.Gaps != 1 {
		t.Fatalf("want one merged gap, got %d", stats.Gaps)
	}
	// Block 1 (samples 16..31) must be all-zero placeholder and flagged.
	b1 := blocks[1]
	if b1.GapSamples != testBlock {
		t.Fatalf("gap samples=%d want %d", b1.GapSamples, testBlock)
	}
	for _, x := range b1.PCM {
		if x != 0 {
			t.Fatal("gap region must contain zero placeholder, not fabricated samples")
		}
	}
	if len(b1.GapRanges) != 1 || b1.GapRanges[0].StartSample != 16 || b1.GapRanges[0].EndSample != 32 {
		t.Fatalf("bad gap range: %+v", b1.GapRanges)
	}
}

func TestIngestDuplicateIgnored(t *testing.T) {
	in := newTestIngest()
	for seq := uint16(0); seq < 4; seq++ {
		in.PushPacket(marshalPkt(seq, uint32(seq)*testFrames, payloadFor(seq)))
	}
	o, err := in.PushPacket(marshalPkt(1, uint32(testFrames), payloadFor(1)))
	if err != nil || o != OutDuplicate {
		t.Fatalf("duplicate: o=%d err=%v", o, err)
	}
	blocks, stats, _ := in.Flush()
	if stats.Duplicates != 1 {
		t.Fatalf("duplicates=%d", stats.Duplicates)
	}
	totalSamples := int64(0)
	for _, b := range blocks {
		totalSamples += b.EndSample - b.StartSample
	}
	if totalSamples != 4*testFrames {
		t.Fatalf("duplicate changed timeline length: %d", totalSamples)
	}
}

func TestIngestMalformedPacket(t *testing.T) {
	in := newTestIngest()
	if _, err := in.PushPacket([]byte{0x01, 0x02}); err == nil {
		t.Fatal("malformed RTP must error")
	} else if media.CategoryOf(err) != media.CatInvalidInput {
		t.Fatalf("category=%s", media.CategoryOf(err))
	}
}

func TestIngestAlignmentErrorBounded(t *testing.T) {
	c := media.NewClock(48000)
	// Use a 90kHz RTP clock to exercise timestamp rounding.
	in := NewIngest(c, Config{ClockRate: 90000, Channels: 2, BitsPerSample: 16, WindowWidth: 8, BlockFrames: 16})
	for seq := uint16(0); seq < 6; seq++ {
		ts := c.SamplesToTimestamp(int64(seq)*testFrames, 90000)
		if _, err := in.PushPacket(marshalPkt(seq, ts, payloadFor(seq))); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := in.Flush(); err != nil {
		t.Fatal(err)
	}
	if in.MaxAlignmentErrorSamples() > 1 {
		t.Fatalf("alignment error %d exceeds one sample", in.MaxAlignmentErrorSamples())
	}
}
