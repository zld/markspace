package media

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestVideoRoundTrip(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "in.vid")
	v := &Video{Width: 4, Height: 2, FPS: 5, Frames: [][]byte{
		make([]byte, 8),
		{0, 1, 2, 3, 4, 5, 6, 7},
	}}
	if err := WriteVideo(p, v); err != nil {
		t.Fatal(err)
	}
	got, err := ReadVideo(p)
	if err != nil {
		t.Fatal(err)
	}
	if got.Width != 4 || len(got.Frames) != 2 || string(got.Frames[1]) != string(v.Frames[1]) {
		t.Fatalf("roundtrip mismatch: %+v", got)
	}
}

func TestReadVideoTruncated(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "in.vid")
	v := &Video{Width: 4, Height: 2, FPS: 5, Frames: [][]byte{make([]byte, 8), make([]byte, 8)}}
	if err := WriteVideo(p, v); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(p)
	if err := os.WriteFile(p, raw[:len(raw)-3], 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadVideo(p); !errors.Is(err, ErrTruncated) {
		t.Fatalf("want ErrTruncated, got %v", err)
	}
}

func TestEncodedRoundTripAndTargetSize(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "out.enc")
	e := &Encoded{Width: 2, Height: 2, FPS: 5, Quant: 4,
		Frames: [][]byte{{0, 0, 9, 9}, {7, 7, 7, 7}}}
	const target = 512
	if err := WriteEncoded(p, e, target); err != nil {
		t.Fatal(err)
	}
	if st, _ := os.Stat(p); st.Size() != target {
		t.Fatalf("size = %d, want %d", st.Size(), target)
	}
	got, err := ReadEncoded(p)
	if err != nil {
		t.Fatal(err)
	}
	if got.Quant != 4 || string(got.Frames[0]) != string(e.Frames[0]) {
		t.Fatalf("encoded roundtrip mismatch: %+v", got)
	}
}

func TestEncodedTruncationDetected(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "out.enc")
	e := &Encoded{Width: 2, Height: 2, FPS: 5, Quant: 4, Frames: [][]byte{{1, 2, 3, 4}}}
	if err := WriteEncoded(p, e, 300); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(p)
	if err := os.WriteFile(p, raw[:len(raw)-10], 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadEncoded(p); !errors.Is(err, ErrTruncated) {
		t.Fatalf("want ErrTruncated, got %v", err)
	}
}

func TestFingerprintStable(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "in.vid")
	v := &Video{Width: 2, Height: 2, FPS: 5, Frames: [][]byte{{1, 2, 3, 4}}}
	if err := WriteVideo(p, v); err != nil {
		t.Fatal(err)
	}
	a, err := Fingerprint(p)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := Fingerprint(p)
	if a != b || len(a) != 64 {
		t.Fatal("fingerprint must be a stable sha256 hex digest")
	}
}
