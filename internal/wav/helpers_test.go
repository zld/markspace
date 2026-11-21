package wav

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
)

// buildWAV assembles a RIFF/WAVE byte slice from raw parts so tests can
// produce ordering and padding variations that a well-behaved writer would
// never emit.
type testChunk struct {
	id      string
	payload []byte
}

func buildWAV(chunks ...testChunk) []byte {
	var body []byte
	for _, c := range chunks {
		body = append(body, c.id...)
		var sz [4]byte
		binary.LittleEndian.PutUint32(sz[:], uint32(len(c.payload)))
		body = append(body, sz[:]...)
		body = append(body, c.payload...)
		if len(c.payload)%2 == 1 {
			body = append(body, 0)
		}
	}
	out := []byte("RIFF\x00\x00\x00\x00WAVE")
	binary.LittleEndian.PutUint32(out[4:8], uint32(4+len(body)))
	return append(out, body...)
}

func fmtChunk(tag, channels uint16, rate uint32, bits uint16) testChunk {
	align := channels * (bits / 8)
	p := make([]byte, 16)
	binary.LittleEndian.PutUint16(p[0:], tag)
	binary.LittleEndian.PutUint16(p[2:], channels)
	binary.LittleEndian.PutUint32(p[4:], rate)
	binary.LittleEndian.PutUint32(p[8:], rate*uint32(align))
	binary.LittleEndian.PutUint16(p[12:], align)
	binary.LittleEndian.PutUint16(p[14:], bits)
	return testChunk{"fmt ", p}
}

func writeTemp(t *testing.T, data []byte) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "test.wav")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write temp wav: %v", err)
	}
	return path
}

func readAll(t *testing.T, path string) (Info, []float32, error) {
	t.Helper()
	r, err := Open(path)
	if err != nil {
		return Info{}, nil, err
	}
	defer r.Close()
	frames := int(r.Info.Frames) * r.Info.Channels
	buf := make([]float32, frames)
	if r.Info.Frames > 0 {
		if _, err := r.ReadFrames(buf); err != nil && err.Error() != "EOF" {
			return r.Info, nil, err
		}
	}
	return r.Info, buf, nil
}

func int16Data(samples ...int16) []byte {
	out := make([]byte, 0, len(samples)*2)
	for _, s := range samples {
		var b [2]byte
		binary.LittleEndian.PutUint16(b[:], uint16(s))
		out = append(out, b[:]...)
	}
	return out
}
