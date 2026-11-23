// Package render produces offscreen, fully deterministic PNG frames sampled
// from the synthesized timeline, plus SHA-256 manifests and audio statistics.
// It never opens a graphics device: every pixel is computed in memory and
// encoded with the standard library.
package render

import (
	"bytes"
	"hash/fnv"
	"image"
	"image/color"
	"image/png"
)

// Frame is one sampled timeline instant rendered to an in-memory PNG.
type Frame struct {
	Index  int
	TimeNs int64
	Width  int
	Height int
	PNG    []byte
}

// FrameOptions controls frame sampling and raster geometry.
type FrameOptions struct {
	Width       int
	Height      int
	FrameStepNs int64 // sampling cadence on the shared clock
}

// DefaultFrameOptions returns a small, fast, deterministic raster.
func DefaultFrameOptions() FrameOptions {
	return FrameOptions{Width: 64, Height: 36, FrameStepNs: 40_000_000}
}

// SamplePoint is one instant the renderer must cover. It keeps the package
// decoupled from the timeline package's concrete sample type.
type SamplePoint struct {
	TrackNumber uint32
	StartNs     int64
	EndNs       int64
	Keyframe    bool
}

// RenderFrames samples the clock range [startNs,endNs] at the configured
// cadence and emits one deterministic PNG per instant. Repeated calls with
// identical points and options produce byte-identical output.
func RenderFrames(points []SamplePoint, startNs, endNs int64, opts FrameOptions) ([]Frame, error) {
	if opts.Width <= 0 || opts.Height <= 0 {
		opts = DefaultFrameOptions()
	}
	if opts.FrameStepNs <= 0 {
		opts.FrameStepNs = DefaultFrameOptions().FrameStepNs
	}
	var frames []Frame
	idx := 0
	for t := startNs; t <= endNs; t += opts.FrameStepNs {
		img := rasterize(opts.Width, opts.Height, t, activeTrack(points, t))
		var buf bytes.Buffer
		if err := png.Encode(&buf, img); err != nil {
			return nil, err
		}
		frames = append(frames, Frame{
			Index: idx, TimeNs: t,
			Width: opts.Width, Height: opts.Height, PNG: buf.Bytes(),
		})
		idx++
	}
	return frames, nil
}

// activeTrack returns the lowest-numbered track covering time t, or 0 if the
// instant falls in a sparse gap.
func activeTrack(points []SamplePoint, t int64) uint32 {
	var best uint32
	found := false
	for _, p := range points {
		if t >= p.StartNs && t < p.EndNs {
			if !found || p.TrackNumber < best {
				best = p.TrackNumber
				found = true
			}
		}
	}
	return best
}

// rasterize builds a deterministic image from the timestamp and active
// track. The pattern is a pure function of its inputs so the same timeline
// always yields the same pixels and therefore the same frame hashes.
func rasterize(w, h int, timeNs int64, track uint32) image.Image {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	base := paletteColor(timeNs)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			r, g, b := base.r, base.g, base.b
			// Encode the active track as a band and the time as a gradient.
			if y < int(track%uint32(h))+1 {
				r, g, b = 255, 255, 255
			} else if (x+int(timeNs/1_000_000))&7 == 0 {
				r = r / 2
				g = g / 2
				b = b / 2
			}
			img.SetRGBA(x, y, color.RGBA{R: r, G: g, B: b, A: 255})
		}
	}
	return img
}

type rgb struct{ r, g, b uint8 }

// paletteColor derives a stable, well-spread color from a timestamp.
func paletteColor(timeNs int64) rgb {
	hh := fnv.New32a()
	_, _ = hh.Write([]byte(byteSlice(timeNs)))
	v := hh.Sum32()
	return rgb{
		r: uint8(v & 0xFF),
		g: uint8((v >> 8) & 0xFF),
		b: uint8((v >> 16) & 0xFF),
	}
}

func byteSlice(v int64) []byte {
	out := make([]byte, 8)
	for i := 7; i >= 0; i-- {
		out[i] = byte(v & 0xFF)
		v >>= 8
	}
	return out
}
