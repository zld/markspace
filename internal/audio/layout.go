// Package audio normalizes channel layouts and sample rates for composed
// audio tracks. Supported layouts are mapped deterministically; an unsupported
// layout fails only that track so other representation layers keep processing.
package audio

import (
	"fmt"

	"mediapack/internal/ingest"
	"mediapack/internal/model"
)

// Channel names in the project-supported canonical order.
const (
	ChL   = "L"
	ChR   = "R"
	ChC   = "C"
	ChLFE = "LFE"
	ChLs  = "Ls"
	ChRs  = "Rs"
)

// layoutMap maps a channel count onto the supported canonical layout.
var layoutMap = map[uint16][]string{
	1: {ChC},
	2: {ChL, ChR},
	6: {ChL, ChR, ChC, ChLFE, ChLs, ChRs},
}

// TrackStats reports one audio track's raw and normalized description.
type TrackStats struct {
	GlobalIndex     int               `json:"global_index"`
	InputChannels   uint16            `json:"input_channels"`
	InputSampleRate uint32            `json:"input_sample_rate"`
	InputSampleSize uint16            `json:"input_sample_size"`
	LayoutSupported bool              `json:"layout_supported"`
	ChannelMap      []string          `json:"channel_map,omitempty"`
	NormalizedRate  uint32            `json:"normalized_sample_rate"`
	NormalizationOK bool              `json:"normalization_ok"`
	Source          model.SourceRef   `json:"source"`
	Error           *model.FieldError `json:"error,omitempty"`
}

// Normalizer keeps the shared target sample rate and per-track statistics.
type Normalizer struct {
	targetRate uint32
	stats      []TrackStats
}

// NewNormalizer builds a normalizer targeting targetRate Hz.
func NewNormalizer(targetRate uint32) *Normalizer {
	if targetRate == 0 {
		targetRate = 48000
	}
	return &Normalizer{targetRate: targetRate}
}

// Normalize validates and normalizes one track. A returned error means the
// track failed; the Normalizer still records the failure and stays usable for
// subsequent tracks.
func (n *Normalizer) Normalize(globalIdx int, t *ingest.Track) (*TrackStats, error) {
	st := TrackStats{
		GlobalIndex:     globalIdx,
		InputChannels:   t.Channels,
		InputSampleRate: t.SampleRate,
		InputSampleSize: t.SampleSize,
		NormalizedRate:  n.targetRate,
		Source:          t.Source("audio"),
	}
	layout, ok := layoutMap[t.Channels]
	if !ok {
		st.LayoutSupported = false
		st.Error = model.NewFieldError(model.KindUnsupported, st.Source.File,
			st.Source.Offset, st.Source.Path,
			fmt.Sprintf("channel layout with %d channels is not supported", t.Channels))
		n.stats = append(n.stats, st)
		return &st, st.Error
	}
	st.LayoutSupported = true
	st.ChannelMap = append([]string(nil), layout...)
	if t.SampleRate == 0 {
		st.Error = model.NewFieldError(model.KindInvalidInput, st.Source.File,
			st.Source.Offset, st.Source.Path, "sample rate must be non-zero")
		n.stats = append(n.stats, st)
		return &st, st.Error
	}
	st.NormalizationOK = true
	n.stats = append(n.stats, st)
	return &st, nil
}

// Stats returns a copy of all recorded per-track statistics.
func (n *Normalizer) Stats() []TrackStats {
	out := make([]TrackStats, len(n.stats))
	copy(out, n.stats)
	return out
}

// TargetRate returns the shared normalized sample rate.
func (n *Normalizer) TargetRate() uint32 { return n.targetRate }

// SupportedChannelCounts lists the layouts the subset accepts.
func SupportedChannelCounts() []uint16 { return []uint16{1, 2, 6} }
