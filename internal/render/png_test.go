package render

import (
	"bytes"
	"image"
	"image/png"
	"testing"
)

func TestEncodePNGDecodesIdentically(t *testing.T) {
	cases := []struct{ w, h int }{{1, 1}, {7, 3}, {64, 48}}
	for _, c := range cases {
		img := image.NewRGBA(image.Rect(0, 0, c.w, c.h))
		for i := range img.Pix {
			img.Pix[i] = uint8(i*31 + 7)
		}
		raw, err := EncodePNG(img, 1)
		if err != nil {
			t.Fatal(err)
		}
		dec, err := png.Decode(bytes.NewReader(raw))
		if err != nil {
			t.Fatalf("%dx%d decode: %v", c.w, c.h, err)
		}
		b := dec.Bounds()
		if b.Dx() != c.w || b.Dy() != c.h {
			t.Fatalf("bounds %v", b)
		}
		for y := 0; y < c.h; y++ {
			for x := 0; x < c.w; x++ {
				// A PNG stores straight alpha; normalize the source the same
				// way the encoder does and compare the decoded bytes.
				got := dec.(*image.NRGBA)
				j := y*got.Stride + x*4
				want := toNRGBA(img)
				k := y*want.Stride + x*4
				if got.Pix[j] != want.Pix[k] || got.Pix[j+1] != want.Pix[k+1] ||
					got.Pix[j+2] != want.Pix[k+2] || got.Pix[j+3] != want.Pix[k+3] {
					t.Fatalf("%dx%d pixel %d,%d mismatch", c.w, c.h, x, y)
				}
			}
		}
		again, _ := EncodePNG(img, 1)
		if !bytes.Equal(raw, again) {
			t.Fatal("encoding must be deterministic")
		}
	}
}

func BenchmarkEncodePNG1080p(b *testing.B) {
	img := image.NewRGBA(image.Rect(0, 0, 1920, 1080))
	for i := range img.Pix {
		img.Pix[i] = uint8(i*7 + i>>11)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := EncodePNG(img, 1); err != nil {
			b.Fatal(err)
		}
	}
}
