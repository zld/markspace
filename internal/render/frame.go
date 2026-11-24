// Package render produces deterministic offscreen frames and their evidence
// hashes. Frames are generated from a smooth synthetic field, transformed with
// the exact crop and rotation resolved for the track, and encoded as PNG.
package render

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"math"

	"archive/internal/clock"
	"archive/internal/normalize"
)

// LosslessPSNRCap is reported when two frames are identical. Encoding a PNG is
// lossless, so an identical render cannot produce a finite PSNR.
const LosslessPSNRCap = 99.0

// Seed derives a stable per-track seed from its identifier.
func Seed(trackID string) uint64 {
	sum := sha256.Sum256([]byte(trackID))
	var v uint64
	for i := 0; i < 8; i++ {
		v = v<<8 | uint64(sum[i])
	}
	return v
}

// Reference builds the canonical source frame for a track, frame index and
// size. Content is a smooth deterministic field so repeated runs and lossy
// reconstructions stay comparable.
func Reference(width, height int, seed uint64, frame int) (*image.RGBA, error) {
	if width <= 0 || height <= 0 {
		return nil, clock.Newf(clock.KindOutOfRange, "frame size %dx%d is not positive", width, height)
	}
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	off := int(seed%31)*3 + frame*5
	wx := float64(width - 1)
	hy := float64(height - 1)
	if wx <= 0 {
		wx = 1
	}
	if hy <= 0 {
		hy = 1
	}
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			r := uint8((float64(x)/wx*200 + float64(off%64)))
			g := uint8((float64(y)/hy*200 + float64((off/3)%64)))
			b := uint8((float64(x+y)/(wx+hy)*180 + float64((off/7)%76)))
			img.SetRGBA(x, y, color.RGBA{R: r, G: g, B: b, A: 255})
		}
	}
	return img, nil
}

// ApplyTransform crops then rotates the source frame. The result is always a
// new image; the input is not modified.
func ApplyTransform(src *image.RGBA, res *normalize.Resolution) (*image.RGBA, error) {
	if src == nil {
		return nil, clock.New(clock.KindInvalidMetadata, "nil source frame")
	}
	b := src.Bounds()
	crop := res.Crop
	if crop.Left < 0 || crop.Top < 0 || crop.Right > b.Dx() || crop.Bottom > b.Dy() || crop.Empty() {
		return nil, clock.Newf(clock.KindOutOfRange,
			"crop [%d,%d,%d,%d] does not fit source %dx%d",
			crop.Left, crop.Right, crop.Top, crop.Bottom, b.Dx(), b.Dy())
	}
	cw, ch := crop.Width(), crop.Height()
	cropped := image.NewRGBA(image.Rect(0, 0, cw, ch))
	for y := 0; y < ch; y++ {
		copyRow(cropped, y, src, crop.Top+y, crop.Left, cw)
	}
	switch res.Rotation {
	case 0:
		return cropped, nil
	case 90:
		return rotate90(cropped), nil
	case 180:
		return rotate180(cropped), nil
	case 270:
		return rotate270(cropped), nil
	default:
		return nil, clock.Newf(clock.KindUnsupported, "rotation %d is not a right angle", res.Rotation)
	}
}

func copyRow(dst *image.RGBA, dy int, src *image.RGBA, sy, sx, n int) {
	for x := 0; x < n; x++ {
		si := src.PixOffset(sx+x, sy)
		di := dst.PixOffset(x, dy)
		copy(dst.Pix[di:di+4], src.Pix[si:si+4])
	}
}

func rotate90(src *image.RGBA) *image.RGBA {
	w, h := src.Bounds().Dx(), src.Bounds().Dy()
	dst := image.NewRGBA(image.Rect(0, 0, h, w))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			// Clockwise: (x,y) -> (h-1-y, x)
			copyPixel(dst, h-1-y, x, src, x, y)
		}
	}
	return dst
}

func rotate180(src *image.RGBA) *image.RGBA {
	w, h := src.Bounds().Dx(), src.Bounds().Dy()
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			copyPixel(dst, w-1-x, h-1-y, src, x, y)
		}
	}
	return dst
}

func rotate270(src *image.RGBA) *image.RGBA {
	w, h := src.Bounds().Dx(), src.Bounds().Dy()
	dst := image.NewRGBA(image.Rect(0, 0, h, w))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			// Counter-clockwise: (x,y) -> (y, w-1-x)
			copyPixel(dst, y, w-1-x, src, x, y)
		}
	}
	return dst
}

func copyPixel(dst *image.RGBA, dx, dy int, src *image.RGBA, sx, sy int) {
	si := src.PixOffset(sx, sy)
	di := dst.PixOffset(dx, dy)
	copy(dst.Pix[di:di+4], src.Pix[si:si+4])
}

