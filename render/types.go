// Package render produces deterministic off-screen frames and audio chunks and
// measures them, so a recovered timeline can be reviewed and compared across
// runs without any display or audio device.
//
// Everything here is a pure function of the timeline it is given: the same
// segments always yield byte-identical frames and audio, and therefore the same
// hashes.
package render

import (
	"mediarc/model"
)

const stream = "render"

// Canvas is the off-screen frame size.
type Canvas struct {
	Width  int
	Height int
}

// DefaultCanvas is a small, fast canvas; pixel statistics are the same shape at
// any size, but a fixed size keeps hashes comparable.
func DefaultCanvas() Canvas { return Canvas{Width: 64, Height: 36} }

func (c Canvas) validate() (Canvas, error) {
	if c.Width == 0 && c.Height == 0 {
		c = DefaultCanvas()
	}
	if c.Width <= 0 || c.Height <= 0 {
		return Canvas{}, model.New(model.CodeInvalidArgument, stream,
			"canvas width and height must be positive")
	}
	if c.Width > 4096 || c.Height > 4096 {
		return Canvas{}, model.New(model.CodeInvalidArgument, stream,
			"canvas dimensions must not exceed 4096")
	}
	return c, nil
}

// Pixels returns the pixel count.
func (c Canvas) Pixels() int { return c.Width * c.Height }

// RGB is one pixel.
type RGB struct {
	R, G, B uint8
}

// Frame is a rendered "video" frame. It is deterministic: a frame carries a solid
// background whose colour is derived from the timeline position and its kind,
// plus a one-pixel marker bar whose position encodes the frame index. That gives
// the metric pass real per-frame variation to measure without a codec.
type Frame struct {
	// Index is the 0-based frame number in the timeline.
	Index int
	// StartNS is the frame's timeline position.
	StartNS int64
	// DurationNS is the frame's timeline length.
	DurationNS int64
	// Recovered marks frames that stand for missing media.
	Recovered bool
	Canvas    Canvas
	// Pixels is row-major RGB.
	Pixels []RGB
	// Hash is FNV-1a over the pixel bytes.
	Hash uint64
}

// AudioChunk is a rendered audio chunk, 16-bit signed little-endian interleaved.
type AudioChunk struct {
	// Index is the 0-based chunk number.
	Index int
	// StartNS and DurationNS place it on the timeline.
	StartNS    int64
	DurationNS int64
	// Recovered marks silence that replaced missing packets.
	Recovered bool
	// SampleRate and Channels describe the shape of Data.
	SampleRate int
	Channels   int
	// Data is the PCM payload.
	Data []byte
	// Hash is FNV-1a over Data.
	Hash uint64
}

// Timeline is the input to rendering: ordered media slots derived from the
// recovered stream and container.
type Timeline struct {
	// Slots are the frames/audio intervals to render, in timeline order.
	Slots []Slot
}

// SlotKind selects what a slot renders as.
type SlotKind int

const (
	// SlotMedia is delivered media.
	SlotMedia SlotKind = iota
	// SlotSilence is recovered silence.
	SlotSilence
	// SlotGap is an unfilled hole; it is rendered but flagged.
	SlotGap
)

// String renders the slot kind for reports.
func (k SlotKind) String() string {
	switch k {
	case SlotMedia:
		return "media"
	case SlotSilence:
		return "silence"
	case SlotGap:
		return "gap"
	default:
		return "unknown"
	}
}

// Slot is one slice of the timeline to render.
type Slot struct {
	Kind       SlotKind
	StartNS    int64
	DurationNS int64
	// Seq names the source position, used to keep ordering deterministic.
	Seq uint64
}

// Config fixes the render parameters.
type Config struct {
	Canvas     Canvas
	SampleRate int
	Channels   int
	// FrameDurationNS is the nominal frame length. Zero means 1/25 s.
	FrameDurationNS int64
}

func (c Config) validate() (Config, error) {
	canvas, err := c.Canvas.validate()
	if err != nil {
		return Config{}, err
	}
	c.Canvas = canvas
	if c.SampleRate == 0 {
		c.SampleRate = 48000
	}
	if c.Channels == 0 {
		c.Channels = 2
	}
	if c.SampleRate < 0 || c.Channels < 0 {
		return Config{}, model.New(model.CodeInvalidArgument, stream, "sample rate and channels must not be negative")
	}
	if c.FrameDurationNS == 0 {
		c.FrameDurationNS = 40 * 1_000_000
	}
	if c.FrameDurationNS < 0 {
		return Config{}, model.New(model.CodeInvalidArgument, stream, "frame duration must not be negative")
	}
	return c, nil
}
