package audio_test

import (
	"testing"

	"mediasync/internal/audio"
	"mediasync/internal/model"
)

// buildAACPkt assembles an RFC 3640 packet: AU-headers-length, one AU header
// (size<<3 | index delta) per AU, then AU payload bytes.
func buildAACPkt(sizes ...int) []byte {
	headerBits := len(sizes) * 16
	out := []byte{byte(headerBits >> 8), byte(headerBits)}
	for _, sz := range sizes {
		word := uint16(sz) << 3
		out = append(out, byte(word>>8), byte(word))
	}
	for _, sz := range sizes {
		for b := 0; b < sz; b++ {
			out = append(out, 0xAB)
		}
	}
	return out
}

func TestAACValidHeaders(t *testing.T) {
	pkt := buildAACPkt(4, 7)
	aus, f := audio.ParseAUHeaders(pkt, 3)
	if f != nil {
		t.Fatal(f)
	}
	if len(aus) != 2 {
		t.Fatalf("au count=%d", len(aus))
	}
	if aus[0].Size != 4 || aus[0].Offset != 6 {
		t.Fatalf("au0 = %+v", aus[0])
	}
	if aus[1].Size != 7 || aus[1].Offset != 10 {
		t.Fatalf("au1 = %+v", aus[1])
	}
}

func TestAACIllegalHeader(t *testing.T) {
	// headers claim 32 bits (2 AUs) but packet ends immediately.
	if _, f := audio.ParseAUHeaders([]byte{0x00, 0x20}, 1); f == nil ||
		f.Code != model.ErrAUHeader || f.PacketOffset != 1 {
		t.Fatalf("header overrun = %+v", f)
	}
	// Odd bit length.
	if _, f := audio.ParseAUHeaders([]byte{0x00, 0x04, 0, 0}, 0); f == nil ||
		f.Code != model.ErrAUHeader {
		t.Fatalf("odd length = %+v", f)
	}
	// Zero-size AU.
	zero := []byte{0x00, 0x10, 0x00, 0x00}
	if _, f := audio.ParseAUHeaders(zero, 2); f == nil || f.Code != model.ErrAUHeader {
		t.Fatalf("zero size = %+v", f)
	}
}

func TestAACTrucatedPayloadLocated(t *testing.T) {
	// One AU declared as 10 bytes, only 3 payload bytes follow.
	pkt := []byte{0x00, 0x10, byte(10 >> 5), byte(10 << 3), 1, 2, 3}
	_, f := audio.ParseAUHeaders(pkt, 11)
	if f == nil || f.Code != model.ErrAUTruncated || f.PacketOffset != 11 {
		t.Fatalf("truncation = %+v", f)
	}
}

func TestAACSampleTimeBaseExact(t *testing.T) {
	// 1024 samples at 48 kHz = 1024/48000 s = 21,333,333 ns (no float).
	ns, f := audio.SampleDurationNS(1024, 48000, 0)
	if f != nil {
		t.Fatal(f)
	}
	if ns != 21_333_333 {
		t.Fatalf("ns=%d", ns)
	}
	// 1024 at 44.1 kHz.
	ns, f = audio.SampleDurationNS(1024, 44100, 0)
	if f != nil {
		t.Fatal(f)
	}
	if ns != 23_219_954 {
		t.Fatalf("ns=%d", ns)
	}
}

func TestAACSampleTimeBaseErrors(t *testing.T) {
	if _, f := audio.SampleDurationNS(100, 0, 8); f == nil ||
		f.Code != model.ErrMP4Box || f.PacketOffset != 8 {
		t.Fatalf("zero timescale = %+v", f)
	}
}
