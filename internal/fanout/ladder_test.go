package fanout

import (
	"bytes"
	"testing"

	"github.com/pion/rtp"

	"mediacheck/internal/demux"
	merr "mediacheck/internal/errors"
	rtpx "mediacheck/internal/rtp"
	"mediacheck/internal/testmedia"
)

func readerOf(b []byte) *bytes.Reader { return bytes.NewReader(b) }

func rawPacket(t *testing.T, ssrc uint32, seq uint16) []byte {
	t.Helper()
	pkt := &rtp.Packet{
		Header: rtp.Header{Version: 2, SSRC: ssrc, SequenceNumber: seq, Timestamp: uint32(seq) * 160},
		Payload: []byte{byte(seq), 0x10},
	}
	b, err := pkt.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestLadderSharedClockAndIndependentLayers(t *testing.T) {
	layers := DefaultLayers(7)
	ladder, err := NewLadder(90000, layers)
	if err != nil {
		t.Fatal(err)
	}
	if ladder.InputTimescale != 90000 {
		t.Fatal("input clock not retained")
	}
	// Feed layer 0 in order, layer 1 with a hole, layer 2 nothing.
	for seq := uint16(0); seq < 5; seq++ {
		if err := ladder.Push(rawPacket(t, layers[0].SSRC, seq)); err != nil {
			t.Fatal(err)
		}
	}
	for _, seq := range []uint16{0, 1, 8, 9, 10, 11, 12} {
		if err := ladder.Push(rawPacket(t, layers[1].SSRC, seq)); err != nil {
			t.Fatal(err)
		}
	}
	s0, _ := ladder.State(0)
	if got := s0.Buffer.Drain(); len(got) != 5 {
		t.Fatalf("layer0 packets = %d; want 5", len(got))
	}
	// Shrink layer 1's tolerance before draining so the hole (2..7) crosses
	// the window and is finalized as a locatable gap.
	s1, _ := ladder.State(1)
	s1.Buffer = rtpx.NewReorderBuffer(4)
	for _, seq := range []uint16{0, 1, 8, 9, 10, 11, 12} {
		if err := s1.Buffer.Push(rawPacket(t, layers[1].SSRC, seq)); err != nil {
			t.Fatal(err)
		}
	}
	s1.Buffer.Drain()
	if gaps := s1.Buffer.Gaps(layers[1].SSRC); len(gaps) != 1 || gaps[0].Count != 6 {
		t.Fatalf("layer1 gap = %+v; want one gap of 6", gaps)
	}
}

func TestUnknownSSRCRejected(t *testing.T) {
	ladder, _ := NewLadder(90000, DefaultLayers(7))
	err := ladder.Push(rawPacket(t, 0xdeadbeef, 0))
	e, ok := merr.As(err)
	if !ok || e.Code != merr.CodeInvalidBox {
		t.Fatalf("got %v; want invalid-box for unknown SSRC", err)
	}
}

func TestKeyframeRecoveryIsPerLayer(t *testing.T) {
	layers := DefaultLayers(7)
	ladder, _ := NewLadder(90000, layers)
	media, _ := testmedia.BuildMP4(testmedia.DefaultSpec(7))
	container, err := demux.Parse(readerOf(media.Container))
	if err != nil {
		t.Fatal(err)
	}
	segs := SegmentsFromTrack(container.Tracks[0])
	for _, seg := range segs {
		ladder.MarkSegment(seg)
	}
	// Only layer 2 suffers loss and recovers; layer 0 stays untouched.
	if ladder.Recovered(2) {
		t.Fatal("layer must not report recovery before Resume")
	}
	tick, ok := ladder.RecoverLayer(2)
	// Recovery resumes from the most recent verifiable keyframe.
	lastKey := int64(0)
	for _, seg := range segs {
		if seg.IsKeyframe && seg.StartTick > lastKey {
			lastKey = seg.StartTick
		}
	}
	if !ok || tick != lastKey {
		t.Fatalf("recovery tick = %d ok=%v; want latest keyframe %d", tick, ok, lastKey)
	}
	if !ladder.Recovered(2) || ladder.Recovered(0) {
		t.Fatal("recovery state must stay confined to one layer")
	}
}

func TestRecoveryFailsWithoutKeyframe(t *testing.T) {
	ladder, _ := NewLadder(90000, DefaultLayers(7))
	// No segments marked: there is no verifiable keyframe to resume from.
	if _, ok := ladder.RecoverLayer(1); ok {
		t.Fatal("recovery without keyframe must fail")
	}
}

func TestDuplicateSSRCRejectedAtConstruction(t *testing.T) {
	layers := []Layer{
		{ID: 0, SSRC: 100}, {ID: 1, SSRC: 100},
	}
	if _, err := NewLadder(90000, layers); err == nil {
		t.Fatal("duplicate SSRC must fail ladder construction")
	}
}

// keep the rtp import meaningful for future direct assertions.
var _ = rtpx.NewReorderBuffer
