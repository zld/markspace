package wav

import (
	"encoding/binary"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"liveregress/internal/fault"
)

// wavBytes builds a PCM WAVE file. nframes frames of silence are emitted with
// a deterministic ramp so tests can check byte identity.
func wavBytes(t *testing.T, channels, bits, nframes int, sampleRate uint32) []byte {
	t.Helper()
	block := channels * bits / 8
	data := make([]byte, block*nframes)
	for i := range data {
		data[i] = byte(i*7 + 3)
	}
	return assemble(t, channels, bits, sampleRate, data, int64(len(data)), false)
}

func assemble(t *testing.T, channels, bits int, sampleRate uint32, data []byte, declared int64, rf64 bool) []byte {
	t.Helper()
	fmtChunk := make([]byte, 16)
	binary.LittleEndian.PutUint16(fmtChunk[0:], 1)
	binary.LittleEndian.PutUint16(fmtChunk[2:], uint16(channels))
	binary.LittleEndian.PutUint32(fmtChunk[4:], sampleRate)
	block := channels * bits / 8
	binary.LittleEndian.PutUint32(fmtChunk[8:], sampleRate*uint32(block))
	binary.LittleEndian.PutUint16(fmtChunk[12:], uint16(block))
	binary.LittleEndian.PutUint16(fmtChunk[14:], uint16(bits))

	var body []byte
	appendChunk := func(id string, payload []byte) {
		h := make([]byte, 8)
		copy(h, id)
		binary.LittleEndian.PutUint32(h[4:], uint32(len(payload)))
		body = append(body, h...)
		body = append(body, payload...)
		if len(payload)%2 == 1 {
			body = append(body, 0)
		}
	}
	if rf64 {
		ds64 := make([]byte, 28)
		binary.LittleEndian.PutUint64(ds64[0:], uint64(len(data)))
		binary.LittleEndian.PutUint64(ds64[8:], uint64(declared))
		binary.LittleEndian.PutUint64(ds64[16:], uint64(len(data)/block))
		binary.LittleEndian.PutUint32(ds64[24:], uint32(sampleRate))
		appendChunk("ds64", ds64)
	}
	appendChunk("fmt ", addExtensible(t, fmtChunk, channels, bits))
	if rf64 {
		appendChunk("data", data)
	} else {
		h := make([]byte, 8)
		copy(h, "data")
		if declared >= 0 {
			binary.LittleEndian.PutUint32(h[4:], uint32(declared))
		} else {
			binary.LittleEndian.PutUint32(h[4:], uint32(len(data)))
		}
		body = append(body, h...)
		body = append(body, data...)
		if len(data)%2 == 1 {
			body = append(body, 0)
		}
	}
	hdr := make([]byte, 12)
	if rf64 {
		copy(hdr, "RF64")
		binary.LittleEndian.PutUint32(hdr[4:], 0xFFFFFFFF)
	} else {
		copy(hdr, "RIFF")
		binary.LittleEndian.PutUint32(hdr[4:], uint32(len(body)+4))
	}
	copy(hdr[8:], "WAVE")
	return append(hdr, body...)
}

func addExtensible(t *testing.T, base []byte, channels, bits int) []byte { return base }

func writeTemp(t *testing.T, b []byte) (string, string) {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "a.wav")
	if err := os.WriteFile(p, b, 0o644); err != nil {
		t.Fatal(err)
	}
	return p, "a.wav"
}

func TestOpenPCMDepths(t *testing.T) {
	for _, bits := range []int{16, 24, 32} {
		b := wavBytes(t, 3, bits, 100, 48000)
		p, rel := writeTemp(t, b)
		info, err := Open(p, rel)
		if err != nil {
			t.Fatalf("%d-bit: %v", bits, err)
		}
		if info.Channels != 3 || info.SampleRate != 48000 || info.BitsPerSample != bits {
			t.Fatalf("%d-bit info = %+v", bits, info)
		}
		if info.Frames != 100 || info.BlockAlign != 3*bits/8 || info.BytesPerSample != bits/8 {
			t.Fatalf("%d-bit frames/align = %+v", bits, info)
		}
		if info.TruncatedBytes != 0 {
			t.Fatalf("%d-bit unexpected truncation", bits)
		}
	}
}

func TestReaderOffsetsAndBytes(t *testing.T) {
	b := wavBytes(t, 3, 24, 10, 48000)
	p, rel := writeTemp(t, b)
	info, err := Open(p, rel)
	if err != nil {
		t.Fatal(err)
	}
	r, err := NewReader(p, rel, info)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	var frames []*SampleFrame
	for {
		fr, err := r.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		frames = append(frames, fr)
	}
	if len(frames) != 10 {
		t.Fatalf("frames = %d", len(frames))
	}
	if frames[0].Offset != info.DataOffset || frames[1].Offset != info.DataOffset+9 {
		t.Fatalf("offsets = %d,%d", frames[0].Offset, frames[1].Offset)
	}
	if got := frames[3].Bytes; string(got) != string(b[frames[3].Offset:frames[3].Offset+9]) {
		t.Fatal("frame bytes differ from raw input")
	}
	if len(info.Sample(frames[0], 2)) != 3 {
		t.Fatal("sample slice wrong size")
	}
}

