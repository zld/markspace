package media

import (
	"path/filepath"
	"testing"
)

func TestEncodeTargetConverges(t *testing.T) {
	tool := requireFFmpeg(t)
	dir := t.TempDir()
	syn := Synthetic{Width: 160, Height: 90, FPS: 10, Seconds: 2, Seed: 3}
	ref, _, err := tool.Generate(dir, syn)
	if err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "out.h264")
	ft, err := tool.EncodeTarget(ref, out, "h264", syn, 300, "ultrafast")
	if err != nil {
		t.Fatal(err)
	}
	if ft.Iterations == 0 || len(ft.Passes) == 0 {
		t.Fatalf("fine tune = %+v", ft)
	}
	last := ft.Passes[len(ft.Passes)-1]
	dev := last.Kbps - 300
	if dev < 0 {
		dev = -dev
	}
	if dev/300 > 0.10 {
		t.Fatalf("final rate %.1f deviates %.1f%%", last.Kbps, dev/3)
	}
}

func TestEncodeTargetRejectsZero(t *testing.T) {
	tool := requireFFmpeg(t)
	if _, err := tool.EncodeTarget("x", "y", "av1", Synthetic{Width: 2, Height: 2, FPS: 1, Seconds: 1}, 0, ""); err == nil {
		t.Fatal("zero target must fail")
	}
}

func TestEncodeDegradedSmaller(t *testing.T) {
	tool := requireFFmpeg(t)
	dir := t.TempDir()
	syn := Synthetic{Width: 160, Height: 90, FPS: 10, Seconds: 1, Seed: 3}
	ref, _, err := tool.Generate(dir, syn)
	if err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "deg.h264")
	if err := tool.EncodeDegraded(ref, out, "h264", syn, 40); err != nil {
		t.Fatal(err)
	}
}
