package audio

import (
	"encoding/binary"
	"math"
	"os"
	"path/filepath"
	"testing"
)

// buildWAV assembles a minimal RIFF/WAVE byte stream for tests.
func buildWAV(format, channels, rate, bits int, data []byte) []byte {
	blockAlign := channels * bits / 8
	out := make([]byte, 0, 44+len(data))
	out = append(out, "RIFF"...)
	out = binary.LittleEndian.AppendUint32(out, uint32(36+len(data)))
	out = append(out, "WAVE"...)
	out = append(out, "fmt "...)
	out = binary.LittleEndian.AppendUint32(out, 16)
	out = binary.LittleEndian.AppendUint16(out, uint16(format))
	out = binary.LittleEndian.AppendUint16(out, uint16(channels))
	out = binary.LittleEndian.AppendUint32(out, uint32(rate))
	out = binary.LittleEndian.AppendUint32(out, uint32(rate*blockAlign))
	out = binary.LittleEndian.AppendUint16(out, uint16(blockAlign))
	out = binary.LittleEndian.AppendUint16(out, uint16(bits))
	out = append(out, "data"...)
	out = binary.LittleEndian.AppendUint32(out, uint32(len(data)))
	return append(out, data...)
}

func writeTemp(t *testing.T, b []byte) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "test.wav")
	if err := os.WriteFile(p, b, 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func readAll(t *testing.T, r *Reader) [][]float64 {
	t.Helper()
	var out [][]float64
	for {
		blk, err := r.ReadBlock(7) // odd size to exercise blocking
		if err != nil {
			t.Fatal(err)
		}
		if blk == nil {
			return out
		}
		if out == nil {
			out = make([][]float64, len(blk))
		}
		for c := range blk {
			out[c] = append(out[c], blk[c]...)
		}
	}
}

func TestOpenPCM16(t *testing.T) {
	data := make([]byte, 0, 8)
	for _, v := range []int16{0, 32767, -32768, 16384} {
		data = binary.LittleEndian.AppendUint16(data, uint16(v))
	}
	p := writeTemp(t, buildWAV(FormatPCM, 2, 48000, 16, data))
	r, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	info := r.Info()
	if info.Channels != 2 || info.SampleRate != 48000 || info.Frames != 2 || info.BitDepth != 16 {
		t.Fatalf("bad info: %+v", info)
	}
	got := readAll(t, r)
	want := [][]float64{{0, -1}, {32767.0 / 32768, 0.5}}
	for c := range want {
		for i := range want[c] {
			if math.Abs(got[c][i]-want[c][i]) > 1e-9 {
				t.Errorf("ch%d[%d] = %v, want %v", c, i, got[c][i], want[c][i])
			}
		}
	}
}

func TestOpenPCM24(t *testing.T) {
	// mono, one frame: 0x7FFFFF (max) then 0x800000 (min)
	data := []byte{0xFF, 0xFF, 0x7F, 0x00, 0x00, 0x80}
	p := writeTemp(t, buildWAV(FormatPCM, 1, 44100, 24, data))
	r, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	got := readAll(t, r)
	if got[0][0] != float64(8388607)/8388608 || got[0][1] != -1 {
		t.Errorf("24-bit decode wrong: %v", got[0])
	}
}

func TestOpenPCM32(t *testing.T) {
	data := binary.LittleEndian.AppendUint32(nil, 0x80000000) // int32 min
	data = binary.LittleEndian.AppendUint32(data, uint32(int32(1073741824)))
	p := writeTemp(t, buildWAV(FormatPCM, 1, 48000, 32, data))
	r, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	got := readAll(t, r)
	if got[0][0] != -1 || got[0][1] != 0.5 {
		t.Errorf("32-bit decode wrong: %v", got[0])
	}
}

func TestOpenFloat32(t *testing.T) {
	data := binary.LittleEndian.AppendUint32(nil, math.Float32bits(0.25))
	data = binary.LittleEndian.AppendUint32(data, math.Float32bits(-0.5))
	p := writeTemp(t, buildWAV(FormatFloat, 1, 48000, 32, data))
	r, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	got := readAll(t, r)
	if got[0][0] != 0.25 || got[0][1] != -0.5 {
		t.Errorf("float decode wrong: %v", got[0])
	}
}

func TestRejectBadFiles(t *testing.T) {
	good := buildWAV(FormatPCM, 1, 48000, 16, make([]byte, 4))
	cases := map[string][]byte{
		"bad magic":         append([]byte("NOPE"), good[4:]...),
		"truncated data":    good[:len(good)-2],
		"unsupported codec": buildWAV(2, 1, 48000, 16, make([]byte, 4)),
		"8-bit pcm":         buildWAV(FormatPCM, 1, 48000, 8, make([]byte, 4)),
		"misaligned data":   buildWAV(FormatPCM, 2, 48000, 16, make([]byte, 6)),
		"zero channels":     buildWAV(FormatPCM, 0, 48000, 16, nil),
		"empty":             {},
	}
	for name, b := range cases {
		if _, err := Open(writeTemp(t, b)); err == nil {
			t.Errorf("%s: expected error, got nil", name)
		}
	}
}

func TestSkipsUnknownChunks(t *testing.T) {
	wav := buildWAV(FormatPCM, 1, 48000, 16, make([]byte, 4))
	// Insert an odd-sized JUNK chunk before fmt.
	junk := append([]byte("JUNK"), 0, 0, 0, 3, 'a', 'b', 'c', 0)
	junk[4], junk[7] = 3, 0
	b := append(wav[:12], append(junk, wav[12:]...)...)
	binary.LittleEndian.PutUint32(b[4:8], uint32(len(b)-8))
	p := writeTemp(t, b)
	r, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if r.Info().Frames != 2 {
		t.Errorf("frames = %d, want 2", r.Info().Frames)
	}
}
