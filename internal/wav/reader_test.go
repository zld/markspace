package wav

import (
	"bytes"
	"encoding/binary"
	"testing"

	"replaycheck/internal/fault"
)

func TestParseStereo(t *testing.T) {
	rate := 8000
	frames := 100
	data := make([]byte, frames*2*2)
	for i := 0; i < frames; i++ {
		binary.LittleEndian.PutUint16(data[i*4:], uint16(int16(1000)))
		binary.LittleEndian.PutUint16(data[i*4+2:], 0x8000) // -32768 on the right channel
	}
	img := BuildPCM(2, rate, 16, data)
	lay, err := Parse(bytes.NewReader(img), int64(len(img)))
	if err != nil {
		t.Fatal(err)
	}
	if lay.Format.Channels != 2 || lay.Format.SampleRate != rate || lay.Format.BitsPerSam != 16 {
		t.Fatalf("unexpected format %+v", lay.Format)
	}
	if lay.Frames != int64(frames) {
		t.Fatalf("frames = %d, want %d", lay.Frames, frames)
	}
	if lay.Truncated || lay.Issues.Len() != 0 {
		t.Fatalf("clean file reported issues: %+v", lay.Issues)
	}
	peak, err := lay.Peak(bytes.NewReader(img), 0, int64(frames))
	if err != nil {
		t.Fatal(err)
	}
	if peak < 0.999 || peak > 1.0 {
		t.Fatalf("peak = %f, want ~1.0", peak)
	}
}

func TestTruncatedDataChunkReported(t *testing.T) {
	img := BuildPCM(1, 48000, 16, make([]byte, 480*2))
	// Cut the image, leaving a data chunk header claiming more than present.
	cut := img[:len(img)-100]
	lay, err := Parse(bytes.NewReader(cut), int64(len(cut)))
	if err != nil {
		t.Fatal(err)
	}
	if !lay.Truncated {
		t.Fatal("expected truncation")
	}
	if lay.Frames != 430 {
		t.Fatalf("frames = %d, want 430", lay.Frames)
	}
	if lay.PartialTail != 0 {
		t.Fatalf("partial tail = %d, want 0", lay.PartialTail)
	}
	iss, ok := lay.Issues.First(fault.KindTruncated)
	if !ok {
		t.Fatalf("missing truncation issue: %v", &lay.Issues)
	}
	if iss.Where.ByteOffset != int64(len(cut)) {
		t.Fatalf("first bad offset = %d, want %d", iss.Where.ByteOffset, len(cut))
	}
}

func TestPartialTrailingFrame(t *testing.T) {
	img := BuildPCM(2, 48000, 24, make([]byte, 10*6))
	img = append(img, 0xAA, 0xBB, 0xCC) // 3 stray bytes
	// Patch the declared sizes to match the actual bytes so the only defect
	// is the incomplete final frame.
	binary.LittleEndian.PutUint32(img[40:44], 63)
	binary.LittleEndian.PutUint32(img[4:8], uint32(len(img)-8))
	lay, err := Parse(bytes.NewReader(img), int64(len(img)))
	if err != nil {
		t.Fatal(err)
	}
	if lay.Frames != 10 {
		t.Fatalf("frames = %d, want 10", lay.Frames)
	}
	if lay.PartialTail != 3 {
		t.Fatalf("partial tail = %d, want 3", lay.PartialTail)
	}
	iss, ok := lay.Issues.First(fault.KindPartialFrame)
	if !ok {
		t.Fatalf("missing partial-frame issue: %v", &lay.Issues)
	}
	wantOff := int64(44 + 60)
	if iss.Where.ByteOffset != wantOff {
		t.Fatalf("partial tail offset = %d, want %d", iss.Where.ByteOffset, wantOff)
	}
	if !iss.Where.HasTick || iss.Where.Tick != 10 {
		t.Fatalf("partial tail tick = %+v, want tick 10", iss.Where)
	}
}

func TestBadBlockAlignRejected(t *testing.T) {
	img := BuildPCM(2, 48000, 16, make([]byte, 16))
	binary.LittleEndian.PutUint16(img[32:34], 3) // invalid block align
	if _, err := Parse(bytes.NewReader(img), int64(len(img))); !fault.IsKind(err, fault.KindBadHeader) {
		t.Fatalf("err = %v, want bad-header", err)
	}
}

func TestZeroChannelsRejected(t *testing.T) {
	img := BuildPCM(1, 48000, 16, make([]byte, 16))
	binary.LittleEndian.PutUint16(img[22:24], 0)
	binary.LittleEndian.PutUint16(img[32:34], 2)
	binary.LittleEndian.PutUint32(img[28:32], 48000*2)
	if _, err := Parse(bytes.NewReader(img), int64(len(img))); !fault.IsKind(err, fault.KindBadHeader) {
		t.Fatalf("err = %v, want bad-header", err)
	}
}

