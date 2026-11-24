// Package audio normalises channel layouts and sample rates for interleaved
// PCM tracks. Ambiguous metadata fails loudly rather than being guessed.
package audio

import (
	"fmt"
	"sort"

	"mediacheck/ebml"
	"mediacheck/timeline"
)

// Channel positions recognised by the fixture convention.
const (
	PosFL uint32 = 1 // front left
	PosFR uint32 = 2 // front right
	PosFC uint32 = 3 // front centre
	PosLFE uint32 = 4 // low frequency effects
	PosBL uint32 = 5 // back left
	PosBR uint32 = 6 // back right
)

// supportedPositions is the closed set this tool knows how to label.
var supportedPositions = map[uint32]string{
	PosFL: "FL", PosFR: "FR", PosFC: "FC",
	PosLFE: "LFE", PosBL: "BL", PosBR: "BR",
}

// namedLayouts maps a declared layout name to its canonical ordered positions.
var namedLayouts = map[string][]uint32{
	"mono":         {PosFC},
	"stereo":       {PosFL, PosFR},
	"5.1 surround": {PosFL, PosFR, PosFC, PosLFE, PosBL, PosBR},
}

// Layout is the resolved, validated mapping for one audio track.
type Layout struct {
	Channels int
	Positions []uint32 // one entry per channel, in storage order
	Source    string   // "map" | "name" | "config"
}

// Labels renders positions as reviewer-friendly channel tags.
func (l Layout) Labels() []string {
	out := make([]string, len(l.Positions))
	for i, p := range l.Positions {
		out[i] = supportedPositions[p]
	}
	return out
}

// Config carries explicit fallbacks for tracks whose metadata is missing.
type Config struct {
	// FallbackLayout is applied when a track declares no layout at all.
	FallbackLayout string
	// RequireExplicit makes missing metadata an error instead of a fallback.
	RequireExplicit bool
}

// Resolve validates the track's channel metadata and returns a canonical
// layout. It rejects duplicate positions, unsupported positions, maps that
// disagree with the channel count, and named layouts that cannot be honoured.
func Resolve(track *ebml.TrackSpec, cfg Config) (*Layout, error) {
	if track == nil {
		return nil, timeline.New(timeline.CatConfig, "nil audio track")
	}
	uid := track.UID
	hasMap := len(track.ChannelMap) > 0
	hasName := track.LayoutName != ""

	switch {
	case hasMap:
		return resolveMap(track, uid)
	case hasName:
		return resolveName(track, cfg, uid)
	default:
		return resolveMissing(track, cfg, uid)
	}
}

func resolveMap(track *ebml.TrackSpec, uid uint32) (*Layout, error) {
	if len(track.ChannelMap) != int(track.Channels) {
		return nil, timeline.New(timeline.CatUnsupported,
			fmt.Sprintf("channel map has %d rows but track declares %d channels",
				len(track.ChannelMap), track.Channels)).WithTrack(uid)
	}
	rows := append([]ebml.ChannelMapEntry(nil), track.ChannelMap...)
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].Order < rows[j].Order })
	seen := make(map[uint32]bool, len(rows))
	positions := make([]uint32, 0, len(rows))
	for _, row := range rows {
		label, ok := supportedPositions[row.Position]
		if !ok {
			return nil, timeline.New(timeline.CatUnsupported,
				fmt.Sprintf("unsupported channel position %d", row.Position)).
				WithTrack(uid)
		}
		if seen[row.Position] {
			return nil, timeline.New(timeline.CatUnsupported,
				fmt.Sprintf("duplicate channel mapping for position %s", label)).
				WithTrack(uid)
		}
		seen[row.Position] = true
		positions = append(positions, row.Position)
	}
	return &Layout{Channels: len(positions), Positions: positions, Source: "map"}, nil
}

func resolveName(track *ebml.TrackSpec, cfg Config, uid uint32) (*Layout, error) {
	positions, ok := namedLayouts[track.LayoutName]
	if !ok {
		return nil, timeline.New(timeline.CatUnsupported,
			fmt.Sprintf("unknown channel layout %q", track.LayoutName)).
			WithTrack(uid)
	}
	if len(positions) != int(track.Channels) {
		return nil, timeline.New(timeline.CatUnsupported,
			fmt.Sprintf("layout %q needs %d channels, track declares %d",
				track.LayoutName, len(positions), track.Channels)).
			WithTrack(uid)
	}
	// Named layouts are canonical by construction; positional uniqueness is
	// enforced on the explicit map path.
	return &Layout{Channels: len(positions), Positions: append([]uint32(nil), positions...),
		Source: "name"}, nil
}

func resolveMissing(track *ebml.TrackSpec, cfg Config, uid uint32) (*Layout, error) {
	if cfg.RequireExplicit || cfg.FallbackLayout == "" {
		return nil, timeline.New(timeline.CatConfig,
			"missing channel layout and no explicit fallback configured").
			WithTrack(uid)
	}
	positions, ok := namedLayouts[cfg.FallbackLayout]
	if !ok || len(positions) != int(track.Channels) {
		return nil, timeline.New(timeline.CatConfig,
			fmt.Sprintf("fallback layout %q does not match %d channels",
				cfg.FallbackLayout, track.Channels)).
			WithTrack(uid)
	}
	return &Layout{Channels: len(positions), Positions: append([]uint32(nil), positions...),
		Source: "config"}, nil
}
