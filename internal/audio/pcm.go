// Package audio implements the PCM-side of the kernel: channel layout
// normalization, sample-rate conversion, loudness measurement and gain, and
// the quality statistics every stage reports. It never derives time itself;
// positions come from the timeline package.
package audio

import (
	"fmt"
	"strings"
)

// Layout is a named channel layout with a fixed, explicit channel order.
// Unknown layouts are rejected rather than guessed.
type Layout struct {
	Name     string
	Channels []Channel
}

// Channel names one speaker position in canonical order.
type Channel string

const (
	ChFrontLeft   Channel = "FL"
	ChFrontRight  Channel = "FR"
	ChFrontCenter Channel = "FC"
	ChLFE         Channel = "LFE"
	ChBackLeft    Channel = "BL"
	ChBackRight   Channel = "BR"
	ChSideLeft    Channel = "SL"
	ChSideRight   Channel = "SR"
)

// Canonical layouts. Order within Channels is the interleaving order.
var (
	Mono       = Layout{Name: "mono", Channels: []Channel{ChFrontCenter}}
	Stereo     = Layout{Name: "stereo", Channels: []Channel{ChFrontLeft, ChFrontRight}}
	LCR        = Layout{Name: "lcr", Channels: []Channel{ChFrontLeft, ChFrontRight, ChFrontCenter}}
	Quad       = Layout{Name: "quad", Channels: []Channel{ChFrontLeft, ChFrontRight, ChBackLeft, ChBackRight}}
	Surround51 = Layout{Name: "5.1", Channels: []Channel{
		ChFrontLeft, ChFrontRight, ChFrontCenter, ChLFE, ChBackLeft, ChBackRight,
	}}
	Surround51Side = Layout{Name: "5.1-side", Channels: []Channel{
		ChFrontLeft, ChFrontRight, ChFrontCenter, ChLFE, ChSideLeft, ChSideRight,
	}}
)

// ParseLayout accepts the names the CLI exposes. It is strict: an unknown
// name is an error, not a silent fallback to stereo.
func ParseLayout(name string) (Layout, error) {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "mono", "1", "1.0":
		return Mono, nil
	case "stereo", "2", "2.0":
		return Stereo, nil
	case "lcr", "3", "3.0":
		return LCR, nil
	case "quad", "4", "4.0":
		return Quad, nil
	case "5.1", "51", "surround51":
		return Surround51, nil
	case "5.1-side", "51side":
		return Surround51Side, nil
	default:
		return Layout{}, fmt.Errorf("audio: unknown channel layout %q", name)
	}
}

// LayoutForChannels infers the layout of a source with no explicit channel
// map. Only count-based layouts that are unambiguous are accepted; anything
// else is rejected so an unknown order never gets remapped as if it were
// known.
func LayoutForChannels(n int) (Layout, error) {
	switch n {
	case 1:
		return Mono, nil
	case 2:
		return Stereo, nil
	case 3:
		return LCR, nil
	case 4:
		return Quad, nil
	case 6:
		// 6 channels without a channel map is ambiguous between 5.1 and
		// 5.1-side; refuse rather than pick one.
		return Layout{}, fmt.Errorf("audio: 6-channel source has no declared order; provide an explicit channel map")
	default:
		return Layout{}, fmt.Errorf("audio: no canonical layout for %d channels", n)
	}
}

// NumChannels returns the channel count of the layout.
func (l Layout) NumChannels() int { return len(l.Channels) }

// Has reports whether the layout contains a channel position.
func (l Layout) Has(c Channel) bool {
	for _, x := range l.Channels {
		if x == c {
			return true
		}
	}
	return false
}

// Valid reports whether the layout is self-consistent.
func (l Layout) Valid() bool {
	if l.Name == "" || len(l.Channels) == 0 || len(l.Channels) > 32 {
		return false
	}
	seen := make(map[Channel]bool, len(l.Channels))
	for _, c := range l.Channels {
		if c == "" || seen[c] {
			return false
		}
		seen[c] = true
	}
	return true
}

// Buffer is interleaved float64 PCM plus the layout it is stored in. Samples
// are nominally in [-1,1]; values outside indicate clipping from upstream, not
// something this package silently fixes.
type Buffer struct {
	Layout   Layout
	Frames   int
	Data     []float64 // len == Frames * Layout.Channels
	Start    int64     // first frame index on the track's output grid
	Metadata BufferMeta
}

// BufferMeta carries provenance so gaps and resets survive processing.
type BufferMeta struct {
	Gaps          int  // number of gaps this segment was split from
	Discontinuity bool // true when the segment starts after a gap
	ResetCount    int  // filter resets applied at segment boundaries
}

// NewBuffer allocates an interleaved buffer and validates its layout.
func NewBuffer(l Layout, frames int) (*Buffer, error) {
	if !l.Valid() {
		return nil, fmt.Errorf("audio: invalid layout %q", l.Name)
	}
	if frames < 0 {
		return nil, fmt.Errorf("audio: negative frame count %d", frames)
	}
	return &Buffer{Layout: l, Frames: frames, Data: make([]float64, frames*len(l.Channels))}, nil
}

// Frame returns the interleaved slice for frame i.
func (b *Buffer) Frame(i int) []float64 {
	n := len(b.Layout.Channels)
	return b.Data[i*n : (i+1)*n]
}

// Clone copies the buffer including metadata.
func (b *Buffer) Clone() *Buffer {
	c := *b
	c.Data = append([]float64(nil), b.Data...)
	return &c
}

// Validate checks the buffer's internal invariants.
func (b *Buffer) Validate() error {
	if !b.Layout.Valid() {
		return fmt.Errorf("audio: buffer has invalid layout %q", b.Layout.Name)
	}
	want := b.Frames * len(b.Layout.Channels)
	if len(b.Data) != want {
		return fmt.Errorf("audio: buffer length %d, want %d", len(b.Data), want)
	}
	for i, v := range b.Data {
		if v != v { // NaN check without importing math for one line
			return fmt.Errorf("audio: NaN sample at index %d", i)
		}
	}
	return nil
}
