package wav

import (
	"encoding/binary"
	"math"
	"testing"
)

func TestReadPCM16(t *testing.T) {
	data := int16Data(0, 32767, -32768, 16384)
	path := writeTemp(t, buildWAV(fmtChunk(FormatPCM, 2, 48000, 16), testChunk{"data", data}))

	info, buf, err := readAll(t, path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if info.Channels != 2 || info.SampleRate != 48000 || info.Bits != 16 {
		t.Fatalf("unexpected info: %+v", info)
	}
	if info.Frames != 2 {
		t.Fatalf("frames = %d, want 2", info.Frames)
	}
	// 32767/32768 is one LSB shy of full scale, as in BS.1770 calibration.
	want := []float64{0, 32767.0 / 32768.0, -1, 0.5}
	for i, w := range want {
		if math.Abs(float64(buf[i])-w) > 1e-9 {
			t.Errorf("sample %d = %v, want %v", i, buf[i], w)
		}
	}
}

func TestReadPCM24SignExtension(t *testing.T) {
	// +1 LSB, -1 LSB, and the most negative value in 24-bit two's complement.
	payload := []byte{
		0x01, 0x00, 0x00,
		0xFF, 0xFF, 0xFF,
		0x00, 0x00, 0x80,
	}
	path := writeTemp(t, buildWAV(fmtChunk(FormatPCM, 1, 44100, 24), testChunk{"data", payload}))
	info, buf, err := readAll(t, path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if info.Frames != 3 {
		t.Fatalf("frames = %d", info.Frames)
	}
	near := func(got float32, want float64) bool {
		return math.Abs(float64(got)-want) < 1e-9
	}
	if !near(buf[0], 1.0/8388608.0) {
		t.Errorf("+1 LSB = %v", buf[0])
	}
	if !near(buf[1], -1.0/8388608.0) {
		t.Errorf("-1 LSB = %v", buf[1])
	}
	if buf[2] != -1 {
		t.Errorf("min value = %v, want -1", buf[2])
	}
}

func TestReadPCM32(t *testing.T) {
	p := make([]byte, 12)
	binary.LittleEndian.PutUint32(p[0:], 0)
	binary.LittleEndian.PutUint32(p[4:], uint32(int32(math.MaxInt32)))
	binary.LittleEndian.PutUint32(p[8:], 0x80000000)
	path := writeTemp(t, buildWAV(fmtChunk(FormatPCM, 1, 96000, 32), testChunk{"data", p}))
	_, buf, err := readAll(t, path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if buf[0] != 0 || math.Abs(float64(buf[1])-1) > 1e-9 || buf[2] != -1 {
		t.Fatalf("pcm32 decode = %v", buf)
	}
}

func TestReadFloat32(t *testing.T) {
	p := make([]byte, 12)
	vals := []float32{0.25, -0.75, 1.5}
	for i, v := range vals {
		binary.LittleEndian.PutUint32(p[i*4:], math.Float32bits(v))
	}
	path := writeTemp(t, buildWAV(fmtChunk(FormatFloat, 1, 48000, 32), testChunk{"data", p}))
	_, buf, err := readAll(t, path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	for i, v := range vals {
		if buf[i] != v {
			t.Errorf("sample %d = %v, want %v", i, buf[i], v)
		}
	}
}

func TestChunkOrderAndOddUnknownChunk(t *testing.T) {
	// data before fmt must still work, and an odd-sized JUNK chunk must be
	// padded and skipped without shifting the sample data.
	data := int16Data(100, -100, 200, -200)
	path := writeTemp(t, buildWAV(
		testChunk{"JUNK", []byte{1, 2, 3}},
		testChunk{"data", data},
		fmtChunk(FormatPCM, 2, 48000, 16),
	))
	info, buf, err := readAll(t, path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if info.Frames != 2 {
		t.Fatalf("frames = %d, want 2", info.Frames)
	}
	if buf[0] <= 0 || buf[1] >= 0 {
		t.Fatalf("samples look misaligned: %v", buf)
	}
}

func TestExtensibleSubFormat(t *testing.T) {
	p := make([]byte, 40)
	binary.LittleEndian.PutUint16(p[0:], FormatExtensible)
	binary.LittleEndian.PutUint16(p[2:], 2)
	binary.LittleEndian.PutUint32(p[4:], 48000)
	binary.LittleEndian.PutUint32(p[8:], 48000*4)
	binary.LittleEndian.PutUint16(p[12:], 4)
	binary.LittleEndian.PutUint16(p[14:], 16)
	binary.LittleEndian.PutUint16(p[16:], 22)
	binary.LittleEndian.PutUint16(p[18:], 16)
	binary.LittleEndian.PutUint32(p[20:], 3)
	binary.LittleEndian.PutUint32(p[24:], uint32(FormatPCM))
	copy(p[28:], subFormatSuffix[:])
	path := writeTemp(t, buildWAV(testChunk{"fmt ", p}, testChunk{"data", int16Data(1, 2)}))
	info, _, err := readAll(t, path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if info.Format != FormatPCM || info.Channels != 2 {
		t.Fatalf("extensible parse = %+v", info)
	}
}
