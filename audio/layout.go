// Package audio performs deterministic audio conversion for publication.
//
// Input blocks are interleaved little-endian signed 16-bit PCM. Conversion
// reshapes channels to a target layout, resamples between integer sample rates
// by exact rational position, applies a loudness gain and reports truncation
// when a block ends part-way through a sample frame. No state is shared
// between tracks: a caller converts one track at a time, so a trailing partial
// frame is reported rather than carried over.
package audio

import (
	"fmt"

	"github.com/zld/media-regress/internal/error"
)

// Channel is one channel of a layout.
type Channel struct {
	Name string
}

// Layout is a named channel arrangement, ordered as stored.
type Layout struct {
	Name     string
	Channels []Channel
}

var layouts = map[string]Layout{
	"mono":   {Name: "mono", Channels: []Channel{{"M"}}},
	"stereo": {Name: "stereo", Channels: []Channel{{"L"}, {"R"}}},
	"5.1": {Name: "5.1", Channels: []Channel{
		{"L"}, {"R"}, {"C"}, {"LFE"}, {"Ls"}, {"Rs"},
	}},
	"7.1": {Name: "7.1", Channels: []Channel{
		{"L"}, {"R"}, {"C"}, {"LFE"}, {"Ls"}, {"Rs"}, {"Lb"}, {"Rb"},
	}},
}

// LayoutNames lists the supported layouts in stable order.
func LayoutNames() []string { return []string{"mono", "stereo", "5.1", "7.1"} }

// LookupLayout resolves a layout name.
func LookupLayout(name string) (Layout, error) {
	l, ok := layouts[name]
	if !ok {
		return Layout{}, merr.New(merr.StageAudio, merr.KindParse, 0, 0, "unknown audio layout %q", name)
	}
	return l, nil
}

func layoutForChannels(n int) string {
	switch n {
	case 1:
		return "mono"
	case 2:
		return "stereo"
	case 6:
		return "5.1"
	case 8:
		return "7.1"
	default:
		return ""
	}
}

// MapRemap describes how one target channel is produced from source channels.
// When Sources is empty the channel is silent (for example LFE when upmixing
// from stereo); otherwise the output is the weighted mean of Sources.
type MapRemap struct {
	Target  string
	Sources []int
}

// ChannelMap returns the up/downmix rules from src to dst. It is deterministic
// and has no side effects, so callers may cache it per conversion.
func ChannelMap(src, dst Layout) []MapRemap {
	out := make([]MapRemap, len(dst.Channels))
	index := make(map[string]int, len(src.Channels))
	for i, c := range src.Channels {
		index[c.Name] = i
	}
	for i, c := range dst.Channels {
		out[i] = MapRemap{Target: c.Name}
		switch {
		case c.Name == "M":
			// Mono target folds every source channel equally.
			for s := range src.Channels {
				out[i].Sources = append(out[i].Sources, s)
			}
		case c.Name == "LFE":
			if s, ok := index["LFE"]; ok {
				out[i].Sources = []int{s}
			}
		case c.Name == "C":
			if s, ok := index["C"]; ok {
				out[i].Sources = []int{s}
			} else if l, ok := index["L"]; ok {
				if r, ok := index["R"]; ok {
					out[i].Sources = []int{l, r}
				}
			}
		case c.Name == "L" || c.Name == "R" || c.Name == "Ls" || c.Name == "Rs" || c.Name == "Lb" || c.Name == "Rb":
			if s, ok := index[c.Name]; ok {
				out[i].Sources = []int{s}
				break
			}
			// Upmix a missing channel from the nearest defined one: a mono
			// source feeds every target channel, and side/back channels fall
			// back to their front counterpart.
			for _, alt := range append([]string{"M", "C"}, surroundFallback(c.Name)...) {
				if s, ok := index[alt]; ok {
					out[i].Sources = []int{s}
					break
				}
			}
		}
	}
	return out
}

func surroundFallback(name string) []string {
	switch name {
	case "Ls":
		return []string{"L"}
	case "Rs":
		return []string{"R"}
	case "Lb":
		return []string{"Ls", "L"}
	case "Rb":
		return []string{"Rs", "R"}
	default:
		return nil
	}
}

// Config specifies one track conversion.
type Config struct {
	SourceRate   uint32
	SourceLayout string
	TargetRate   uint32
	TargetLayout string
	// LoudnessLUFS is the integrated loudness target; gain is derived from the
	// measured input loudness. Zero disables loudness normalisation.
	LoudnessLUFS float64
}

// Converter applies one track's conversion parameters.
type Converter struct {
	SourceRate   uint32
	SourceLayout Layout
	TargetRate   uint32
	TargetLayout Layout
	TargetLUFS   float64
	// GainDB is set by MeasureGain and applied to every sample.
	GainDB float64
	remap  []MapRemap
}

// NewConverter validates a request and resolves the channel map.
func NewConverter(cfg Config) (*Converter, error) {
	if cfg.SourceRate == 0 || cfg.TargetRate == 0 {
		return nil, merr.New(merr.StageAudio, merr.KindInconsistent, 0, 0,
			"sample rates must be positive (source %d, target %d)", cfg.SourceRate, cfg.TargetRate)
	}
	src, err := LookupLayout(cfg.SourceLayout)
	if err != nil {
		return nil, err
	}
	dst, err := LookupLayout(cfg.TargetLayout)
	if err != nil {
		return nil, err
	}
	if cfg.LoudnessLUFS > 0 {
		return nil, merr.New(merr.StageAudio, merr.KindLimit, 0, 0,
			"loudness target %.2f LUFS is above full scale", cfg.LoudnessLUFS)
	}
	return &Converter{
		SourceRate:   cfg.SourceRate,
		SourceLayout: src,
		TargetRate:   cfg.TargetRate,
		TargetLayout: dst,
		TargetLUFS:   cfg.LoudnessLUFS,
		remap:        ChannelMap(src, dst),
	}, nil
}

// Block is the output of converting one input block.
type Block struct {
	Index        int
	Frames       int    // output frames
	PCM          []byte // interleaved s16le
	Truncated    bool   // the source block ended mid-frame
	Start        int64  // output frame position of the first produced frame
	SourceFrames int64
}

// String summarises the block for reporting.
func (b Block) String() string {
	return fmt.Sprintf("block=%d frames=%d truncated=%t", b.Index, b.Frames, b.Truncated)
}
