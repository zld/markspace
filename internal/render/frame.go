// Package render produces reviewable PNG frame sequences offscreen.
// It does not decode codecs: each frame is a deterministic
// visualisation of the verified sample payload together with the
// recorded transform (rotation, crop, colour decision), so reviewers
// can reproduce and compare frames bit-for-bit.
package render

import (
	"crypto/sha256"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"

	"archive/internal/model"
	"archive/internal/timeline"
)

// Frame geometry for the review sequence (well under MaxFrameDim).
const (
	FrameWidth  = 160
	FrameHeight = 120
)

// RenderSegment renders one PNG per sample of the segment into dir and
// returns the frame paths in order. Files are written to temporary
// names and atomically renamed into place.
func RenderSegment(dir string, seg timeline.Segment, payloads [][]byte) ([]string, error) {
	if len(payloads) != len(seg.Samples) {
		return nil, model.StageFault(model.StageRender, seg.Track, -1, nil,
			"payload count %d != sample count %d", len(payloads), len(seg.Samples))
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	paths := make([]string, 0, len(seg.Samples))
	for i, sample := range seg.Samples {
		img := frameImage(seg, sample, payloads[i])
		final := filepath.Join(dir, fmt.Sprintf("seg-%02d-frame-%03d.png", seg.Index, i))
		tmp, err := os.CreateTemp(dir, ".tmp-frame-*")
		if err != nil {
			return nil, err
		}
		if err := png.Encode(tmp, img); err != nil {
			tmp.Close()
			os.Remove(tmp.Name())
			return nil, model.StageFault(model.StageRender, seg.Track, -1, nil, "png encode: %v", err)
		}
		if err := tmp.Close(); err != nil {
			os.Remove(tmp.Name())
			return nil, err
		}
		if err := os.Rename(tmp.Name(), final); err != nil {
			os.Remove(tmp.Name())
			return nil, err
		}
		paths = append(paths, final)
	}
	return paths, nil
}

// frameImage builds the deterministic review frame for one sample.
func frameImage(seg timeline.Segment, sample timeline.AlignedSample, payload []byte) *image.RGBA {
	seed := sha256.Sum256(append(append([]byte{}, payload...),
		[]byte(fmt.Sprintf("/%d/%d/%d", seg.Track, sample.SampleIndex, seg.Transform.RotationDeg))...))
	// Deterministic byte stream for the pattern.
	stream := make([]byte, 0, 4096)
	ctr := uint64(0)
	for len(stream) < 4096 {
		h := sha256.Sum256(append(append([]byte{}, seed[:]...), byte(ctr), byte(ctr>>8)))
		stream = append(stream, h[:]...)
		ctr++
	}
	img := image.NewRGBA(image.Rect(0, 0, FrameWidth, FrameHeight))
	tint := seg.Transform.Color.Chosen
	tr, tg, tb := colorTint(tint.Primaries, tint.Transfer, tint.Matrix)
	for y := 0; y < FrameHeight; y++ {
		for x := 0; x < FrameWidth; x++ {
			sx, sy := rotateCoords(x, y, seg.Transform.RotationDeg)
			v := stream[(sy*FrameWidth+sx)%len(stream)]
			r := v & tr
			g := (v >> 1) & tg
			b := (v >> 2) & tb
			if outsideCrop(seg, x, y) {
				r, g, b = 0, 0, 0
			}
			img.SetRGBA(x, y, color.RGBA{R: r, G: g, B: b, A: 0xFF})
		}
	}
	return img
}

// colorTint derives deterministic channel masks from the chosen
// colour description so different decisions are visibly distinct.
func colorTint(p, t, m uint16) (uint8, uint8, uint8) {
	return uint8(0xC0 | uint8(p)<<4), uint8(0xC0 | uint8(t)<<4), uint8(0xC0 | uint8(m)<<4)
}

// rotateCoords maps output coordinates back to source coordinates for
// the recorded right-angle rotation.
func rotateCoords(x, y, deg int) (int, int) {
	switch deg {
	case 90:
		return y, FrameWidth - 1 - x
	case 180:
		return FrameWidth - 1 - x, FrameHeight - 1 - y
	case 270:
		return FrameHeight - 1 - y, x
	}
	return x, y
}

// outsideCrop reports whether the output pixel falls outside the
// recorded crop window (crop is given in source pixels and scaled to
// the review frame geometry).
func outsideCrop(seg timeline.Segment, x, y int) bool {
	c := seg.Transform.Crop
	if c == nil || seg.Width == 0 || seg.Height == 0 {
		return false
	}
	x0 := int(c.X) * FrameWidth / int(seg.Width)
	y0 := int(c.Y) * FrameHeight / int(seg.Height)
	x1 := int(c.X+c.W) * FrameWidth / int(seg.Width)
	y1 := int(c.Y+c.H) * FrameHeight / int(seg.Height)
	return x < x0 || x >= x1 || y < y0 || y >= y1
}
