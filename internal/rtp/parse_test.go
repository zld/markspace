package rtp

import (
	"encoding/binary"
	"testing"

	"github.com/pion/rtp"

	"mediad/internal/model"
)

// frame wraps a marshaled RTP packet with its length prefix.
func frame(t *testing.T, pkt *rtp.Packet) []byte {
	t.Helper()
	body, err := pkt.Marshal()
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return append([]byte{byte(len(body) >> 8), byte(len(body))}, body...)
}

func mediaPacket(seq uint16, ts uint32, pt uint8, key bool) *rtp.Packet {
	payload := []byte{0, 1, 2, 3}
	if key {
		payload[0] = 1
	}
	return &rtp.Packet{
		Header: rtp.Header{
			Version: 2, PayloadType: pt, SequenceNumber: seq,
			Timestamp: ts, SSRC: 0xABCDEF01,
		},
		Payload: payload,
	}
}

func TestParseValidMedia(t *testing.T) {
	var p Parser
	evs := p.Feed(frame(t, mediaPacket(100, 9000, PTVideo, true)), 10)
	if len(evs) != 1 {
		t.Fatalf("want 1 event, got %d", len(evs))
	}
	ev := evs[0]
	if ev.Kind != model.KindPacket || ev.Err != model.ErrNone {
		t.Fatalf("kind/err = %v/%v", ev.Kind, ev.Err)
	}
	if ev.Seq16 != 100 || ev.RTPTs != 9000 || ev.Media != model.MediaVideo {
		t.Fatalf("fields = seq %d ts %d media %v", ev.Seq16, ev.RTPTs, ev.Media)
	}
	if !ev.Keyframe {
		t.Fatal("keyframe flag lost")
	}
	if ev.ArrivalMs != 10 {
		t.Fatalf("arrival = %d, want 10", ev.ArrivalMs)
	}
	if ev.EventMs != 100 {
		t.Fatalf("event ms = %d, want 100", ev.EventMs)
	}
}

func TestParseCSRCExtensionAndPadding(t *testing.T) {
	// Two CSRCs, one extension word carrying no data, and padding.
	pkt := mediaPacket(7, 900, PTVideo, false)
	pkt.CSRC = []uint32{1, 2}
	pkt.Extension = true
	pkt.ExtensionProfile = 0xBEDE
	pkt.Padding = true
	pkt.PaddingSize = 4
	raw := frame(t, pkt)

	var p Parser
	evs := p.Feed(raw, 5)
	if len(evs) != 1 || evs[0].Kind != model.KindPacket {
		t.Fatalf("events = %+v", evs)
	}
	if got := len(evs[0].Payload); got != 4 {
		t.Fatalf("payload len = %d, want 4 (padding stripped)", got)
	}
}

func TestTruncatedTailReportsOffset(t *testing.T) {
	raw := frame(t, mediaPacket(1, 0, PTVideo, false))
	full := len(raw)
	var p Parser
	// Feed all but the last 5 bytes, then finish.
	if evs := p.Feed(raw[:full-5], 3); len(evs) != 0 {
		t.Fatalf("unexpected events before finish: %+v", evs)
	}
	evs := p.Finish()
	if len(evs) != 1 {
		t.Fatalf("want 1 malformed event, got %d", len(evs))
	}
	ev := evs[0]
	if ev.Kind != model.KindMalformed || ev.Err != model.ErrTruncated {
		t.Fatalf("kind/err = %v/%v", ev.Kind, ev.Err)
	}
	if ev.ByteOffset != 0 {
		t.Fatalf("byte offset = %d, want 0", ev.ByteOffset)
	}
}

func TestBadVersionAtBoundaryResyncs(t *testing.T) {
	// A frame whose length is plausible but whose version is wrong is not
	// a declared packet; the scanner must slide forward and still find
	// the following good frame without losing it.
	body := make([]byte, 20)
	body[0] = 0x00 // version 0
	body[1] = PTVideo
	bad := append([]byte{0x00, byte(len(body))}, body...)
	good := frame(t, mediaPacket(30, 0, PTVideo, false))
	var p Parser
	evs := p.Feed(append(bad, good...), 1)
	var ok int
	for _, ev := range evs {
		if ev.Kind == model.KindPacket && ev.Seq16 == 30 {
			ok++
		}
	}
	if ok != 1 {
		t.Fatalf("good frame after bad version was lost: %+v", evs)
	}
}

