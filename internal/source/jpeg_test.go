package source_test

import (
	"image"
	"image/color"
	"image/jpeg"
	"os"
	"path/filepath"
	"testing"

	"mediarepair/internal/source"
)

func TestJPEGDecodeSupported(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "frames"), 0o755); err != nil {
		t.Fatal(err)
	}
	img := image.NewRGBA(image.Rect(0, 0, 8, 8))
	img.SetRGBA(0, 0, color.RGBA{R: 10, G: 20, B: 30, A: 255})
	path := filepath.Join(root, "frames", "small.jpg")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := jpeg.Encode(f, img, &jpeg.Options{Quality: 90}); err != nil {
		_ = f.Close()
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if format, ok := source.FormatForPath(path); !ok || format != "jpeg" {
		t.Fatalf("format=%q ok=%v", format, ok)
	}
	decoded, format, err := source.DecodeFrame(root, "frames/small.jpg")
	if err != nil {
		t.Fatal(err)
	}
	if format != "jpeg" || decoded.Bounds().Dx() != 8 {
		t.Fatalf("format=%s bounds=%v", format, decoded.Bounds())
	}
}
