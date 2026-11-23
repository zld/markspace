// Package render produces deterministic offscreen RGBA frames and computes
// per-frame hashes, pixel statistics and bitrate accounting.
package render

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"

	"mediad/timeline"
)

// Frame geometry is fixed by the delivery contract.
const (
	Width  = 320
	Height = 180
	MaxFPS = 30
)

// Overlay is a timed annotation drawn onto a frame.
type Overlay struct {
	TimeNS  int64
	TrackID uint32
	Text    string
	Kind    timeline.Kind
}

// Frame is a single deterministic RGBA image plus its timed context.
type Frame struct {
	Index   int64
	TimeNS  int64
	Pix     []byte // Width*Height*4 RGBA, no windowing
	Bad     bool
	BadTime int64 // first bad sample time on the source track, 0 when healthy
}

// Generator renders frames purely from frame index, time and timeline events.
// Identical inputs always produce identical bytes (no time/rand sources).
type Generator struct {
	overlays []Overlay
}

// NewGenerator pre-indexes subtitle and chapter events as overlays.
func NewGenerator(tl *timeline.Timeline) *Generator {
	g := &Generator{}
	for _, ev := range tl.Events {
		if ev.Kind == timeline.KindSubtitle || ev.Kind == timeline.KindChapter {
			g.overlays = append(g.overlays, Overlay{
				TimeNS: ev.TimeNS, TrackID: ev.TrackID, Text: ev.Text, Kind: ev.Kind,
			})
		}
	}
	return g
}

// Render builds frame i at timeNS. A corrupt video event at this time yields a
// flagged frame plus the first bad time of its track.
func (g *Generator) Render(index int64, timeNS int64, badEvents map[int64]bool, firstBadTime int64) Frame {
	pix := make([]byte, Width*Height*4)
	// Deterministic gradient seeded by frame index: no randomness, no clock.
	base := uint8(index % 256)
	for y := 0; y < Height; y++ {
		for x := 0; x < Width; x++ {
			o := (y*Width + x) * 4
			pix[o+0] = base + uint8(x%64)
			pix[o+1] = base + uint8(y%64)
			pix[o+2] = uint8((x + y) % 128)
			pix[o+3] = 255
		}
	}
	// Overlay band: deterministic 5x7 bitmap text, chapters in red, subs white.
	var band []Overlay
	for _, ov := range g.overlays {
		if ov.TimeNS == timeNS {
			band = append(band, ov)
		}
	}
	for i, ov := range band {
		drawText(pix, 8, 8+i*12, ov.Text, ov.Kind == timeline.KindChapter)
	}
	// Frame index encoded into the top-right marker for cross-check visibility.
	encodeIndexMarker(pix, uint64(index))
	f := Frame{Index: index, TimeNS: timeNS, Pix: pix}
	if badEvents[timeNS] {
		f.Bad = true
		f.BadTime = firstBadTime
	}
	return f
}

// font5x7 is a minimal deterministic glyph set for the overlay alphabet.
var font5x7 = map[byte][7]byte{
	'A': {0x7E, 0x11, 0x11, 0x11, 0x7E, 0x00, 0x00},
	'C': {0x7E, 0x01, 0x01, 0x01, 0x01, 0x7E, 0x00},
	'H': {0x11, 0x11, 0x7F, 0x11, 0x11, 0x11, 0x00},
	'E': {0x7F, 0x01, 0x01, 0x01, 0x01, 0x7F, 0x00},
	'R': {0x7E, 0x11, 0x11, 0x31, 0x4E, 0x00, 0x00},
	'0': {0x3E, 0x41, 0x41, 0x41, 0x3E, 0x00, 0x00},
	'1': {0x00, 0x42, 0x7F, 0x40, 0x00, 0x00, 0x00},
	'2': {0x42, 0x61, 0x51, 0x49, 0x46, 0x00, 0x00},
	'3': {0x21, 0x41, 0x45, 0x4B, 0x31, 0x00, 0x00},
	'4': {0x18, 0x14, 0x12, 0x7F, 0x10, 0x00, 0x00},
	'5': {0x27, 0x45, 0x45, 0x45, 0x39, 0x00, 0x00},
	'6': {0x3C, 0x4A, 0x49, 0x49, 0x30, 0x00, 0x00},
	'7': {0x01, 0x61, 0x19, 0x05, 0x03, 0x00, 0x00},
	'8': {0x36, 0x49, 0x49, 0x49, 0x36, 0x00, 0x00},
	'9': {0x06, 0x49, 0x49, 0x29, 0x1E, 0x00, 0x00},
	':': {0x00, 0x36, 0x36, 0x00, 0x00, 0x00, 0x00},
	' ': {0, 0, 0, 0, 0, 0, 0},
	'.': {0, 0, 0, 0, 0, 0x0C, 0},
	'-': {0x08, 0x08, 0x08, 0x08, 0x08, 0x00, 0x00},
	'_': {0x40, 0x40, 0x40, 0x40, 0x40, 0x40, 0x40},
}

func drawText(pix []byte, x0, y0 int, text string, red bool) {
	for i := 0; i < len(text); i++ {
		glyph, ok := font5x7[toUpper(text[i])]
		if !ok {
			glyph = font5x7[' ']
		}
		for row := 0; row < 7; row++ {
			bits := glyph[row]
			for col := 0; col < 5; col++ {
				if bits&(1<<(4-col)) == 0 {
					continue
				}
				x, y := x0+i*6+col, y0+row
				if x < 0 || x >= Width || y < 0 || y >= Height {
					continue
				}
				o := (y*Width + x) * 4
				if red {
					pix[o+0], pix[o+1], pix[o+2] = 220, 40, 40
				} else {
					pix[o+0], pix[o+1], pix[o+2] = 245, 245, 245
				}
				pix[o+3] = 255
			}
		}
	}
}

func toUpper(c byte) byte {
	if c >= 'a' && c <= 'z' {
		return c - 32
	}
	return c
}

// encodeIndexMarker writes the frame index as 8 bytes into fixed corner pixels
// so frame identity is visually present and fully deterministic.
func encodeIndexMarker(pix []byte, index uint64) {
	var buf [8]byte
	binary.BigEndian.PutUint64(buf[:], index)
	for i, b := range buf {
		o := ((Height-1)*Width + i) * 4
		pix[o+0] = b
		pix[o+1] = b ^ 0xFF
		pix[o+2] = b
	}
}

// FrameHash returns the hex SHA-256 of the RGBA payload.
func FrameHash(f Frame) string {
	sum := sha256.Sum256(f.Pix)
	return fmt.Sprintf("%x", sum)
}
