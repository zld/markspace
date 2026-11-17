package media

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func writeY4M(t *testing.T, w, h, frames int, chroma string) []byte {
	t.Helper()
	fs := w * h * 3 / 2
	var b bytes.Buffer
	b.WriteString("YUV4MPEG2 W" + itoa(w) + " H" + itoa(h) + " F25:1 Ip A1:1 " + chroma + "\n")
	for i := 0; i < frames; i++ {
		b.WriteString("FRAME\n")
		b.Write(make([]byte, fs))
	}
	return b.Bytes()
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var s []byte
	for n > 0 {
		s = append([]byte{byte('0' + n%10)}, s...)
		n /= 10
	}
	return string(s)
}

func TestReaderValid(t *testing.T) {
	data := writeY4M(t, 16, 16, 3, "C420")
	h, frames, err := FrameBytes(data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if h.Width != 16 || h.Height != 16 || h.FrameSize != 384 {
		t.Fatalf("bad header: %+v", h)
	}
	if len(frames) != 3 {
		t.Fatalf("want 3 frames, got %d", len(frames))
	}
}

func TestReaderRejects(t *testing.T) {
	cases := map[string][]byte{
		"bad magic":   []byte("NOTY4M\n"),
		"bad width":   writeY4M(t, 16, 16, 1, "C420"),
		"bad chroma":  writeY4M(t, 16, 16, 1, "C422"),
		"odd width":   writeY4M(t, 15, 16, 1, "C420"),
	}
	cases["bad width"] = bytes.Replace(cases["bad width"], []byte("W16"), []byte("W0"), 1)
	for name, raw := range cases {
		if _, _, err := FrameBytes(raw); err == nil {
			t.Errorf("%s: expected rejection", name)
		}
	}
}

func TestReaderTruncated(t *testing.T) {
	good := writeY4M(t, 16, 16, 2, "C420")
	trunc := good[:len(good)-10]
	if _, _, err := FrameBytes(trunc); !errors.Is(err, ErrTruncated) {
		t.Fatalf("want ErrTruncated, got %v", err)
	}
}

func TestReaderEmptyEOF(t *testing.T) {
	r, err := NewReader(bytes.NewReader(writeY4M(t, 8, 8, 1, "C420")))
	if err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, r.Header().FrameSize)
	if err := r.NextFrame(buf); err != nil {
		t.Fatalf("first frame: %v", err)
	}
	if err := r.NextFrame(buf); err != io.EOF {
		t.Fatalf("want EOF, got %v", err)
	}
}

func TestDemoDeterministicAndSplit(t *testing.T) {
	dir := t.TempDir()
	p1 := filepath.Join(dir, "a.y4m")
	p2 := filepath.Join(dir, "b.y4m")
	cfg := DemoConfig{Path: p1, Width: 64, Height: 48, FPSNum: 25, FPSDen: 1,
		Segments: 3, FramesPerSegment: 10}
	n, err := GenerateDemo(cfg)
	if err != nil || n != 30 {
		t.Fatalf("generate: n=%d err=%v", n, err)
	}
	cfg.Path = p2
	if _, err := GenerateDemo(cfg); err != nil {
		t.Fatal(err)
	}
	fp1, _ := FingerprintFile(p1)
	fp2, _ := FingerprintFile(p2)
	if fp1 != fp2 {
		t.Fatal("demo output is not deterministic")
	}

	f, _ := os.Open(p1)
	defer f.Close()
	r, err := NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	refs, err := SplitToSegments(r, filepath.Join(dir, "segs"), 10)
	if err != nil {
		t.Fatalf("split: %v", err)
	}
	if len(refs) != 3 {
		t.Fatalf("want 3 segments, got %d", len(refs))
	}
}

func TestSplitShortTail(t *testing.T) {
	// 25 frames split by 10 -> segments of 10, 10, 5; last keeps real length.
	dir := t.TempDir()
	src := filepath.Join(dir, "src.y4m")
	if err := os.WriteFile(src, writeY4M(t, 16, 16, 25, "C420"), 0o644); err != nil {
		t.Fatal(err)
	}
	f, _ := os.Open(src)
	defer f.Close()
	r, _ := NewReader(f)
	refs, err := SplitToSegments(r, filepath.Join(dir, "segs"), 10)
	if err != nil {
		t.Fatal(err)
	}
	want := []int{10, 10, 5}
	if len(refs) != 3 {
		t.Fatalf("got %d refs", len(refs))
	}
	for i, ref := range refs {
		if ref.Frames != want[i] {
			t.Errorf("segment %d: want %d frames, got %d", i, want[i], ref.Frames)
		}
		_, n, err := CountFrames(ref.Path)
		if err != nil || n != want[i] {
			t.Errorf("segment %d re-read: n=%d err=%v", i, n, err)
		}
	}
}

func TestEnsureSafePaths(t *testing.T) {
	dir := t.TempDir()
	in := filepath.Join(dir, "in.y4m")
	if err := os.WriteFile(in, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	ws := filepath.Join(dir, "ws")
	if _, _, err := EnsureSafePaths(in, ws); err != nil {
		t.Fatalf("valid pair rejected: %v", err)
	}
	if _, _, err := EnsureSafePaths(in, in); err == nil {
		t.Fatal("same path should be rejected")
	}
	if _, _, err := EnsureSafePaths(in, filepath.Join(ws, "..")); err == nil {
		t.Fatal("workspace equal to input dir via .. should be rejected")
	}
}
