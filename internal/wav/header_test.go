package wav

import (
	"encoding/binary"
	"testing"

	"assetcheck/internal/errmodel"
)

// testWAV builds a minimal PCM16-ish WAV with the given raw data payload.
func testWAV(channels, rate, bits int, dataPayload []byte) []byte {
	align := channels * bits / 8
	fmtPayload := make([]byte, 16)
	binary.LittleEndian.PutUint16(fmtPayload[0:2], 1)
	binary.LittleEndian.PutUint16(fmtPayload[2:4], uint16(channels))
	binary.LittleEndian.PutUint32(fmtPayload[4:8], uint32(rate))
	binary.LittleEndian.PutUint32(fmtPayload[8:12], uint32(rate*align))
	binary.LittleEndian.PutUint16(fmtPayload[12:14], uint16(align))
	binary.LittleEndian.PutUint16(fmtPayload[14:16], uint16(bits))
	var b []byte
	b = append(b, []byte("RIFF")...)
	b = append(b, 0, 0, 0, 0) // size patched below
	b = append(b, []byte("WAVE")...)
	b = append(b, makeChunk("fmt ", fmtPayload)...)
	b = append(b, makeChunk("data", dataPayload)...)
	binary.LittleEndian.PutUint32(b[4:8], uint32(len(b)-8))
	return b
}

func TestParseGoodHeader(t *testing.T) {
	pcm := make([]byte, 12*100) // 100 frames, 6ch 16-bit
	w, err := Parse(testWAV(6, 48000, 16, pcm))
	if err != nil {
		t.Fatal(err)
	}
	if w.Config.Channels != 6 || w.Config.SampleRate != 48000 ||
		w.Config.BlockAlign != 12 || w.Config.BitsPerSample != 16 {
		t.Fatalf("config wrong: %+v", w.Config)
	}
	if w.ValidFrames != 100 || len(w.TailBytes) != 0 {
		t.Fatalf("frames=%d tail=%d", w.ValidFrames, len(w.TailBytes))
	}
	if len(w.Findings) != 0 {
		t.Fatalf("unexpected findings: %v", w.Findings)
	}
}

func TestParseTruncatedHeader(t *testing.T) {
	_, err := Parse([]byte("RIFF0000W"))
	if err == nil {
		t.Fatal("expected truncated header error")
	}
	e, ok := errmodel.AsError(err)
	if !ok || e.Code != errmodel.ErrTruncatedHeader || e.Offset != 0 {
		t.Fatalf("got %v", err)
	}
}

func TestParseBadRIFFMagic(t *testing.T) {
	b := testWAV(2, 44100, 16, make([]byte, 4))
	copy(b[0:4], "XXXX")
	_, err := Parse(b)
	if e, ok := errmodel.AsError(err); !ok || e.Code != errmodel.ErrBadRIFF {
		t.Fatalf("want E1001, got %v", err)
	}
}

func TestParseBlockAlignMismatch(t *testing.T) {
	b := testWAV(2, 44100, 16, make([]byte, 8))
	// fmt blockAlign lives at: 12 (RIFF/WAVE) + 8 chunk header + 12.
	binary.LittleEndian.PutUint16(b[12+8+12:], 99)
	_, err := Parse(b)
	if e, ok := errmodel.AsError(err); !ok || e.Code != errmodel.ErrBlockAlignMismatch {
		t.Fatalf("want E1010, got %v", err)
	}
}

func TestParseNonFrameTailReportsOffset(t *testing.T) {
	// 2ch 16-bit => align 4; 10 bytes = 2 frames + 2 tail bytes.
	b := testWAV(2, 48000, 16, make([]byte, 10))
	w, err := Parse(b)
	if err != nil {
		t.Fatal(err)
	}
	if w.ValidFrames != 2 || len(w.TailBytes) != 2 {
		t.Fatalf("frames=%d tail=%d", w.ValidFrames, len(w.TailBytes))
	}
	var tail *errmodel.Error
	for _, f := range w.Findings {
		if f.Code == errmodel.ErrNonFrameTail {
			tail = f
		}
	}
	if tail == nil {
		t.Fatalf("missing E1014 finding: %v", w.Findings)
	}
	// data chunk starts after RIFF(12) + fmt chunk(24), header 8 => payload 44.
	if tail.Offset != 44+8 {
		t.Fatalf("tail offset=%d, want 52", tail.Offset)
	}
	if tail.Frame != 2 {
		t.Fatalf("tail frame=%d, want 2", tail.Frame)
	}
}

func TestParseOutOfBoundsData(t *testing.T) {
	b := testWAV(2, 48000, 16, make([]byte, 16))
	// Find data chunk and inflate declared size.
	chunks, _ := WalkChunks(b, 12)
	dc := FindChunk(chunks, "data")
	binary.LittleEndian.PutUint32(b[dc.HeaderOffset+4:], 9999)
	w, err := Parse(b)
	if err != nil {
		t.Fatalf("out-of-bounds data should be a finding, not fatal: %v", err)
	}
	if !w.DataTruncated {
		t.Fatal("expected DataTruncated")
	}
	if w.DataChunk.Available != 16 {
		t.Fatalf("available=%d", w.DataChunk.Available)
	}
}
