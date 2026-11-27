package audio_test

import (
	"testing"

	"mediasync/internal/audio"
	"mediasync/internal/model"
)

func toc(config, code int) byte { return byte(config<<3 | code) }

func TestOpusCodeZeroOneTwo(t *testing.T) {
	cases := []struct {
		name   string
		toc    byte
		frames int
		wantNS model.NS
	}{
		{"silk-20ms-code0", toc(1, 0), 1, 20_000_000},
		{"celt-2.5ms-code0", toc(16, 0), 1, 2_500_000},
		{"silk-60ms-code1", toc(11, 1), 2, 120_000_000},
		{"hybrid-20ms-code2", toc(13, 2), 2, 40_000_000},
		{"celt-20ms-code1", toc(19, 1), 2, 40_000_000},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p, f := audio.PacketDuration([]byte{tc.toc, 0xAA}, 7)
			if f != nil {
				t.Fatalf("unexpected failure: %v", f)
			}
			if p.FrameCount != tc.frames || p.DurationNS != tc.wantNS {
				t.Fatalf("got frames=%d dur=%d want %d/%d", p.FrameCount, p.DurationNS, tc.frames, tc.wantNS)
			}
		})
	}
}

func TestOpusCodeThreeFrameCount(t *testing.T) {
	// config 1 (20 ms), code 3, M byte: 3 frames => M&0x3F = 2, no VBR/pad.
	payload := []byte{toc(1, 3), 0x02, 0x00, 0x00}
	p, f := audio.PacketDuration(payload, 0)
	if f != nil {
		t.Fatal(f)
	}
	if p.FrameCount != 3 || p.DurationNS != 60_000_000 || p.VBR {
		t.Fatalf("got %+v", p)
	}

	// VBR bit set.
	p, f = audio.PacketDuration([]byte{toc(1, 3), 0x82}, 0)
	if f != nil || !p.VBR || p.FrameCount != 3 {
		t.Fatalf("vbr: p=%+v f=%v", p, f)
	}
}

func TestOpusTruncatedAndIllegal(t *testing.T) {
	if _, f := audio.PacketDuration(nil, 4); f == nil || f.PacketOffset != 4 ||
		f.Code != model.ErrEmptyPacket {
		t.Fatalf("empty = %+v", f)
	}
	// code 3 without the M byte.
	if _, f := audio.PacketDuration([]byte{toc(1, 3)}, 9); f == nil ||
		f.Code != model.ErrTruncated || f.PacketOffset != 9 {
		t.Fatalf("code3 truncation = %+v", f)
	}
	// Frame count 49 (M&0x3F = 48).
	if _, f := audio.PacketDuration([]byte{toc(1, 3), 0x30}, 2); f == nil ||
		f.Code != model.ErrOpusFrameCount {
		t.Fatalf("frame-count = %+v", f)
	}
	// Padding that runs past the packet.
	if _, f := audio.PacketDuration([]byte{toc(1, 3), 0x40, 255}, 5); f == nil ||
		f.Code != model.ErrTruncated {
		t.Fatalf("padding = %+v", f)
	}
}

func TestOpusDurationOutOfBounds(t *testing.T) {
	// config 11 = 60 ms frames; code 1 => two frames = 120 ms (allowed).
	if _, f := audio.PacketDuration([]byte{toc(11, 1)}, 0); f != nil {
		t.Fatalf("120ms should be allowed: %v", f)
	}
	// code 3 with many 60 ms frames easily exceeds the 120 ms ceiling.
	if _, f := audio.PacketDuration([]byte{toc(11, 3), 0x05 /*6 frames*/}, 0); f == nil ||
		f.Code != model.ErrDurationOverflow {
		t.Fatalf("overflow not detected: %+v", f)
	}
}
