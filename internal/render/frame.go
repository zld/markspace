// Package render draws review frames offscreen and hashes them.
//
// Frames are pure functions of their specification: the same timeline state at
// the same instant always produces byte-identical PNG output, so a repeated
// run publishes the same SHA-256 values. Drawing uses only integer arithmetic
// and a fixed palette; there are no timestamps, random ids or font files.
package render

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"sort"

	"media.rebuild/internal/model"
)

// DefaultWidth and DefaultHeight are the review frame dimensions.
const (
	DefaultWidth  = 320
	DefaultHeight = 180
)

// Lane is one track's state at a sampling instant.
type Lane struct {
	Track  string `json:"track"`
	Kind   string `json:"kind"`
	Active bool   `json:"active"`
	Items  int    `json:"items"`
	// SkewNs is the worst absolute deviation of this track's active items.
	SkewNs int64 `json:"skew_ns"`
	// Over marks an active item beyond the deviation budget.
	Over bool `json:"over"`
}

// Spec is everything that determines a frame.
type Spec struct {
	Index       int    `json:"index"`
	TimeNs      int64  `json:"time_ns"`
	SpanStartNs int64  `json:"span_start_ns"`
	SpanEndNs   int64  `json:"span_end_ns"`
	SourceEvent string `json:"source_event"`
	Subtitle    string `json:"subtitle"`
	Lanes       []Lane `json:"lanes"`
	Width       int    `json:"width"`
	Height      int    `json:"height"`
}

// Frame is a rendered artifact.
type Frame struct {
	Index       int    `json:"index"`
	TimeNs      int64  `json:"time_ns"`
	SourceEvent string `json:"source_event"`
	File        string `json:"file"`
	Bytes       int    `json:"bytes"`
	SHA256      string `json:"sha256"`
	Data        []byte `json:"-"`
}

// laneColor derives a stable RGB color from a track name.
func laneColor(track string) color.RGBA {
	sum := sha256.Sum256([]byte("lane:" + track))
	// Keep channels bright enough to stand out on the dark background.
	return color.RGBA{R: 64 + sum[0]%160, G: 64 + sum[1]%160, B: 64 + sum[2]%160, A: 255}
}

var (
	bg        = color.RGBA{R: 16, G: 18, B: 24, A: 255}
	ruler     = color.RGBA{R: 70, G: 74, B: 86, A: 255}
	marker    = color.RGBA{R: 250, G: 250, B: 250, A: 255}
	subtitleC = color.RGBA{R: 210, G: 220, B: 235, A: 255}
	overCol   = color.RGBA{R: 230, G: 70, B: 70, A: 255}
	dim       = color.RGBA{R: 40, G: 44, B: 54, A: 255}
)

