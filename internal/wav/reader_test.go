package wav

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"testing"

	"mediacheck/internal/timebase"
)

type wavSpec struct {
	channels int
	rate     int
	bits     int
	data     []byte
}

func buildWAV(s wavSpec) []byte {
	blockAlign := s.channels * s.bits / 8
	byteRate := s.rate * blockAlign
	var buf bytes.Buffer
	buf.WriteString("RIFF")
	dataPad := len(s.data) & 1
	_ = binary.Write(&buf, binary.LittleEndian, uint32(36+len(s.data)+dataPad))
	buf.WriteString("WAVE")
	buf.WriteString("fmt ")
	_ = binary.Write(&buf, binary.LittleEndian, uint32(16))
	_ = binary.Write(&buf, binary.LittleEndian, uint16(1))
	_ = binary.Write(&buf, binary.LittleEndian, uint16(s.channels))
	_ = binary.Write(&buf, binary.LittleEndian, uint32(s.rate))
	_ = binary.Write(&buf, binary.LittleEndian, uint32(byteRate))
	_ = binary.Write(&buf, binary.LittleEndian, uint16(blockAlign))
	_ = binary.Write(&buf, binary.LittleEndian, uint16(s.bits))
	buf.WriteString("data")
	_ = binary.Write(&buf, binary.LittleEndian, uint32(len(s.data)))
	buf.Write(s.data)
	if len(s.data)&1 == 1 {
		buf.WriteByte(0)
	}
	return buf.Bytes()
}

func TestOpenValidPCM16AndDecode(t *testing.T) {
	// One stereo frame: left max positive, right negative.
	data := []byte{0xff, 0x7f, 0x00, 0x80}
	b := buildWAV(wavSpec{channels: 2, rate: 48000, bits: 16, data: data})
	r, err := OpenAt(bytes.NewReader(b), int64(len(b)))
	if err != nil {
		t.Fatal(err)
	}
	m := r.Meta()
	if m.FrameCount != 1 || m.BlockAlign != 4 || m.DataOffset != 44 {
		t.Fatalf("metadata=%+v", m)
	}
	frame := make([]byte, 4)
	if err := r.ReadFrame(frame); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(frame, data) {
		t.Fatalf("frame=%x want %x", frame, data)
	}
	if err := r.ReadFrame(frame); !errors.Is(err, io.EOF) {
		t.Fatalf("second read err=%v want EOF", err)
	}
	l, _ := DecodeSample(frame[0:2], 16)
	rr, _ := DecodeSample(frame[2:4], 16)
	if l != 32767<<16 || rr != -32768<<16 {
		t.Fatalf("decoded=%d,%d", l, rr)
	}
}

func TestBadRIFFLengthReportsOffsetFour(t *testing.T) {
	b := buildWAV(wavSpec{channels: 1, rate: 8000, bits: 16, data: []byte{1, 0}})
	binary.LittleEndian.PutUint32(b[4:8], 9999)
	_, err := OpenAt(bytes.NewReader(b), int64(len(b)))
	var te *timebase.Error
	if !errors.As(err, &te) || te.Code != timebase.CodeRIFF || te.Offset != 4 {
		t.Fatalf("err=%#v", err)
	}
}

func TestPartialFrameReportsFirstBadByte(t *testing.T) {
	// Mono 16-bit: one complete two-byte frame plus one orphan byte.
	b := buildWAV(wavSpec{channels: 1, rate: 48000, bits: 16, data: []byte{1, 0, 2}})
	_, err := OpenAt(bytes.NewReader(b), int64(len(b)))
	var te *timebase.Error
	if !errors.As(err, &te) || te.Code != timebase.CodeAlignment {
		t.Fatalf("err=%v", err)
	}
	// data body begins at 44; complete frame occupies 44:46, orphan at 46.
	if te.Offset != 46 {
		t.Fatalf("bad offset=%d want 46", te.Offset)
	}
}

func TestRejectsUnsupportedRange(t *testing.T) {
	tests := []wavSpec{
		{channels: 9, rate: 48000, bits: 16, data: []byte{}},
		{channels: 2, rate: 193000, bits: 16, data: []byte{}},
		{channels: 2, rate: 48000, bits: 8, data: []byte{}},
	}
	for i, spec := range tests {
		b := buildWAV(spec)
		_, err := OpenAt(bytes.NewReader(b), int64(len(b)))
		if !timebase.IsCode(err, timebase.CodeFmt) {
			t.Fatalf("case %d err=%v want fmt_chunk", i, err)
		}
	}
}