func TestTruncatedTailIsRecoverable(t *testing.T) {
	full := wavBytes(t, 3, 24, 10, 48000)
	// Chop 4 bytes: a block is 9 bytes, so 86 bytes remain = 9 frames + 5 tail.
	cut := full[:len(full)-4]
	p, rel := writeTemp(t, cut)
	info, err := Open(p, rel)
	if !errors.Is(err, fault.ErrTruncated) {
		t.Fatalf("want truncate, got %v", err)
	}
	fe, _ := fault.As(err)
	if !fe.Recoverable || fe.Offset != info.DataOffset+9*9 {
		t.Fatalf("fault = %+v info=%+v", fe, info)
	}
	if info.Frames != 9 || info.TruncatedBytes != 5 {
		t.Fatalf("info = %+v", info)
	}
}

func TestDeclaredDataPastEOF(t *testing.T) {
	data := make([]byte, 9*10)
	b := assemble(t, 3, 24, 48000, data, 9*100, false) // declares 100 frames
	p, rel := writeTemp(t, b)
	info, err := Open(p, rel)
	if !errors.Is(err, fault.ErrTruncated) {
		t.Fatalf("want truncate, got %v", err)
	}
	if info.Frames != 10 {
		t.Fatalf("info=%+v err=%v", info, err)
	}
}

func TestUnsupportedEncodings(t *testing.T) {
	base := wavBytes(t, 2, 16, 4, 44100)
	cases := map[string]func([]byte){
		"adpcm": func(b []byte) { binary.LittleEndian.PutUint16(b[20:], 0x0002) },
		"float": func(b []byte) { binary.LittleEndian.PutUint16(b[20:], 0x0003) },
	}
	for name, mutate := range cases {
		b := append([]byte(nil), base...)
		mutate(b)
		p, rel := writeTemp(t, b)
		_, err := Open(p, rel)
		if !errors.Is(err, fault.ErrUnsupported) {
			t.Fatalf("%s: want unsupported, got %v", name, err)
		}
	}
}

func TestEightBitRejected(t *testing.T) {
	p, rel := writeTemp(t, wavBytes(t, 1, 8, 4, 8000))
	_, err := Open(p, rel)
	if !errors.Is(err, fault.ErrUnsupported) {
		t.Fatalf("8-bit: %v", err)
	}
}

func TestNotWav(t *testing.T) {
	p, rel := writeTemp(t, []byte("not a wav file at all"))
	_, err := Open(p, rel)
	if !errors.Is(err, fault.ErrFormat) {
		t.Fatalf("want format, got %v", err)
	}
}

func TestMissingDataChunk(t *testing.T) {
	base := wavBytes(t, 1, 16, 1, 8000)
	idx := indexOf(base, "data")
	p, rel := writeTemp(t, base[:idx-4])
	_, err := Open(p, rel)
	if err == nil {
		t.Fatal("expected error for missing data chunk")
	}
}

func indexOf(b []byte, s string) int {
	for i := 0; i+len(s) <= len(b); i++ {
		if string(b[i:i+len(s)]) == s {
			return i
		}
	}
	return -1
}

func TestRF64ParsesDataSizeFromDS64(t *testing.T) {
	data := make([]byte, 9*20)
	for i := range data {
		data[i] = byte(i)
	}
	raw := assemble(t, 3, 24, 48000, data, int64(len(data)), true)
	p, rel := writeTemp(t, raw)
	info, err := Open(p, rel)
	if err != nil {
		t.Fatalf("RF64: %v", err)
	}
	if !info.RF64 || info.Frames != 20 || info.Channels != 3 {
		t.Fatalf("RF64 info = %+v", info)
	}
	r, err := NewReader(p, rel, info)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	fr, err := r.Next()
	if err != nil {
		t.Fatal(err)
	}
	if string(fr.Bytes) != string(data[:9]) {
		t.Fatalf("first frame = %v", fr.Bytes)
	}
}

func TestExtensiblePCMAccepted(t *testing.T) {
	fmtChunk := make([]byte, 40)
	binary.LittleEndian.PutUint16(fmtChunk[0:], 0xFFFE) // extensible
	binary.LittleEndian.PutUint16(fmtChunk[2:], 2)
	binary.LittleEndian.PutUint32(fmtChunk[4:], 48000)
	block := 2 * 3
	binary.LittleEndian.PutUint32(fmtChunk[8:], 48000*uint32(block))
	binary.LittleEndian.PutUint16(fmtChunk[12:], uint16(block))
	binary.LittleEndian.PutUint16(fmtChunk[14:], 24)
	binary.LittleEndian.PutUint16(fmtChunk[16:], 22) // cbSize
	binary.LittleEndian.PutUint16(fmtChunk[18:], 24) // valid bits
	// Sub-format GUID begins with the little-endian format tag 1 (PCM).
	binary.LittleEndian.PutUint16(fmtChunk[24:], 1)
	data := make([]byte, block*10)

	var body []byte
	appendChunk := func(id string, payload []byte) {
		h := make([]byte, 8)
		copy(h, id)
		binary.LittleEndian.PutUint32(h[4:], uint32(len(payload)))
		body = append(body, h...)
		body = append(body, payload...)
		if len(payload)%2 == 1 {
			body = append(body, 0)
		}
	}
	appendChunk("fmt ", fmtChunk)
	appendChunk("data", data)
	hdr := make([]byte, 12)
	copy(hdr, "RIFF")
	binary.LittleEndian.PutUint32(hdr[4:], uint32(len(body)))
	copy(hdr[8:], "WAVE")
	p, rel := writeTemp(t, append(hdr, body...))
	info, err := Open(p, rel)
	if err != nil {
		t.Fatalf("extensible: %v", err)
	}
	if info.FormatTag != 1 || info.Channels != 2 || info.BitsPerSample != 24 {
		t.Fatalf("extensible info = %+v", info)
	}
}
