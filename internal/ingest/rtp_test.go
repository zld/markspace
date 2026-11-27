package ingest

import (
	"testing"

	"github.com/pion/rtp"

	"mediad/internal/model"
)

func marshalRTP(seq uint16, ts uint32, payload []byte) []byte {
	p := &rtp.Packet{
		Header: rtp.Header{
			Version: 2, PayloadType: 96, SequenceNumber: seq, Timestamp: ts, SSRC: 1,
		},
		Payload: payload,
	}
	raw, err := p.Marshal()
	if err != nil {
		panic(err)
	}
	return raw
}

func opusSpec() TrackSpec {
	return TrackSpec{ID: "opus-a", Codec: CodecOpus, ClockRate: 48000}
}

func TestOpusDurationsFromTOC(t *testing.T) {
	cases := []struct {
		name   string
		toc    byte
		extra  []byte
		frames int
		wantNS model.Nano
	}{
		{"code0 20ms", 0x00, []byte{0xAA}, 1, 20 * model.Millisecond},
		{"code1 two 20ms", 0x01, []byte{0xAA, 0xBB}, 2, 40 * model.Millisecond},
		{"code2 two 20ms", 0x02, []byte{1, 0xAA, 0xBB}, 2, 40 * model.Millisecond},
		{"code3 cbr two frames", 0x03, []byte{2, 0xAA, 0xBB}, 2, 40 * model.Millisecond},
		{"config16 code0 40ms", 16 << 3, []byte{0xAA}, 1, 40 * model.Millisecond},
		{"config20 code0 60ms", 20 << 3, []byte{0xAA}, 1, 60 * model.Millisecond},
		{"config30 code0 10ms", 30 << 3, []byte{0xAA}, 1, 10 * model.Millisecond},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw := marshalRTP(7, 1000, append([]byte{tc.toc}, tc.extra...))
			pkt, err := ParseRTP(opusSpec(), raw, 0)
			if err != nil {
				t.Fatal(err)
			}
			if err := Parse(opusSpec(), pkt); err != nil {
				t.Fatal(err)
			}
			if pkt.DurationNS != tc.wantNS {
				t.Fatalf("DurationNS=%d want %d", pkt.DurationNS, tc.wantNS)
			}
			wantTC, err := model.RescaleNano(tc.wantNS, 48000)
			if err != nil {
				t.Fatal(err)
			}
			if pkt.DurationTC != wantTC {
				t.Fatalf("DurationTC=%d want %d", pkt.DurationTC, wantTC)
			}
		})
	}
}

func TestTruncatedAndInvalidRTPAreLocated(t *testing.T) {
	spec := opusSpec()
	if _, err := ParseRTP(spec, []byte{0x80, 0x60}, 0); err == nil {
		t.Fatal("short datagram must fail")
	} else if model.AsError(err).Code != model.ErrRTPTruncated {
		t.Fatalf("code = %v", err)
	}
	badVersion := marshalRTP(1, 0, []byte{0})
	badVersion[0] = 0x00 // version 0
	_, err := ParseRTP(spec, badVersion, 0)
	if model.AsError(err).Code != model.ErrRTPVersion {
		t.Fatalf("want ERR_RTP_VERSION, got %v", err)
	}
	// TOC config 31 is reserved.
	raw := marshalRTP(9, 0, []byte{31 << 3, 0xAA})
	pkt, err := ParseRTP(spec, raw, 0)
	if err != nil {
		t.Fatal(err)
	}
	err = Parse(spec, pkt)
	me := model.AsError(err)
	if me.Code != model.ErrOpusTOC || me.Sequence != 9 || me.Track != "opus-a" || me.Stage != "ingest" {
		t.Fatalf("bad located error: %v", err)
	}
	if model.ExitStatus(err) != 2 {
		t.Fatal("input failure must map to exit 2")
	}
}

func TestTruncatedOpusFrames(t *testing.T) {
	spec := opusSpec()
	cases := [][]byte{
		{0x01, 0xAA},    // code 1: odd payload, cannot split
		{0x02, 5, 0xAA}, // code 2: length exceeds payload
		{0x03, 0},       // code 3: zero frame count
		{0x03, 1},       // code 3: no frame data
	}
	for i, pl := range cases {
		raw := marshalRTP(uint16(20+i), 0, pl)
		pkt, err := ParseRTP(spec, raw, 0)
		if err != nil {
			t.Fatal(err)
		}
		if err := Parse(spec, pkt); model.AsError(err).Code != model.ErrOpusTOC {
			t.Fatalf("case %d: want ErrOpusTOC, got %v", i, err)
		}
	}
}

func TestExtendSequenceRollover(t *testing.T) {
	last := uint64(0xFFFF)
	if got := ExtendSequence(0, last); got != 0x10000 {
		t.Fatalf("rollover -> %#x want 0x10000", got)
	}
	if got := ExtendSequence(0xFFFE, last); got != 0xFFFE {
		t.Fatalf("in-order predecessor -> %#x", got)
	}
	if got := ExtendSequence(0x8000, last); got != 0x8000 {
		t.Fatalf("old half -> %#x want 0x8000", got)
	}
	// 0x7fff is exactly 2^15 behind: still treated as a pre-rollover packet.
	if got := ExtendSequence(0x7FFF, last); got != 0x7FFF {
		t.Fatalf("boundary packet -> %#x want 0x7fff", got)
	}
	if got := ExtendSequence(0x7FFE, last); got != 0x17FFE {
		t.Fatalf("post-rollover jump -> %#x want 0x17ffe", got)
	}
	if got := ExtendSequence(123, 0); got != 123 {
		t.Fatalf("first packet -> %d", got)
	}
}
