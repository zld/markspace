package render

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"image"
	"image/color"
	"image/png"
	"time"

	"continuity/internal/timeline"
)

const (
	Width  = 320
	Height = 180
)

// Frame is an in-memory review PNG and its content hash.
type Frame struct {
	Sequence int      `json:"sequence"`
	PTSNS    int64    `json:"pts_ns"`
	Path     string   `json:"path,omitempty"`
	SHA256   string   `json:"sha256"`
	Sources  []string `json:"sources"`
	PNGBytes []byte   `json:"-"`
}

// Result contains rendered frames and encoding latency samples.
type Result struct {
	Frames    []*Frame        `json:"frames"`
	Durations []time.Duration `json:"-"`
	P95MS     float64         `json:"p95_render_ms"`
}

// RenderAll deterministically encodes every aligned timeline point.
func RenderAll(tl *timeline.Result) (*Result, error) {
	out := &Result{}
	for _, p := range tl.Points {
		start := time.Now()
		f, err := RenderPoint(p)
		out.Durations = append(out.Durations, time.Since(start))
		if err != nil {
			return nil, err
		}
		out.Frames = append(out.Frames, f)
	}
	out.P95MS = percentile95MS(out.Durations)
	return out, nil
}

// RenderPoint draws track presence, integer-NS timestamp bits, source switch
// and gap markers into a PNG. No system fonts or GUI are required.
func RenderPoint(p timeline.Point) (*Frame, error) {
	img := image.NewRGBA(image.Rect(0, 0, Width, Height))
	bg := color.RGBA{R: 18, G: 22, B: 30, A: 255}
	fill(img, img.Bounds(), bg)

	// Track lanes: blue=video present, green=audio present, red=missing.
	drawTrack(img, 14, 14, "V", p.Video != nil, color.RGBA{40, 120, 255, 255})
	drawTrack(img, 92, 14, "A", p.Audio != nil, color.RGBA{40, 200, 120, 255})

	// Sequence digits and PTS decimal-ish marker area.
	drawDigits(img, 180, 18, p.Index+1, 3, color.RGBA{235, 240, 255, 255})

	// 64-bit PTS nanosecond barcode, MSB first.
	drawTimestampBarcode(img, 14, 62, uint64(p.PTSNS))

	// Sync error as a small bar: each pixel is 0.1 ms up to 20 ms.
	syncWidth := int(p.SyncErrorNS / 100_000)
	if syncWidth > 200 {
		syncWidth = 200
	}
	syncColor := color.RGBA{80, 220, 160, 255}
	if p.SyncErrorNS > 20_000_000 {
		syncColor = color.RGBA{255, 80, 80, 255}
	}
	fill(img, image.Rect(14, 104, 214, 116), color.RGBA{45, 50, 62, 255})
	fill(img, image.Rect(14, 104, 14+syncWidth, 116), syncColor)

	if p.Switch {
		orange := color.RGBA{255, 166, 0, 255}
		for t := 0; t < 6; t++ {
			fill(img, image.Rect(t*8, 0, t*8+4, Height), orange)
			fill(img, image.Rect(Width-4-t*8, 0, Width-t*8, Height), orange)
		}
		drawDiagonal(img, 14, 126, 110, 42, orange)
	}
	if p.Gap {
		red := color.RGBA{255, 72, 72, 255}
		drawThickLine(img, 180, 126, 306, 166, red, 5)
		drawThickLine(img, 306, 126, 180, 166, red, 5)
		for x := 0; x < Width; x += 16 {
			fill(img, image.Rect(x, Height-8, x+8, Height), red)
		}
	}

	var buf bytes.Buffer
	enc := png.Encoder{CompressionLevel: png.BestCompression}
	if err := enc.Encode(&buf, img); err != nil {
		return nil, err
	}
	sum := sha256.Sum256(buf.Bytes())
	return &Frame{Sequence: p.Index + 1, PTSNS: p.PTSNS, Sources: append([]string(nil), p.Sources...),
		SHA256: hex.EncodeToString(sum[:]), PNGBytes: buf.Bytes()}, nil
}

func drawTrack(img *image.RGBA, x, y int, glyph string, present bool, c color.RGBA) {
	box := image.Rect(x, y, x+64, y+34)
	if present {
		fill(img, box, c)
		r := 'V'
		if glyph == "A" {
			r = 'A'
		}
		drawRune(img, x+22, y+8, r, 4, color.RGBA{10, 14, 20, 255})
	} else {
		fill(img, box, color.RGBA{90, 35, 40, 255})
		drawThickLine(img, x+8, y+8, x+56, y+26, color.RGBA{255, 90, 90, 255}, 3)
		drawThickLine(img, x+56, y+8, x+8, y+26, color.RGBA{255, 90, 90, 255}, 3)
	}
}

