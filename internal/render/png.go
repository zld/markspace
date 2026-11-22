package render

import (
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"hash/crc32"
	"image"
)

// EncodePNG writes an RGBA image as a PNG using a fixed filter strategy.
//
// Output is deterministic for a given image: filter 0 on every row and a
// fixed zlib level. The standard image/png encoder is still used for
// decoding in the roundtrip check; this encoder only replaces the slow
// adaptive-filter selection on large noisy rasters.
//
// PNG stores straight alpha, so a non-opaque image is converted to NRGBA
// first. Opaque images (the pipeline's normal case) are copied unchanged.
func EncodePNG(img *image.RGBA, level int) ([]byte, error) {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	if !img.Opaque() {
		img = toNRGBA(img)
		b = img.Bounds()
	}
	if level < zlib.BestSpeed || level > zlib.BestCompression {
		level = zlib.BestSpeed
	}
	var out bytes.Buffer
	out.Write([]byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'})

	var ihdr [13]byte
	binary.BigEndian.PutUint32(ihdr[0:], uint32(w))
	binary.BigEndian.PutUint32(ihdr[4:], uint32(h))
	ihdr[8] = 8 // bit depth
	ihdr[9] = 6 // color type: truecolor with alpha
	writeChunk(&out, "IHDR", ihdr[:])

	// One filter byte (0 = None) followed by the row's raw RGBA bytes.
	raw := make([]byte, 0, (w*4+1)*h)
	for y := 0; y < h; y++ {
		raw = append(raw, 0)
		start := (y+b.Min.Y-img.Rect.Min.Y)*img.Stride + (b.Min.X-img.Rect.Min.X)*4
		raw = append(raw, img.Pix[start:start+w*4]...)
	}
	var z bytes.Buffer
	zw, err := zlib.NewWriterLevel(&z, level)
	if err != nil {
		return nil, err
	}
	if _, err := zw.Write(raw); err != nil {
		return nil, err
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	writeChunk(&out, "IDAT", z.Bytes())
	writeChunk(&out, "IEND", nil)
	return out.Bytes(), nil
}

// toNRGBA un-premultiplies a premultiplied RGBA buffer. The result is
// returned as an *image.RGBA so callers can slice Pix, but its bytes are
// straight-alpha RGBA in NRGBA order, which is what a PNG IDAT stores.
func toNRGBA(src *image.RGBA) *image.RGBA {
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			r, g, bl, a := src.At(b.Min.X+x, b.Min.Y+y).RGBA()
			i := y*dst.Stride + x*4
			if a == 0 {
				dst.Pix[i], dst.Pix[i+1], dst.Pix[i+2], dst.Pix[i+3] = 0, 0, 0, 0
				continue
			}
			dst.Pix[i] = uint8((r * 0xffff / a) >> 8)
			dst.Pix[i+1] = uint8((g * 0xffff / a) >> 8)
			dst.Pix[i+2] = uint8((bl * 0xffff / a) >> 8)
			dst.Pix[i+3] = uint8(a >> 8)
		}
	}
	return dst
}

func writeChunk(out *bytes.Buffer, typ string, data []byte) {
	var hdr [8]byte
	binary.BigEndian.PutUint32(hdr[0:], uint32(len(data)))
	copy(hdr[4:], typ)
	out.Write(hdr[:])
	out.Write(data)
	crc := crc32.NewIEEE()
	crc.Write(hdr[4:])
	crc.Write(data)
	var sum [4]byte
	binary.BigEndian.PutUint32(sum[:], crc.Sum32())
	out.Write(sum[:])
}
