// Package render exports a timeline interval as a deterministic offscreen
// frame sequence. Rendering is a pure function of the committed timeline: each
// frame is a fixed-size RGB565 buffer whose pixels encode which events are
// active at that instant. There is no graphics device, no random source and no
// elapsed-wall-clock dependence, so identical inputs produce identical frames.
package render

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"hash"

	"archive.example/mediakernel/internal/clock"
	"archive.example/mediakernel/internal/events"
)

// Config fixes frame geometry and timing. Frame duration is derived from the
// rate, and is the hard upper bound on frame/event alignment error.
type Config struct {
	Width     int
	Height    int
	FPSNum    int // frames per second numerator
	FPSDen    int // frames per second denominator
	StartNS   int64
	StepNS    int64 // defaults to one frame duration
	MaxFrames int
}

// Frame is one rendered frame with its canonical digest.
type Frame struct {
	Index      int
	AtNS       int64
	Pixels     []uint16 // RGB565, row-major
	SHA256     string
	MeanByte   float64
	MinByte    uint8
	MaxByte    uint8
	EventIDs   []events.Identity
	AlignError int64 // ns between this frame instant and the nearest active event
}

// Sequence is the rendered frame sequence for one interval.
type Sequence struct {
	Frames          []Frame
	FrameDurationNS int64
	StartNS         int64
	EndNS           int64
	MaxAlignError   int64
}

// DefaultConfig is the archive frame layout: 16x9 RGB565 at 25 fps.
func DefaultConfig(startNS int64) Config {
	return Config{Width: 16, Height: 9, FPSNum: 25, FPSDen: 1, StartNS: startNS, MaxFrames: 240}
}

func (c Config) validate() (Config, *events.Error) {
	if c.Width <= 0 || c.Height <= 0 {
		return c, events.NewError(events.CodeInternal, "", 0, -1, "render", "non-positive frame size", nil)
	}
	if c.Width > 256 || c.Height > 256 {
		return c, events.NewError(events.CodeCapacity, "", 0, -1, "render",
			fmt.Sprintf("frame %dx%d exceeds offscreen limit 256x256", c.Width, c.Height), nil)
	}
	if c.FPSNum <= 0 || c.FPSDen <= 0 {
		return c, events.NewError(events.CodeInternal, "", 0, -1, "render", "non-positive frame rate", nil)
	}
	dur := int64(clock.NSPerSecond) * int64(c.FPSDen) / int64(c.FPSNum)
	if dur <= 0 {
		return c, events.NewError(events.CodeInternal, "", 0, -1, "render", "frame duration rounds to zero", nil)
	}
	if c.StepNS <= 0 {
		c.StepNS = dur
	}
	if c.MaxFrames <= 0 {
		c.MaxFrames = 240
	}
	if c.StepNS < dur/2 {
		return c, events.NewError(events.CodeInternal, "", 0, -1, "render",
			"frame step below half the frame duration would break alignment guarantees", nil)
	}
	return c, nil
}

// FrameDurationNS returns the nominal frame duration for the rate.
func FrameDurationNS(fpsNum, fpsDen int) int64 {
	if fpsNum <= 0 || fpsDen <= 0 {
		return 0
	}
	return int64(clock.NSPerSecond) * int64(fpsDen) / int64(fpsNum)
}

// Sequence renders frames over [StartNS, EndNS) with the given committed
// events. Only events that are active at a frame instant contribute pixels and
// alignment records; gaps are never rendered as content.
func RenderFrameSequence(cfg Config, committed []events.Event, endNS int64) (Sequence, *events.Error) {
	cfg, err := cfg.validate()
	if err != nil {
		return Sequence{}, err
	}
	if endNS < cfg.StartNS {
		// An empty interval is valid: it produces zero frames.
		return Sequence{StartNS: cfg.StartNS, EndNS: endNS, FrameDurationNS: cfg.StepNS}, nil
	}
	span := endNS - cfg.StartNS
	count := int(span / cfg.StepNS)
	if span%cfg.StepNS != 0 {
		count++
	}
	if count > cfg.MaxFrames {
		return Sequence{}, events.NewError(events.CodeCapacity, "", 0, -1, "render",
			fmt.Sprintf("frame count %d exceeds limit %d", count, cfg.MaxFrames), nil)
	}

	// Active events are looked up by instant; keep the committed set sorted.
	ordered := events.SortEvents(committed)
	seq := Sequence{StartNS: cfg.StartNS, EndNS: endNS, FrameDurationNS: cfg.StepNS}
	pixels := make([]uint16, cfg.Width*cfg.Height)

	for i := 0; i < count; i++ {
		at := cfg.StartNS + int64(i)*cfg.StepNS
		for j := range pixels {
			pixels[j] = 0
		}
		var active []events.Identity
		for _, e := range ordered {
			if e.At > at || e.At+e.Duration <= at {
				continue
			}
			active = append(active, e.Identity())
			paintEvent(pixels, cfg.Width, cfg.Height, e, i)
		}
		f := Frame{Index: i, AtNS: at, Pixels: append([]uint16(nil), pixels...), EventIDs: active}
		f.SHA256 = hashPixels(f.Pixels)
		f.MeanByte, f.MinByte, f.MaxByte = pixelStats(f.Pixels)
		if len(active) > 0 {
			f.AlignError = alignmentError(ordered, at, cfg.StepNS)
		} else {
			f.AlignError = cfg.StepNS
		}
		if f.AlignError > seq.MaxAlignError {
			seq.MaxAlignError = f.AlignError
		}
		seq.Frames = append(seq.Frames, f)
	}
	return seq, nil
}

