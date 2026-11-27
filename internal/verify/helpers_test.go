package verify

import (
	"image"
	"image/color"
	"image/png"
	"os"
	"testing"

	"medialab/internal/pixel"
)

func encodePNG(t *testing.T, r pixel.Raw) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, r.W, r.H))
	for y := 0; y < r.H; y++ {
		for x := 0; x < r.W; x++ {
			i := (y*r.W + x) * 3
			img.SetNRGBA(x, y, color.NRGBA{R: r.Pix[i], G: r.Pix[i+1], B: r.Pix[i+2], A: 255})
		}
	}
	f, err := os.CreateTemp(t.TempDir(), "*.png")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := png.Encode(f, img); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(f.Name())
	if err != nil {
		t.Fatal(err)
	}
	return data
}
