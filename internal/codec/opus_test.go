package codec

import (
	"errors"
	"testing"

	"github.com/example/mediad/internal/model"
)

// opusTOC builds a TOC byte for a config/stereo/code combination.
func opusTOC(cfg int, stereo bool, code byte) byte {
	b := byte(cfg) << 3
	if stereo {
		b |= 0x10
	}
	return b | code
}

func TestOpusSingleFrame(t *testing.T) {
	p := NewOpus(48000)
	// config 0: SILK 10ms; code 0 = one frame.
	pkt := []byte{opusTOC(0, false, 0), 0x01, 0x02}
	f, err := p.Parse(10, 0, pkt)
	if err != nil {
		t.Fatal(err)
	}
	if f.Samples != 480 || f.FrameCount != 1 {
		t.Fatalf("frame = %+v, want 480/1", f)
	}
}

func TestOpusCELT2_5ms(t *testing.T) {
	p := NewOpus(48000)
	// config 19: CELT-only 2.5ms.
	pkt := []byte{opusTOC(19, true, 0), 0xAB}
	f, err := p.Parse(1, 0, pkt)
	if err != nil {
		t.Fatal(err)
	}
	if f.Samples != 120 { // 48000 * 2.5 / 1000
		t.Fatalf("samples = %d, want 120", f.Samples)
	}
}

func TestOpusCode1And2(t *testing.T) {
	p := NewOpus(48000)
	// code 1: two equal CBR frames -> config 1 = 20ms each = 960 * 2.
	cbr := []byte{opusTOC(1, false, 1), 0x11, 0x22}
	f, err := p.Parse(2, 0, cbr)
	if err != nil {
		t.Fatal(err)
	}
	if f.Samples != 1920 || f.FrameCount != 2 {
		t.Fatalf("cbr frame = %+v, want 1920/2", f)
	}

	// Unequal CBR bodies are illegal.
	_, err = p.Parse(3, 0, []byte{opusTOC(1, false, 1), 0x11, 0x22, 0x33})
	var me *model.Error
	if !errors.As(err, &me) || me.Code != model.CodeIllegalPacket {
		t.Fatalf("unequal CBR error wrong: %v", err)
	}

	// code 2: VBR pair; length byte 1 then frame0(1B) + frame1(1B).
	vbr := []byte{opusTOC(1, false, 2), 1, 0xAA, 0xBB}
	f, err = p.Parse(4, 0, vbr)
	if err != nil {
		t.Fatal(err)
	}
	if f.Samples != 1920 {
		t.Fatalf("vbr samples = %d, want 1920", f.Samples)
	}

	// VBR first frame overruns packet.
	_, err = p.Parse(5, 77, []byte{opusTOC(1, false, 2), 5, 0xAA})
	if !errors.As(err, &me) || me.Code != model.CodeTruncatedPacket || me.Offset != 77 {
		t.Fatalf("vbr overrun error wrong: %v", err)
	}
}

func TestOpusCode3Framing(t *testing.T) {
	p := NewOpus(48000)
	// CBR code 3: M byte = count-1 = 2 (three frames), no VBR/pad.
	pkt := []byte{opusTOC(0, false, 3), 2, 0xA, 0xB, 0xC}
	f, err := p.Parse(6, 0, pkt)
	if err != nil {
		t.Fatal(err)
	}
	if f.FrameCount != 3 || f.Samples != 1440 { // 3 * 480
		t.Fatalf("code3 frame = %+v, want 1440/3", f)
	}

	// VBR code 3: VBR bit set, count=2 -> one length byte then two frames.
	vbr := []byte{opusTOC(0, false, 3), 0x80 | 0x01, 1, 0xAA, 0xBB}
	f, err = p.Parse(7, 0, vbr)
	if err != nil {
		t.Fatal(err)
	}
	if f.FrameCount != 2 || f.Samples != 960 {
		t.Fatalf("code3 vbr = %+v, want 960/2", f)
	}

	// Padding overrun.
	pad := []byte{opusTOC(0, false, 3), 0x40 | 0x00, 3, 0xAA}
	_, err = p.Parse(8, 0, pad)
	var me *model.Error
	if !errors.As(err, &me) || me.Code != model.CodeTruncatedPacket {
		t.Fatalf("padding error wrong: %v", err)
	}
}

func TestOpusEmptyPacket(t *testing.T) {
	p := NewOpus(48000)
	_, err := p.Parse(9, 321, nil)
	var me *model.Error
	if !errors.As(err, &me) || me.Code != model.CodeTruncatedPacket ||
		me.Stage != model.StageCodec || me.Sequence != 9 || me.Offset != 321 {
		t.Fatalf("empty packet error wrong: %v", err)
	}
}

func TestOpus24kRate(t *testing.T) {
	p := NewOpus(24000)
	f, err := p.Parse(1, 0, []byte{opusTOC(0, false, 0), 0x01})
	if err != nil {
		t.Fatal(err)
	}
	if f.Samples != 240 { // 10ms at 24k
		t.Fatalf("24k samples = %d, want 240", f.Samples)
	}
}
