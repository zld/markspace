// Package audio normalizes channel layouts and sampling-rate boundaries.
//
// A layout is a named, ordered list of channels. Converting between layouts
// always goes through an explicit channel map: a mapping that silently drops a
// source channel is rejected, and an unknown layout is reported as a located
// fault rather than guessed.
package audio

import (
	"fmt"
	"strings"

	"mediatool/internal/fault"
)

// Channel is a canonical channel label.
type Channel string

// Canonical channel labels used by the known layouts.
const (
	ChMono Channel = "mono"
	ChFL   Channel = "FL"
	ChFR   Channel = "FR"
	ChFC   Channel = "FC"
	ChLFE  Channel = "LFE"
	ChBL   Channel = "BL"
	ChBR   Channel = "BR"
	ChSL   Channel = "SL"
	ChSR   Channel = "SR"
)

// Layout is a named ordered set of channels.
type Layout struct {
	Name     string    `json:"name"`
	Channels []Channel `json:"channels"`
}

// Len returns the channel count.
func (l Layout) Len() int { return len(l.Channels) }

// String renders the layout as name(count).
func (l Layout) String() string { return fmt.Sprintf("%s(%dch)", l.Name, len(l.Channels)) }

var known = map[string][]Channel{
	"mono":   {ChMono},
	"stereo": {ChFL, ChFR},
	"quad":   {ChFL, ChFR, ChBL, ChBR},
	"5.1":    {ChFL, ChFR, ChFC, ChLFE, ChBL, ChBR},
	"7.1":    {ChFL, ChFR, ChFC, ChLFE, ChBL, ChBR, ChSL, ChSR},
}

// Lookup returns a known layout by name (case-insensitive).
func Lookup(name string) (Layout, error) {
	key := strings.ToLower(strings.TrimSpace(name))
	chs, ok := known[key]
	if !ok {
		return Layout{}, fmt.Errorf("unknown audio layout %q", name)
	}
	out := make([]Channel, len(chs))
	copy(out, chs)
	return Layout{Name: key, Channels: out}, nil
}

// UnknownLayoutFault builds the located fault used when a layout name is not
// recognized. offset is the first byte of the metadata record that carried it.
func UnknownLayoutFault(name string, trackID uint32, offset int64) *fault.Fault {
	return (&fault.Fault{
		Kind:     fault.KindLayout,
		TrackID:  trackID,
		Offset:   offset,
		Recovery: fault.RecoverAbort,
		Severity: fault.SeverityFatal,
	}).WithDetail("unknown audio layout %q", name)
}

// ChannelMap is an explicit mapping from a source layout to an output layout.
// Index maps each output channel to a source channel index, or -1 for silence.
// Dropped lists source channels that are intentionally omitted; every channel
// not referenced by Index and not in Dropped causes an error, so no channel is
// lost silently.
type ChannelMap struct {
	From    Layout `json:"from"`
	To      Layout `json:"to"`
	Index   []int  `json:"index"`
	Dropped []int  `json:"dropped,omitempty"`
}

// Validate checks the map for consistency without touching media.
func (m ChannelMap) Validate() error {
	if len(m.Index) != m.To.Len() {
		return fmt.Errorf("channel map has %d outputs for %s (%d channels)", len(m.Index), m.To, m.To.Len())
	}
	used := make([]bool, m.From.Len())
	for _, src := range m.Index {
		if src == -1 {
			continue
		}
		if src < 0 || src >= m.From.Len() {
			return fmt.Errorf("channel map source index %d out of range for %s", src, m.From)
		}
		used[src] = true
	}
	for _, d := range m.Dropped {
		if d < 0 || d >= m.From.Len() {
			return fmt.Errorf("channel map dropped index %d out of range for %s", d, m.From)
		}
		used[d] = true
	}
	var lost []string
	for i, ok := range used {
		if !ok {
			lost = append(lost, string(m.From.Channels[i]))
		}
	}
	if len(lost) > 0 {
		return fmt.Errorf("channel map silently drops %s", strings.Join(lost, ","))
	}
	return nil
}

