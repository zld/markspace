package render

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"

	"mediasync/internal/media"
	"mediasync/internal/pool"
	"mediasync/internal/timeline"
)

const (
	Width  = 320
	Height = 180
)

type Spec struct {
	Index    int    `json:"index"`
	TimeMS   int64  `json:"time_ms"`
	TrackID  string `json:"track_id,omitempty"`
	Subtitle string `json:"subtitle,omitempty"`
	Active   bool   `json:"active"`
}

type Result struct {
	Index  int    `json:"index"`
	TimeMS int64  `json:"time_ms"`
	Path   string `json:"path"`
	Bytes  int    `json:"bytes"`
	SHA256 string `json:"sha256"`
}

// BuildSpecs maps requested review times to the subtitle active at each time.
func BuildSpecs(tl *timeline.Timeline, timesMS []int64) ([]Spec, error) {
	specs := make([]Spec, 0, len(timesMS))
	for i, ms := range timesMS {
		ticks, err := media.FromMilliseconds(ms)
		if err != nil {
			return nil, media.OverflowError("render", "", err).WithRecord(i)
		}
		spec := Spec{Index: i + 1, TimeMS: ms}
		for _, ev := range tl.Events {
			if ev.Kind != timeline.KindSubtitle {
				continue
			}
			if ev.StartTicks <= ticks && ticks <= ev.EndTicks {
				spec.TrackID = ev.TrackID
				spec.Subtitle = ev.Text
				spec.Active = true
				break
			}
		}
		specs = append(specs, spec)
	}
	return specs, nil
}

// RenderAll uses the shared bounded pool for offscreen PNG export.
func RenderAll(ctx context.Context, specs []Spec, outDir string) ([]Result, error) {
	p := pool.New[Spec, Result](pool.Config{}, func(_ context.Context, spec Spec) (Result, error) {
		return RenderFrame(spec, outDir)
	})
	return p.Run(ctx, specs)
}

func RenderFrame(spec Spec, outDir string) (Result, error) {
	img := image.NewRGBA(image.Rect(0, 0, Width, Height))
	drawBackground(img)
	drawFrameNumber(img, spec.Index)
	drawTimelineRuler(img, spec.TimeMS)
	drawSubtitleGlyphs(img, spec.Subtitle)
	if spec.Active {
		fillRect(img, 0, 0, Width, 6, color.RGBA{R: 0x4f, G: 0xc3, B: 0xf7, A: 255})
	} else {
		fillRect(img, 0, 0, Width, 6, color.RGBA{R: 0x45, G: 0x55, B: 0x68, A: 255})
	}

	var buf bytes.Buffer
	enc := png.Encoder{CompressionLevel: png.BestCompression}
	if err := enc.Encode(&buf, img); err != nil {
		return Result{}, media.InputError("render.frame", spec.TrackID, err).WithRecord(spec.Index)
	}
	sum := sha256.Sum256(buf.Bytes())
	rel := filepath.Join("frames", fmt.Sprintf("frame-%04d.png", spec.Index))
	full := filepath.Join(outDir, rel)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return Result{}, media.InputError("render.frame", spec.TrackID, err).WithRecord(spec.Index)
	}
	tmp := full + ".tmp"
	if err := os.WriteFile(tmp, buf.Bytes(), 0o644); err != nil {
		return Result{}, media.InputError("render.frame", spec.TrackID, err).WithRecord(spec.Index)
	}
	if err := os.Rename(tmp, full); err != nil {
		return Result{}, media.InputError("render.frame", spec.TrackID, err).WithRecord(spec.Index)
	}
	return Result{
		Index: spec.Index, TimeMS: spec.TimeMS, Path: filepath.ToSlash(rel),
		Bytes: buf.Len(), SHA256: hex.EncodeToString(sum[:]),
	}, nil
}

func drawBackground(img *image.RGBA) {
	bg := color.RGBA{R: 0x10, G: 0x18, B: 0x27, A: 255}
	for y := 0; y < Height; y++ {
		for x := 0; x < Width; x++ {
			img.SetRGBA(x, y, bg)
		}
	}
	border := color.RGBA{R: 0x33, G: 0x41, B: 0x55, A: 255}
	for x := 0; x < Width; x++ {
		img.SetRGBA(x, Height-24, border)
	}
}

func fillRect(img *image.RGBA, x0, y0, w, h int, c color.Color) {
	for y := y0; y < y0+h && y < Height; y++ {
		for x := x0; x < x0+w && x < Width; x++ {
			if x >= 0 && y >= 0 {
				img.Set(x, y, c)
			}
		}
	}
}

var digitFont = [10][5]byte{
	{7, 5, 5, 5, 7}, // 0
	{2, 6, 2, 2, 7}, // 1
	{7, 1, 7, 4, 7}, // 2
	{7, 1, 7, 1, 7}, // 3
	{5, 5, 7, 1, 1}, // 4
	{7, 4, 7, 1, 7}, // 5
	{7, 4, 7, 5, 7}, // 6
	{7, 1, 2, 2, 2}, // 7
	{7, 5, 7, 5, 7}, // 8
	{7, 5, 7, 1, 7}, // 9
}

func drawFrameNumber(img *image.RGBA, n int) {
	digits := []int{(n / 1000) % 10, (n / 100) % 10, (n / 10) % 10, n % 10}
	x := 20
	on := color.RGBA{R: 0xe5, G: 0xe7, B: 0xeb, A: 255}
	for _, d := range digits {
		for row, bits := range digitFont[d] {
			for col := 0; col < 3; col++ {
				if bits&(1<<(2-col)) != 0 {
					fillRect(img, x+col*5, 24+row*7, 4, 6, on)
				}
			}
		}
		x += 22
	}
}

func drawTimelineRuler(img *image.RGBA, ms int64) {
	y := Height - 14
	fillRect(img, 16, y, Width-32, 2, color.RGBA{R: 0x64, G: 0x74, B: 0x8b, A: 255})
	pos := 16 + int((ms%4000)*int64(Width-32)/3999)
	fillRect(img, pos-2, y-5, 4, 12, color.RGBA{R: 0x38, G: 0xbd, B: 0xf8, A: 255})
	for tick := 0; tick <= 4; tick++ {
		x := 16 + tick*(Width-32)/4
		fillRect(img, x, y-3, 1, 8, color.RGBA{R: 0x94, G: 0xa3, B: 0xb8, A: 255})
	}
}

func drawSubtitleGlyphs(img *image.RGBA, text string) {
	if text == "" {
		return
	}
	x, y := 20, 82
	const cell = 18
	for _, r := range text {
		if x > Width-24 {
			x = 20
			y += 22
		}
		if y > Height-34 {
			break
		}
		sum := sha256.Sum256([]byte(string(r)))
		c := color.RGBA{R: 0x80 + sum[0]%120, G: 0x90 + sum[1]%100, B: 0xc0 + sum[2]%55, A: 255}
		for row := 0; row < 8; row++ {
			bits := sum[row]
			for col := 0; col < 8; col++ {
				if bits&(1<<uint(col)) != 0 {
					fillRect(img, x+col*2, y+row*2, 2, 2, c)
				}
			}
		}
		x += cell
	}
}
