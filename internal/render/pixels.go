package render

import (
	"encoding/hex"

	"mediapack/internal/ingest"
)

// rgbaImage is a plain RGBA pixel buffer matching the fixed PixelFormat.
type rgbaImage struct {
	Pix  []uint8
	W, H int
}

// newRGBA allocates an opaque-capable RGBA buffer.
func newRGBA(w, h int) *rgbaImage {
	return &rgbaImage{Pix: make([]uint8, 4*w*h), W: w, H: h}
}

func (im *rgbaImage) set(x, y int, r, g, b, a uint8) {
	i := 4 * (y*im.W + x)
	im.Pix[i], im.Pix[i+1], im.Pix[i+2], im.Pix[i+3] = r, g, b, a
}

func (im *rgbaImage) at(x, y int) (r, g, b, a uint8) {
	i := 4 * (y*im.W + x)
	return im.Pix[i], im.Pix[i+1], im.Pix[i+2], im.Pix[i+3]
}

// digestSeed decodes the renderer digest once into mixing bytes.
func digestSeed(digest string) []byte {
	if b, err := hex.DecodeString(digest); err == nil {
		return b
	}
	return []byte(digest)
}

// mix32 is a small deterministic integer mixer (no math/rand, so output never
// depends on global generator state).
func mix32(h, v uint32) uint32 {
	h ^= v
	h *= 0x01000193 // FNV prime
	h ^= h >> 13
	h *= 0x85EBCA6B
	h ^= h >> 16
	return h
}

// drawSource fills a frame-sized image from (input digest, track, sample,
// position). The x/y stripe terms make the content asymmetric so rotation and
// cropping produce distinguishable outputs.
func drawSource(t *ingest.Track, sample int, digest string) *rgbaImage {
	w, h := int(t.Width), int(t.Height)
	im := newRGBA(w, h)
	seed := digestSeed(digest)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			hv := uint32(0x811C9DC5)
			for _, b := range seed {
				hv = mix32(hv, uint32(b))
			}
			hv = mix32(hv, uint32(t.Index))
			hv = mix32(hv, uint32(sample))
			hv = mix32(hv, uint32(x))
			hv = mix32(hv, uint32(y))
			im.set(x, y,
				byte(hv)^uint8(x&0xFF),
				byte(hv>>8),
				byte(hv>>16)^uint8(y&0xFF),
				255)
		}
	}
	return im
}

// applyCrop keeps the clean aperture when the track retained a legal crop;
// otherwise the full aperture is returned untouched.
func applyCrop(src *rgbaImage, t *ingest.Track) *rgbaImage {
	if t.Crop == nil {
		return src
	}
	cx, cy, cw, ch := t.Crop.EffectiveRect(uint32(t.Width), uint32(t.Height))
	x0, y0 := int(cx), int(cy)
	if x0 < 0 {
		x0 = 0
	}
	if y0 < 0 {
		y0 = 0
	}
	w, h := int(cw), int(ch)
	if x0+w > src.W {
		w = src.W - x0
	}
	if y0+h > src.H {
		h = src.H - y0
	}
	if w <= 0 || h <= 0 {
		return src
	}
	out := newRGBA(w, h)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			r, g, b, a := src.at(x0+x, y0+y)
			out.set(x, y, r, g, b, a)
		}
	}
	return out
}

// applyRotation rotates clockwise by the track's explicit 90-multiple angle.
func applyRotation(src *rgbaImage, degrees int) *rgbaImage {
	switch degrees {
	case 90:
		out := newRGBA(src.H, src.W)
		for y := 0; y < src.H; y++ {
			for x := 0; x < src.W; x++ {
				r, g, b, a := src.at(x, y)
				out.set(src.H-1-y, x, r, g, b, a)
			}
		}
		return out
	case 180:
		out := newRGBA(src.W, src.H)
		for y := 0; y < src.H; y++ {
			for x := 0; x < src.W; x++ {
				r, g, b, a := src.at(x, y)
				out.set(src.W-1-x, src.H-1-y, r, g, b, a)
			}
		}
		return out
	case 270:
		out := newRGBA(src.H, src.W)
		for y := 0; y < src.H; y++ {
			for x := 0; x < src.W; x++ {
				r, g, b, a := src.at(x, y)
				out.set(y, src.W-1-x, r, g, b, a)
			}
		}
		return out
	default:
		return src
	}
}

// scaleNearest resamples to the layer geometry with integer nearest mapping.
func scaleNearest(src *rgbaImage, dw, dh int) *rgbaImage {
	if dw == src.W && dh == src.H {
		return src
	}
	out := newRGBA(dw, dh)
	for y := 0; y < dh; y++ {
		sy := y * src.H / dh
		for x := 0; x < dw; x++ {
			sx := x * src.W / dw
			r, g, b, a := src.at(sx, sy)
			out.set(x, y, r, g, b, a)
		}
	}
	return out
}