// EncodePNG serialises the image with Go's default encoder, which emits no
// timestamps or other non-deterministic metadata.
func EncodePNG(img *image.RGBA) ([]byte, error) {
	if img == nil {
		return nil, clock.New(clock.KindInvalidMetadata, "nil image")
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil, clock.New(clock.KindUnsupported, "png encode failed").WithCause(err)
	}
	return buf.Bytes(), nil
}

// HashBytes returns the lowercase hex SHA-256 of b.
func HashBytes(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// PSNR compares two equally sized RGB frames. Identical frames return the
// documented cap.
func PSNR(a, b *image.RGBA) (float64, error) {
	ab, bb := a.Bounds(), b.Bounds()
	if ab.Dx() != bb.Dx() || ab.Dy() != bb.Dy() {
		return 0, clock.Newf(clock.KindMisaligned, "psnr size mismatch %dx%d vs %dx%d",
			ab.Dx(), ab.Dy(), bb.Dx(), bb.Dy())
	}
	var mse float64
	n := 0
	for y := 0; y < ab.Dy(); y++ {
		for x := 0; x < ab.Dx(); x++ {
			ai, bi := a.PixOffset(x, y), b.PixOffset(x, y)
			for c := 0; c < 3; c++ {
				d := float64(a.Pix[ai+c]) - float64(b.Pix[bi+c])
				mse += d * d
				n++
			}
		}
	}
	if n == 0 {
		return LosslessPSNRCap, nil
	}
	mse /= float64(n)
	if mse == 0 {
		return LosslessPSNRCap, nil
	}
	return 10 * math.Log10(255*255/mse), nil
}

// PSNRGray compares two luma planes.
func PSNRGray(a, b []uint8) (float64, error) {
	if len(a) != len(b) || len(a) == 0 {
		return 0, clock.Newf(clock.KindMisaligned, "psnr plane mismatch %d vs %d", len(a), len(b))
	}
	var mse float64
	for i := range a {
		d := float64(a[i]) - float64(b[i])
		mse += d * d
	}
	mse /= float64(len(a))
	if mse == 0 {
		return LosslessPSNRCap, nil
	}
	return 10 * math.Log10(255*255/mse), nil
}

// Luma extracts a deterministic grayscale plane using integer weights.
func Luma(img *image.RGBA) []uint8 {
	w, h := img.Bounds().Dx(), img.Bounds().Dy()
	out := make([]uint8, w*h)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			i := img.PixOffset(x, y)
			r, g, b := int(img.Pix[i]), int(img.Pix[i+1]), int(img.Pix[i+2])
			out[y*w+x] = uint8((299*r + 587*g + 114*b) / 1000)
		}
	}
	return out
}

// Downsample averages blocks of the source plane into a target grid. It is
// used to derive a representation-layer proxy from a full-size frame.
func Downsample(src []uint8, sw, sh, dw, dh int) ([]uint8, error) {
	if sw <= 0 || sh <= 0 || dw <= 0 || dh <= 0 || len(src) != sw*sh {
		return nil, clock.Newf(clock.KindOutOfRange, "bad downsample geometry %dx%d -> %dx%d (len %d)",
			sw, sh, dw, dh, len(src))
	}
	out := make([]uint8, dw*dh)
	for y := 0; y < dh; y++ {
		y0, y1 := y*sh/dh, (y+1)*sh/dh
		if y1 <= y0 {
			y1 = y0 + 1
		}
		for x := 0; x < dw; x++ {
			x0, x1 := x*sw/dw, (x+1)*sw/dw
			if x1 <= x0 {
				x1 = x0 + 1
			}
			sum, n := 0, 0
			for yy := y0; yy < y1 && yy < sh; yy++ {
				for xx := x0; xx < x1 && xx < sw; xx++ {
					sum += int(src[yy*sw+xx])
					n++
				}
			}
			if n == 0 {
				n = 1
			}
			out[y*dw+x] = uint8(sum / n)
		}
	}
	return out, nil
}

// Crop returns the resolved crop rectangle as four ints for the manifest.
func Crop(res *normalize.Resolution) [4]int {
	return [4]int{res.Crop.Left, res.Crop.Right, res.Crop.Top, res.Crop.Bottom}
}

// TransformLabel renders a stable description of the applied transform.
func TransformLabel(res *normalize.Resolution) string {
	return fmt.Sprintf("crop=%d,%d,%d,%d;rot=%d", res.Crop.Left, res.Crop.Right,
		res.Crop.Top, res.Crop.Bottom, res.Rotation)
}