func TestExtensionThenValidPacket(t *testing.T) {
	good := frame(t, mediaPacket(10, 0, PTVideo, false))
	bad := append([]byte(nil), good...)
	badBody := bad[2:]
	badBody[0] |= 0x10 // X bit
	badBody[12], badBody[13], badBody[14], badBody[15] = 0x00, 0x00, 0xFF, 0xFF
	binary.BigEndian.PutUint16(bad, uint16(len(badBody)))

	var p Parser
	evs := p.Feed(append(bad, frame(t, mediaPacket(11, 1, PTVideo, false))...), 1)
	var malformed, ok int
	for _, ev := range evs {
		switch {
		case ev.Kind == model.KindMalformed && ev.Err == model.ErrBadExtension:
			malformed++
			if ev.Seq16 != 10 {
				t.Fatalf("malformed seq = %d, want 10 (header survived)", ev.Seq16)
			}
		case ev.Kind == model.KindPacket && ev.Seq16 == 11:
			ok++
		}
	}
	if malformed != 1 || ok != 1 {
		t.Fatalf("malformed=%d valid=%d events=%+v", malformed, ok, evs)
	}
}

func TestImplausibleFramingResyncs(t *testing.T) {
	junk := []byte{0x00, 0x01, 0xFF}
	good := frame(t, mediaPacket(20, 0, PTAudio, false))
	var p Parser
	evs := p.Feed(append(junk, good...), 2)
	var ok int
	for _, ev := range evs {
		if ev.Kind == model.KindPacket && ev.Seq16 == 20 {
			ok++
		}
	}
	if ok != 1 {
		t.Fatalf("valid packet after junk not recovered: %+v", evs)
	}
}

func TestSenderReportNTPAndNACKExpansion(t *testing.T) {
	sr := make([]byte, 28)
	sr[0], sr[1] = 0x80, 200
	binary.BigEndian.PutUint16(sr[2:], 6)
	binary.BigEndian.PutUint32(sr[4:], 0x11111111)
	binary.BigEndian.PutUint32(sr[8:], uint32(2208988800+12))
	binary.BigEndian.PutUint32(sr[12:], 1<<31)
	binary.BigEndian.PutUint32(sr[16:], 9000)
	srFrame := append([]byte{0x00, 28}, sr...)

	nack := make([]byte, 16)
	nack[0], nack[1] = 0x81, 205
	binary.BigEndian.PutUint16(nack[2:], 3)
	binary.BigEndian.PutUint32(nack[8:], 0x11111111)
	binary.BigEndian.PutUint16(nack[12:], 100)
	binary.BigEndian.PutUint16(nack[14:], 0b101) // PID+1 and PID+3
	nackFrame := append([]byte{0x00, 16}, nack...)

	var p Parser
	evs := p.Feed(append(srFrame, nackFrame...), 1)
	if len(evs) != 2 {
		t.Fatalf("want 2 events, got %+v", evs)
	}
	if evs[0].Kind != model.KindSenderReport {
		t.Fatalf("first kind = %v", evs[0].Kind)
	}
	if want := int64(12500); evs[0].NTPMs != want {
		t.Fatalf("ntp = %d, want %d", evs[0].NTPMs, want)
	}
	if evs[1].Kind != model.KindNACK {
		t.Fatalf("second kind = %v", evs[1].Kind)
	}
	got := evs[1].NACKSeqs
	want := []uint16{100, 101, 103}
	if len(got) != len(want) {
		t.Fatalf("nack seqs = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("nack seqs = %v, want %v", got, want)
		}
	}
}

func TestExtenderWrapReorderDuplicate(t *testing.T) {
	var e Extender
	seqs := []uint16{65534, 65535, 0, 1, 65535, 2, 1}
	wantClass := []SeqClass{SeqInOrder, SeqInOrder, SeqWrap, SeqInOrder, SeqDuplicate, SeqInOrder, SeqDuplicate}
	var last uint64
	for i, s := range seqs {
		ext, class := e.Extend(s)
		if class != wantClass[i] {
			t.Fatalf("step %d seq %d: class %v, want %v", i, s, class, wantClass[i])
		}
		if class != SeqDuplicate {
			last = ext
		}
	}
	if last != 65538 {
		t.Fatalf("final extended = %d, want 65538", last)
	}
}

func TestExtenderAmbiguousIsolation(t *testing.T) {
	var e Extender
	e.Extend(1000)
	if _, class := e.Extend(20000); class != SeqAmbiguous {
		t.Fatalf("forward jump class = %v, want ambiguous", class)
	}
	var base uint16
	back := base - 3000 // wraps to 62536, which is 3000 behind the maximum
	if _, class := e.Extend(back); class != SeqAmbiguous {
		t.Fatalf("backward jump class = %v, want ambiguous", class)
	}
}