func drawTimestampBarcode(img *image.RGBA, x, y int, v uint64) {
	for i := 0; i < 64; i++ {
		on := v&(uint64(1)<<(63-i)) != 0
		c := color.RGBA{70, 80, 98, 255}
		if on {
			c = color.RGBA{220, 235, 255, 255}
		}
		bx := x + i*4
		fill(img, image.Rect(bx, y, bx+3, y+28), c)
	}
}

var digits = map[rune][5]byte{
	'0': {0b01110, 0b10001, 0b10011, 0b10101, 0b10001},
	'1': {0b00100, 0b01100, 0b00100, 0b00100, 0b01110},
	'2': {0b01110, 0b10001, 0b00110, 0b01000, 0b11111},
	'3': {0b11110, 0b00001, 0b01110, 0b00001, 0b11110},
	'4': {0b00010, 0b00110, 0b01010, 0b11111, 0b00010},
	'5': {0b11111, 0b10000, 0b11110, 0b00001, 0b11110},
	'6': {0b01110, 0b10000, 0b11110, 0b10001, 0b01110},
	'7': {0b11111, 0b00001, 0b00010, 0b00100, 0b01000},
	'8': {0b01110, 0b10001, 0b01110, 0b10001, 0b01110},
	'9': {0b01110, 0b10001, 0b01111, 0b00001, 0b01110},
	'A': {0b01110, 0b10001, 0b11111, 0b10001, 0b10001},
	'V': {0b10001, 0b10001, 0b10001, 0b01010, 0b00100},
}

func drawRune(img *image.RGBA, x, y int, r rune, scale int, c color.Color) {
	glyph, ok := digits[r]
	if !ok {
		return
	}
	for row, bits := range glyph {
		for col := 0; col < 5; col++ {
			if bits&(1<<(4-col)) != 0 {
				rect := image.Rect(x+col*scale, y+row*scale,
					x+(col+1)*scale, y+(row+1)*scale)
				fill(img, rect, c)
			}
		}
	}
}

func drawDigits(img *image.RGBA, x, y, value, scale int, c color.Color) {
	for di, ch := range itoa(value) {
		drawRune(img, x+di*6*scale, y, ch, scale, c)
	}
}

func itoa(v int) string {
	if v == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for v > 0 {
		i--
		b[i] = byte('0' + v%10)
		v /= 10
	}
	return string(b[i:])
}

func fill(img *image.RGBA, r image.Rectangle, c color.Color) {
	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			if image.Pt(x, y).In(img.Bounds()) {
				img.Set(x, y, c)
			}
		}
	}
}

func drawDiagonal(img *image.RGBA, x, y, w, h int, c color.Color) {
	for i := 0; i < w || i < h; i += 2 {
		if i < w && i < h {
			drawThickLine(img, x, y+i, x+i, y, c, 2)
		}
	}
}

func drawThickLine(img *image.RGBA, x0, y0, x1, y1 int, c color.Color, thick int) {
	dx := absInt(x1 - x0)
	dy := -absInt(y1 - y0)
	sx, sy := 1, 1
	if x0 > x1 {
		sx = -1
	}
	if y0 > y1 {
		sy = -1
	}
	err := dx + dy
	for {
		for oy := -thick / 2; oy <= thick/2; oy++ {
			for ox := -thick / 2; ox <= thick/2; ox++ {
				img.Set(x0+ox, y0+oy, c)
			}
		}
		if x0 == x1 && y0 == y1 {
			break
		}
		e2 := 2 * err
		if e2 >= dy {
			err += dy
			x0 += sx
		}
		if e2 <= dx {
			err += dx
			y0 += sy
		}
	}
}

func absInt(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

func percentile95MS(d []time.Duration) float64 {
	if len(d) == 0 {
		return 0
	}
	cp := append([]time.Duration(nil), d...)
	// Tiny deterministic insertion/sort without exposing sort at call site.
	for i := 1; i < len(cp); i++ {
		for j := i; j > 0 && cp[j] < cp[j-1]; j-- {
			cp[j], cp[j-1] = cp[j-1], cp[j]
		}
	}
	idx := (95*len(cp) + 99) / 100
	if idx > len(cp) {
		idx = len(cp)
	}
	return float64(cp[idx-1].Microseconds()) / 1000.0
}
