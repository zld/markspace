// Package maptrack validates stream descriptors and maps interleaved input
// channels onto explicit target tracks. Ambiguous layouts, mismatched sample
// rates and contradictory channel counts are rejected with fault errors
// instead of silently guessing a mapping.
package maptrack

import (
	"fmt"

	"mediad/internal/fault"
)

// Layout describes one PCM stream's channel arrangement.
type Layout struct {
	Channels      uint16
	SampleRate    uint32
	BitsPerSample uint16
	// ChannelOrder names the input channels positionally, e.g.
	// ["L","R"] for stereo or ["L","R","C","LFE","BL","BR"] for 5.1.
	ChannelOrder []string
}

// FrameSize is the byte width of one interleaved frame.
func (l Layout) FrameSize() int {
	return int(l.Channels) * int(l.BitsPerSample) / 8
}

// SampleSize is the byte width of a single channel sample.
func (l Layout) SampleSize() int { return int(l.BitsPerSample) / 8 }

// Mapping binds a target track to one named input channel.
type Mapping struct {
	TrackID       int
	SourceChannel string
}

// Plan is a validated, ambiguity-free demultiplexing plan.
type Plan struct {
	Source  Layout
	Tracks  []Mapping
	indices []int // source channel index per target track, parallel to Tracks
}

// TrackCount reports the number of target tracks.
func (p *Plan) TrackCount() int { return len(p.Tracks) }

// SourceIndex returns the input channel index feeding track i.
func (p *Plan) SourceIndex(track int) int { return p.indices[track] }

// Validate checks that a layout only uses the explicitly supported PCM
// profiles and that every channel has a unique, consistent name.
func Validate(l Layout) error {
	if l.Channels < 1 || l.Channels > 8 {
		return fault.New(fault.CatMapping, "maptrack.validate", true,
			"channel count must be between 1 and 8")
	}
	switch l.BitsPerSample {
	case 16, 24, 32:
	default:
		return fault.New(fault.CatMapping, "maptrack.validate", true,
			"only 16/24/32-bit PCM samples are supported")
	}
	if l.SampleRate == 0 {
		return fault.New(fault.CatMapping, "maptrack.validate", true,
			"sample rate must be non-zero")
	}
	if int(l.Channels) != len(l.ChannelOrder) {
		return fault.New(fault.CatHeader, "maptrack.validate", true,
			"channel order length contradicts channel count")
	}
	seen := make(map[string]bool, l.Channels)
	for _, name := range l.ChannelOrder {
		if name == "" {
			return fault.New(fault.CatMapping, "maptrack.validate", true,
				"channel names must be non-empty")
		}
		if seen[name] {
			return fault.New(fault.CatMapping, "maptrack.validate", true,
				"duplicate channel name makes the layout ambiguous: "+name)
		}
		seen[name] = true
	}
	return nil
}

// Build validates the source layout and target mappings, then freezes a plan.
// Every input channel must be mapped exactly once and every mapping must
// resolve to a declared source channel.
func Build(src Layout, mappings []Mapping) (*Plan, error) {
	if err := Validate(src); err != nil {
		return nil, err
	}
	if len(mappings) == 0 {
		return nil, fault.New(fault.CatMapping, "maptrack.build", true,
			"at least one target track mapping is required")
	}
	indexByName := make(map[string]int, src.Channels)
	for i, name := range src.ChannelOrder {
		indexByName[name] = i
	}
	usedSource := make([]bool, src.Channels)
	usedTrack := make(map[int]bool, len(mappings))
	indices := make([]int, len(mappings))
	for i, m := range mappings {
		if m.TrackID < 0 {
			return nil, fault.New(fault.CatMapping, "maptrack.build", true,
				"track ids must be non-negative")
		}
		if usedTrack[m.TrackID] {
			return nil, fault.New(fault.CatMapping, "maptrack.build", true,
				fmt.Sprintf("track %d is assigned twice", m.TrackID))
		}
		idx, ok := indexByName[m.SourceChannel]
		if !ok {
			return nil, fault.New(fault.CatMapping, "maptrack.build", true,
				"mapping references unknown source channel: "+m.SourceChannel)
		}
		if usedSource[idx] {
			return nil, fault.New(fault.CatMapping, "maptrack.build", true,
				"source channel mapped to multiple tracks: "+m.SourceChannel)
		}
		usedSource[idx] = true
		usedTrack[m.TrackID] = true
		indices[i] = idx
	}
	for i, used := range usedSource {
		if !used {
			return nil, fault.New(fault.CatMapping, "maptrack.build", true,
				"unmapped source channel: "+src.ChannelOrder[i])
		}
	}
	cp := make([]Mapping, len(mappings))
	copy(cp, mappings)
	return &Plan{Source: src, Tracks: cp, indices: indices}, nil
}

// DefaultMonoPlan demultiplexes every input channel into its own mono track
// preserving source order.
func DefaultMonoPlan(src Layout) (*Plan, error) {
	if err := Validate(src); err != nil {
		return nil, err
	}
	mappings := make([]Mapping, src.Channels)
	for i, name := range src.ChannelOrder {
		mappings[i] = Mapping{TrackID: i, SourceChannel: name}
	}
	return Build(src, mappings)
}

// SplitFrame deinterleaves one whole sample frame into per-track sample
// bytes. The returned slice is parallel to Plan.Tracks; the total byte count
// equals the input, so no sample bytes are dropped or synthesized.
func (p *Plan) SplitFrame(frame []byte) ([][]byte, error) {
	if len(frame) != p.Source.FrameSize() {
		return nil, fault.New(fault.CatAlignment, "maptrack.split", true,
			fmt.Sprintf("frame is %d bytes, expected %d",
				len(frame), p.Source.FrameSize()))
	}
	sample := p.Source.SampleSize()
	out := make([][]byte, len(p.Tracks))
	for track, srcIdx := range p.indices {
		start := srcIdx * sample
		out[track] = append([]byte(nil), frame[start:start+sample]...)
	}
	return out, nil
}
