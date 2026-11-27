package wav

import (
	"encoding/binary"
	"errors"
	"testing"

	"rtx/internal/model"
)

func buildWAV(channels uint16, rate uint32, bits uint16, frames int, extra []byte) []byte {
	align := channels * (bits / 8)
	dataLen := uint32(frames * int(align))
	buf := make([]byte, 0, 44+int(dataLen)+len(extra))
	buf = append(buf, "RIFF"...)
	buf = binary.LittleEndian.AppendUint32(buf, 36+dataLen+uint32(len(extra)))
	buf = append(buf, "WAVE"...)
	buf = append(buf, "fmt "...)
	buf = binary.LittleEndian.AppendUint32(buf, 16)
	buf = binary.LittleEndian.AppendUint16(buf, 1)
	buf = binary.LittleEndian.AppendUint16(buf, channels)
	buf = binary.LittleEndian.AppendUint32(buf, rate)
	buf = binary.LittleEndian.AppendUint32(buf, rate*uint32(align))
	buf = binary.LittleEndian.AppendUint16(buf, align)
	buf = binary.LittleEndian.AppendUint16(buf, bits)
	buf = append(buf, "data"...)
	buf = binary.LittleEndian.AppendUint32(buf, dataLen)
	frame := make([]byte, align)
	for i := 0; i < frames; i++ {
		buf = append(buf, frame...)
	}
	return append(buf, extra...)
}

func TestParseHeaderPCMWidths(t *testing.T) {
	for _, bits := range []uint16{16, 24, 32} {
		buf := buildWAV(2, 48000, bits, 10, nil)
		h, err := ParseHeader(buf)
		if err != nil {
			t.Fatalf("bits=%d: %v", bits, err)
		}
		if h.BlockAlign != 2*(bits/8) || h.BitsPerSample != bits {
			t.Fatalf("bits=%d: bad header %+v", bits, h)
		}
		if h.DataOffset != 44 || h.FrameCount() != 10 {
			t.Fatalf("bits=%d: offset=%d frames=%d", bits, h.DataOffset, h.FrameCount())
		}
	}
}

func TestBlockAlignmentRejected(t *testing.T) {
	buf := buildWAV(2, 48000, 16, 4, nil)
	binary.LittleEndian.PutUint16(buf[20:22], 3) // corrupt block align
	_, err := ParseHeader(buf)
	var se *model.StructuredError
	if !errors.As(err, &se) || se.Anomaly.Reason != model.ReasonHeader {
		t.Fatalf("want structured header error, got %v", err)
	}
}

func TestTruncatedBlockOffset(t *testing.T) {
	buf := buildWAV(2, 48000, 16, 5, nil)
	// Drop the final 3 bytes: the 5th block (4 bytes) loses 3 bytes, so
	// four complete blocks survive and one orphan byte is reported.
	buf = buf[:len(buf)-3]
	binary.LittleEndian.PutUint32(buf[40:44], 20) // declared size still 5 frames
	h, err := ParseHeader(buf)
	if err != nil {
		t.Fatal(err)
	}
	res, err := ParseBlocks(buf, h, model.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Blocks) != 4 {
		t.Fatalf("want 4 retained complete blocks, got %d", len(res.Blocks))
	}
	wantOffset := h.DataOffset + int64(4*h.BlockAlign)
	if len(res.Anomaly) != 1 || res.Anomaly[0].Offset != wantOffset {
		t.Fatalf("want truncation at %d, got %+v", wantOffset, res.Anomaly)
	}
	if res.Anomaly[0].RawValue != 1 {
		t.Fatalf("want 1 orphan byte, got %d", res.Anomaly[0].RawValue)
	}
}

func TestBoundaryTruncation(t *testing.T) {
	// File ends exactly on a block boundary but before the declared length.
	buf := buildWAV(1, 48000, 16, 5, nil)
	buf = buf[:44+4*2]
	binary.LittleEndian.PutUint32(buf[40:44], 10) // 5 declared, 4 present
	h, err := ParseHeader(buf)
	if err != nil {
		t.Fatal(err)
	}
	if !h.DeclaredTrunc {
		t.Fatal("header must flag declared truncation")
	}
	res, err := ParseBlocks(buf, h, model.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Blocks) != 4 {
		t.Fatalf("want 4 complete blocks, got %d", len(res.Blocks))
	}
	if len(res.Anomaly) != 1 || res.Anomaly[0].Offset != 52 {
		t.Fatalf("want boundary anomaly at 52, got %+v", res.Anomaly)
	}
}

func TestPartialTruncationWithTrailingChunk(t *testing.T) {
	trailer := append([]byte("LIST"), 4, 0, 0, 0, 1, 2, 3, 4)
	// Declared data covers six frames plus one orphan byte (13 bytes);
	// physically the six frames are followed by the orphan and a LIST
	// trailer, so the parser keeps 6 blocks, reports the orphan at its
	// exact offset and still identifies the trailing chunk.
	extra := append([]byte{0xAB}, trailer...)
	buf := buildWAV(1, 48000, 16, 6, extra) // 44+12+1+12 = 69 bytes
	binary.LittleEndian.PutUint32(buf[40:44], 13)
	h, err := ParseHeader(buf)
	if err != nil {
		t.Fatal(err)
	}
	res, err := ParseBlocks(buf, h, model.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Blocks) != 6 {
		t.Fatalf("want 6 complete blocks, got %d", len(res.Blocks))
	}
	var partial, trail bool
	for _, a := range res.Anomaly {
		switch {
		case a.Offset == 56 && a.RawValue == 1: // orphan byte between data and LIST
			partial = true
		case a.Offset == 57: // LIST begins one byte after the declared data end
			trail = true
		}
	}
	if !partial {
		t.Fatalf("missing partial-block anomaly: %+v", res.Anomaly)
	}
	if !trail {
		t.Fatalf("trailing chunk not reported: %+v", res.Anomaly)
	}
}

func TestSampleDecodingWidths(t *testing.T) {
	buf := buildWAV(1, 48000, 16, 1, nil)
	binary.LittleEndian.PutUint16(buf[44:46], 0x8000) // -32768
	h, _ := ParseHeader(buf)
	res, _ := ParseBlocks(buf, h, model.DefaultLimits())
	if res.Blocks[0].Samples[0] != -32768 {
		t.Fatalf("16-bit decode = %d", res.Blocks[0].Samples[0])
	}

	buf24 := buildWAV(1, 48000, 24, 1, nil)
	buf24[44], buf24[45], buf24[46] = 0x00, 0x00, 0x80 // -8388608
	h24, _ := ParseHeader(buf24)
	r24, _ := ParseBlocks(buf24, h24, model.DefaultLimits())
	if r24.Blocks[0].Samples[0] != -1<<23 {
		t.Fatalf("24-bit decode = %d", r24.Blocks[0].Samples[0])
	}
}

func TestLimitEnforced(t *testing.T) {
	buf := buildWAV(2, 48000, 16, 10, nil)
	h, _ := ParseHeader(buf)
	_, err := ParseBlocks(buf, h, model.Limits{MaxBlocks: 3, MaxBytes: 1 << 30})
	var se *model.StructuredError
	if !errors.As(err, &se) || se.Anomaly.Reason != model.ReasonLimit {
		t.Fatalf("want limit error, got %v", err)
	}
}