// Apply reorders interleaved samples according to the map. in holds
// frame-major interleaved samples for one frame batch; out holds To.Len()
// channels per frame. A malformed map yields a located, fatal layout fault.
func (m ChannelMap) Apply(in []float32, frames int, trackID uint32, offset int64) ([]float32, error) {
	if err := m.Validate(); err != nil {
		return nil, (&fault.Fault{
			Kind:     fault.KindLayout,
			TrackID:  trackID,
			Offset:   offset,
			Recovery: fault.RecoverAbort,
			Severity: fault.SeverityFatal,
		}).WithDetail("%v", err)
	}
	if len(in) != frames*m.From.Len() {
		return nil, (&fault.Fault{
			Kind:     fault.KindLayout,
			TrackID:  trackID,
			Offset:   offset,
			Recovery: fault.RecoverAbort,
			Severity: fault.SeverityFatal,
		}).WithDetail("expected %d interleaved samples for %d frames of %s, got %d",
			frames*m.From.Len(), frames, m.From, len(in))
	}
	out := make([]float32, frames*m.To.Len())
	for f := 0; f < frames; f++ {
		base := f * m.From.Len()
		for o, src := range m.Index {
			if src < 0 {
				out[f*m.To.Len()+o] = 0
				continue
			}
			out[f*m.To.Len()+o] = in[base+src]
		}
	}
	return out, nil
}

// DefaultMap builds the conventional mapping between two known layouts.
// It is explicit: the returned map can be inspected and stored in the
// manifest, and Validate reports any channel it would omit.
func DefaultMap(from, to Layout) (ChannelMap, error) {
	m := ChannelMap{From: from, To: to}
	if from.Name == to.Name {
		for i := range to.Channels {
			m.Index = append(m.Index, i)
		}
		return m, m.Validate()
	}
	pos := make(map[Channel]int, from.Len())
	for i, ch := range from.Channels {
		pos[ch] = i
	}
	for _, ch := range to.Channels {
		if i, ok := pos[ch]; ok {
			m.Index = append(m.Index, i)
		} else {
			m.Index = append(m.Index, -1)
		}
	}
	// Downmixes intentionally drop channels that the target layout cannot
	// represent; record them so the omission is auditable.
	used := make(map[int]bool)
	for _, i := range m.Index {
		if i >= 0 {
			used[i] = true
		}
	}
	for i := range from.Channels {
		if !used[i] {
			m.Dropped = append(m.Dropped, i)
		}
	}
	if err := m.Validate(); err != nil {
		return m, err
	}
	return m, nil
}

// RateConverter plans conversion between two constant sampling rates.
// Only integral ratios are supported; any other boundary is a located,
// fatal sample-rate fault so the boundary cannot be crossed by accident.
type RateConverter struct {
	From int `json:"from"`
	To   int `json:"to"`
}

// NewRateConverter validates the rate pair.
func NewRateConverter(from, to int) (RateConverter, error) {
	rc := RateConverter{From: from, To: to}
	if from <= 0 || to <= 0 {
		return rc, (&fault.Fault{Kind: fault.KindSampleRate, Recovery: fault.RecoverAbort, Severity: fault.SeverityFatal}).
			WithDetail("invalid sample rate boundary %d -> %d", from, to)
	}
	if from%to != 0 && to%from != 0 {
		return rc, (&fault.Fault{Kind: fault.KindSampleRate, Recovery: fault.RecoverAbort, Severity: fault.SeverityFatal}).
			WithDetail("non-integral sample rate boundary %d -> %d", from, to)
	}
	return rc, nil
}

// Resample converts a frame count across the boundary. The ratio is integral
// by construction, so no partial output frames are produced.
func (rc RateConverter) Resample(frames int) int {
	return frames * rc.To / rc.From
}

// Locate attaches a track and byte offset to a rate-boundary fault.
func Locate(err error, trackID uint32, offset int64) error {
	f, ok := fault.As(err)
	if !ok {
		return err
	}
	g := *f
	g.TrackID = trackID
	g.Offset = offset
	return &g
}