// Render draws the frame and returns its PNG bytes.
func Render(spec Spec) ([]byte, error) {
	w, h := spec.Width, spec.Height
	if w == 0 {
		w = DefaultWidth
	}
	if h == 0 {
		h = DefaultHeight
	}
	if w < 16 || h < 16 || w > 4096 || h > 4096 {
		return nil, model.Newf(model.CodeInvalidArgument, model.Source{Offset: -1},
			"frame size %dx%d outside 16..4096", w, h)
	}
	if spec.Index < 0 {
		return nil, model.Newf(model.CodeInvalidArgument, model.Source{Offset: -1}, "negative frame index %d", spec.Index)
	}
	if spec.TimeNs < 0 {
		return nil, model.Newf(model.CodeInvalidArgument, model.Source{Offset: -1}, "negative frame time %d", spec.TimeNs)
	}

	img := image.NewRGBA(image.Rect(0, 0, w, h))
	fill(img, bg)

	lanes := append([]Lane(nil), spec.Lanes...)
	sort.SliceStable(lanes, func(i, j int) bool {
		if lanes[i].Track != lanes[j].Track {
			return lanes[i].Track < lanes[j].Track
		}
		return lanes[i].Kind < lanes[j].Kind
	})

	// A 1-pixel ruler across the top with a marker at the sampling instant and
	// tick marks at 10% steps.
	for x := 0; x < w; x++ {
		img.SetRGBA(x, 1, ruler)
	}
	for i := 0; i <= 10; i++ {
		x := (w - 1) * i / 10
		for y := 0; y < 4; y++ {
			img.SetRGBA(x, y, ruler)
		}
	}
	if spec.SpanEndNs > spec.SpanStartNs {
		pos := (spec.TimeNs - spec.SpanStartNs) * int64(w-1) / (spec.SpanEndNs - spec.SpanStartNs)
		if pos < 0 {
			pos = 0
		}
		if pos > int64(w-1) {
			pos = int64(w - 1)
		}
		for y := 0; y < 6; y++ {
			img.SetRGBA(int(pos), y, marker)
		}
	}

	// Lanes occupy vertical bands below the ruler. An active lane is drawn as
	// a solid bar whose width encodes its skew; an over-budget lane is drawn
	// in the alarm color so the frame itself records the finding.
	top := 10
	usable := h - top - 26
	if usable < len(lanes)*4 {
		usable = len(lanes) * 4
	}
	band := 0
	if len(lanes) > 0 {
		band = usable / len(lanes)
		if band > 14 {
			band = 14
		}
		if band < 3 {
			band = 3
		}
	}
	for i, lane := range lanes {
		y := top + i*band
		if y+band-1 >= h-26 {
			break
		}
		c := laneColor(lane.Track)
		// Every lane gets a baseline strip so the frame shows which tracks
		// exist even when they are idle at this instant.
		for x := 0; x < w; x++ {
			img.SetRGBA(x, y+band-1, dim)
		}
		if !lane.Active {
			continue
		}
		bar := w / 3
		if lane.SkewNs > 0 {
			bar += int(lane.SkewNs) / 4_000_000
		}
		if bar > w {
			bar = w
		}
		if lane.Over {
			c = overCol
		}
		for yy := y; yy < y+band-1; yy++ {
			for x := 0; x < bar; x++ {
				img.SetRGBA(x, yy, c)
			}
		}
	}

	// Active subtitle text is represented by a fixed cell pattern derived from
	// the text, so changing the caption changes the frame hash.
	if spec.Subtitle != "" {
		sum := sha256.Sum256([]byte(spec.Subtitle))
		base := h - 22
		for i := 0; i < 24; i++ {
			cell := sum[i%len(sum)]
			x0 := 4 + i*12
			if x0+8 >= w {
				break
			}
			for yy := 0; yy < 6; yy++ {
				for xx := 0; xx < 8; xx++ {
					if cell&(1<<uint(xx%8)) != 0 {
						img.SetRGBA(x0+xx, base+yy+int(cell>>6)%6, subtitleC)
					}
				}
			}
		}
	}

	var buf bytes.Buffer
	enc := png.Encoder{CompressionLevel: png.DefaultCompression}
	if err := enc.Encode(&buf, img); err != nil {
		return nil, model.New(model.CodeIO, model.Source{Offset: -1}, "encode frame png", err)
	}
	return buf.Bytes(), nil
}

// RenderFrame renders and hashes one frame.
func RenderFrame(spec Spec, file string) (*Frame, error) {
	data, err := Render(spec)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(data)
	return &Frame{
		Index:       spec.Index,
		TimeNs:      spec.TimeNs,
		SourceEvent: spec.SourceEvent,
		File:        file,
		Bytes:       len(data),
		SHA256:      hex.EncodeToString(sum[:]),
		Data:        data,
	}, nil
}

func fill(img *image.RGBA, c color.RGBA) {
	w := img.Bounds().Dx()
	h := img.Bounds().Dy()
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.SetRGBA(x, y, c)
		}
	}
}

// Digest is the canonical hash over a set of frames used for run-to-run
// comparison. It folds file names and hashes in frame order.
func Digest(frames []Frame) string {
	h := sha256.New()
	for _, f := range frames {
		fmt.Fprintf(h, "%d\x00%s\x00%s\x00", f.Index, f.File, f.SHA256)
	}
	return hex.EncodeToString(h.Sum(nil))
}
