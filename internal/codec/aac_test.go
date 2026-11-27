package codec

import (
	"errors"
	"testing"

	"github.com/example/mediad/internal/model"
)

func makeADTS(freqIdx, chanCfg byte, frameLen int, protectionAbsent bool) []byte {
	h := make([]byte, 7)
	h[0] = 0xFF
	h[1] = 0xF0 // MPEG-4, layer 0
	if protectionAbsent {
		h[1] |= 0x01
	}
	h[2] = (freqIdx << 2) | (chanCfg >> 2)
	h[3] = (chanCfg&0x03)<<6 | byte((frameLen>>11)&0x03)
	h[4] = byte((frameLen >> 3) & 0xFF)
	h[5] = byte((frameLen&0x07)<<5) | 0x1F // buffer fullness 0x7FF
	h[6] = 0x1C                            // number_of_AAC_frames - 1 = 0 (bits 7..6)
	return h
}

func TestAACCleanPacket(t *testing.T) {
	p := NewAAC(48000)
	hdr := makeADTS(3 /*48k*/, 2 /*stereo*/, 10, true)
	body := make([]byte, 3) // header 7 + 3 payload = 10
	pkt := append(hdr, body...)
	f, err := p.Parse(4, 100, pkt)
	if err != nil {
		t.Fatal(err)
	}
	if f.Samples != 1024 || f.FrameCount != 1 {
		t.Fatalf("frame = %+v, want 1024 samples / 1 frame", f)
	}
}

func TestAACMultiFrameAndTruncation(t *testing.T) {
	p := NewAAC(48000)
	hdr := makeADTS(3, 2, 9, true)
	pkt := append(hdr, 0xAA, 0xBB) // two frames back to back
	pkt = append(pkt, hdr...)
	pkt = append(pkt, 0xCC, 0xDD)
	f, err := p.Parse(5, 0, pkt)
	if err != nil {
		t.Fatal(err)
	}
	if f.Samples != 2048 || f.FrameCount != 2 {
		t.Fatalf("frame = %+v, want 2048/2", f)
	}

	// Header cut short.
	_, err = p.Parse(6, 50, []byte{0xFF, 0xF0, 0x4C})
	var me *model.Error
	if !errors.As(err, &me) || me.Code != model.CodeTruncatedPacket || me.Sequence != 6 || me.Offset != 50 {
		t.Fatalf("truncation error wrong: %v", err)
	}

	// Declared length overruns the packet.
	bad := makeADTS(3, 2, 50, true)
	_, err = p.Parse(7, 0, bad)
	if !errors.As(err, &me) || me.Code != model.CodeTruncatedPacket {
		t.Fatalf("overrun error wrong: %v", err)
	}

	// Bad sync.
	badSync := makeADTS(3, 2, 9, true)
	badSync[0] = 0x00
	_, err = p.Parse(8, 0, append(badSync, 1, 2))
	if !errors.As(err, &me) || me.Code != model.CodeIllegalPacket {
		t.Fatalf("sync error wrong: %v", err)
	}

	// Reserved rate index.
	badRate := makeADTS(14, 2, 9, true)
	_, err = p.Parse(9, 0, append(badRate, 1, 2))
	if !errors.As(err, &me) || me.Code != model.CodeIllegalPacket {
		t.Fatalf("reserved rate error wrong: %v", err)
	}
}

func TestAACRateMismatch(t *testing.T) {
	p := NewAAC(44100)
	hdr := makeADTS(3 /*48k*/, 2, 9, true)
	_, err := p.Parse(1, 0, append(hdr, 1, 2))
	var me *model.Error
	if !errors.As(err, &me) || me.Code != model.CodeIllegalPacket {
		t.Fatalf("rate mismatch error wrong: %v", err)
	}
}