// alignmentError measures the distance from a frame instant to the nearest
// active event boundary (start or end). It is zero when an event starts exactly
// on the frame.
func alignmentError(ordered []events.Event, at, step int64) int64 {
	best := step
	for _, e := range ordered {
		if e.At > at || e.At+e.Duration <= at {
			continue
		}
		for _, boundary := range []int64{e.At, e.At + e.Duration} {
			d := boundary - at
			if d < 0 {
				d = -d
			}
			if d < best {
				best = d
			}
		}
	}
	return best
}

// paintEvent marks pixels with a per-track colour band so frames visibly
// distinguish tracks and stay deterministic across runs.
func paintEvent(pixels []uint16, w, h int, e events.Event, frame int) {
	base := colorFor(e.Track)
	// A band whose vertical position depends only on the frame index and the
	// event identity keeps the output stable and disjoint per track.
	row := (frame + int(e.Seq)) % h
	col := int(e.Seq) % w
	for x := 0; x < w; x++ {
		if x%3 == col%3 {
			pixels[row*w+x] = base
		}
	}
}

func colorFor(track events.TrackID) uint16 {
	// RGB565 constants, one per unified track.
	switch track {
	case events.TrackVideo:
		return 0xF800 // red
	case events.TrackAudio:
		return 0x07E0 // green
	case events.TrackSubtitle:
		return 0x001F // blue
	case events.TrackChapter:
		return 0xFFE0 // yellow
	case events.TrackID3:
		return 0xF81F // magenta
	case events.TrackEmsg:
		return 0x07FF // cyan
	case events.TrackSEI:
		return 0xFC00 // orange
	default:
		return 0x8410 // grey
	}
}

// pixelStats computes the mean byte value and the byte extremes over the frame.
func pixelStats(pixels []uint16) (mean float64, lo, hi uint8) {
	if len(pixels) == 0 {
		return 0, 0, 0
	}
	lo, hi = 255, 0
	sum := 0
	for _, p := range pixels {
		r := byte((p >> 11) & 0x1F)
		g := byte((p >> 5) & 0x3F)
		b := byte(p & 0x1F)
		for _, v := range []byte{r, g, b} {
			sum += int(v)
			if v < lo {
				lo = v
			}
			if v > hi {
				hi = v
			}
		}
	}
	return float64(sum) / float64(len(pixels)*3), lo, hi
}

// hashPixels renders the canonical SHA-256 of the frame buffer.
func hashPixels(pixels []uint16) string {
	h := sha256.New()
	buf := make([]byte, 2)
	for _, p := range pixels {
		binary.BigEndian.PutUint16(buf, p)
		h.Write(buf)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// SequenceHash is the canonical digest of the whole frame sequence: frame
// instants, geometry and per-frame pixel digests in order.
func (s Sequence) SequenceHash() string {
	h := sha256.New()
	fmt.Fprintf(h, "frame-sequence-v1\nframes=%d\nstep=%d\n", len(s.Frames), s.FrameDurationNS)
	for _, f := range s.Frames {
		fmt.Fprintf(h, "%d|%d|%s\n", f.Index, f.AtNS, f.SHA256)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// SegmentHash groups frames into segments of n and digests each segment, so a
// manifest can report fragment-level hashes alongside per-frame ones.
func SegmentHashes(s Sequence, n int) []string {
	if n <= 0 {
		n = 1
	}
	var out []string
	for start := 0; start < len(s.Frames); start += n {
		end := start + n
		if end > len(s.Frames) {
			end = len(s.Frames)
		}
		var h hash.Hash = sha256.New()
		fmt.Fprintf(h, "segment|%d|%d\n", start, end)
		for _, f := range s.Frames[start:end] {
			fmt.Fprintf(h, "%s\n", f.SHA256)
		}
		out = append(out, hex.EncodeToString(h.Sum(nil)))
	}
	return out
}