func TestReadFramesRangeCheck(t *testing.T) {
	img := BuildPCM(1, 8000, 16, make([]byte, 20))
	lay, err := Parse(bytes.NewReader(img), int64(len(img)))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := lay.ReadFrames(bytes.NewReader(img), 5, 10); !fault.IsKind(err, fault.KindOutOfRange) {
		t.Fatalf("err = %v, want out-of-range", err)
	}
	got, err := lay.ReadFrames(bytes.NewReader(img), 0, 10)
	if err != nil || len(got) != 20 {
		t.Fatalf("read = %d bytes, %v", len(got), err)
	}
}

func TestFloat32Peak(t *testing.T) {
	data := make([]byte, 4*3)
	binary.LittleEndian.PutUint32(data[0:], 0xBF000000) // -0.5
	binary.LittleEndian.PutUint32(data[4:], 0x3F800000) // 1.0
	binary.LittleEndian.PutUint32(data[8:], 0)          // 0
	img := BuildPCM(1, 8000, 32, data)
	binary.LittleEndian.PutUint16(img[20:22], 3) // IEEE float
	lay, err := Parse(bytes.NewReader(img), int64(len(img)))
	if err != nil {
		t.Fatal(err)
	}
	if peak, _ := lay.Peak(bytes.NewReader(img), 0, 3); peak != 1.0 {
		t.Fatalf("peak = %f, want 1.0", peak)
	}
}

func TestEightBitPeak(t *testing.T) {
	img := BuildPCM(2, 8000, 8, []byte{0x80, 0xFF, 0x00, 0x80})
	lay, err := Parse(bytes.NewReader(img), int64(len(img)))
	if err != nil {
		t.Fatal(err)
	}
	if lay.Frames != 2 {
		t.Fatalf("frames = %d", lay.Frames)
	}
	if peak, _ := lay.Peak(bytes.NewReader(img), 0, 2); peak < 0.99 {
		t.Fatalf("peak = %f, want ~1.0", peak)
	}
}

func TestOddChunkPadding(t *testing.T) {
	// Add an odd-sized LIST chunk before data; the parser must skip its pad.
	var b bytes.Buffer
	img := BuildPCM(1, 8000, 16, make([]byte, 8))
	b.Write(img[0:36]) // RIFF/WAVE + fmt
	b.WriteString("LIST")
	binary.Write(&b, binary.LittleEndian, uint32(3))
	b.Write([]byte{'a', 'b', 'c', 0})
	b.Write(img[36:])
	full := b.Bytes()
	binary.LittleEndian.PutUint32(full[4:8], uint32(len(full)-8))
	lay, err := Parse(bytes.NewReader(full), int64(len(full)))
	if err != nil {
		t.Fatal(err)
	}
	if lay.Frames != 4 {
		t.Fatalf("frames = %d, want 4", lay.Frames)
	}
}

func TestSixChannelPCM(t *testing.T) {
	const ch = 6
	frames := 200
	data := make([]byte, frames*ch*2)
	for i := 0; i < frames; i++ {
		for c := 0; c < ch; c++ {
			v := int16(1000 * (c + 1))
			binary.LittleEndian.PutUint16(data[(i*ch+c)*2:], uint16(v))
		}
	}
	img := BuildPCM(ch, 48000, 16, data)
	lay, err := Parse(bytes.NewReader(img), int64(len(img)))
	if err != nil {
		t.Fatal(err)
	}
	if lay.Format.Channels != ch || lay.Format.BlockAlign != ch*2 {
		t.Fatalf("format = %+v", lay.Format)
	}
	if lay.Frames != int64(frames) {
		t.Fatalf("frames = %d, want %d", lay.Frames, frames)
	}
	got, err := lay.ReadFrames(bytes.NewReader(img), 10, 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 5*ch*2 {
		t.Fatalf("frame read = %d bytes, want %d", len(got), 5*ch*2)
	}
	// Channel 5 (the sixth) holds 6000, proving interleaving is preserved.
	first := int16(binary.LittleEndian.Uint16(got[5*2:]))
	if first != 6000 {
		t.Fatalf("sixth channel sample = %d, want 6000", first)
	}
	peak, err := lay.Peak(bytes.NewReader(img), 0, int64(frames))
	if err != nil {
		t.Fatal(err)
	}
	want := 6000.0 / 32768.0
	if peak < want-1e-9 || peak > want+1e-9 {
		t.Fatalf("peak = %f, want %f", peak, want)
	}
}

func TestRiffSizeBeyondFileReported(t *testing.T) {
	// A RIFF header promising more bytes than exist means the file was cut
	// short after a complete data chunk; that must be reported, not ignored.
	img := BuildPCM(1, 8000, 16, make([]byte, 100))
	binary.LittleEndian.PutUint32(img[4:8], uint32(len(img)+500-8))
	lay, err := Parse(bytes.NewReader(img), int64(len(img)))
	if err != nil {
		t.Fatal(err)
	}
	if !lay.Truncated {
		t.Fatal("expected truncation from short RIFF")
	}
	iss, ok := lay.Issues.First(fault.KindTruncated)
	if !ok {
		t.Fatalf("missing truncation issue: %v", &lay.Issues)
	}
	if iss.Where.ByteOffset != int64(len(img)) {
		t.Fatalf("bad offset = %d, want %d", iss.Where.ByteOffset, len(img))
	}
}
