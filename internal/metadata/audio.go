// Package metadata normalizes per-track audio and video descriptors so all
// nodes interpret tracks identically. Incompatible values are returned as
// classified, locatable errors rather than silently dropped.
package metadata

import (
	"github.com/example/tsrepair/internal/model"
)

// ChannelLayout is the speaker layout declared by an audio track.
type ChannelLayout uint8

const (
	LayoutUnknown  ChannelLayout = 0
	LayoutMono     ChannelLayout = 1
	LayoutStereo   ChannelLayout = 2
	LayoutSurround ChannelLayout = 6 // 5.1
)

// AudioDescriptor is the raw view of one audio track.
type AudioDescriptor struct {
	PID            uint16
	Layout         ChannelLayout
	Channels       uint8
	SampleRate     uint32
	FrameSizeBytes int // PCM frame size in bytes; 0 disables divisibility checks
}

// NormalizedAudio is the resolved descriptor after config mapping.
type NormalizedAudio struct {
	PID            uint16
	Layout         ChannelLayout
	Channels       uint8
	SampleRate     uint32
	RateRemapped   bool
	LayoutRemapped bool
}

// AudioEvent records one conflict or repair for the run summary.
type AudioEvent struct {
	Kind   string // "rate-conflict" | "layout-remap" | "unknown-layout" | "indivisible"
	PID    uint16
	Offset int64
	Detail string
}

// AudioConfig drives deterministic normalization.
type AudioConfig struct {
	// RateMap redirects non-standard sample rates to canonical ones.
	RateMap map[uint32]uint32
	// LayoutMap redirects declared layouts to canonical ones.
	LayoutMap map[ChannelLayout]ChannelLayout
	// AllowedRates are accepted without remapping.
	AllowedRates map[uint32]bool
	// AllowedLayouts are accepted without remapping.
	AllowedLayouts map[ChannelLayout]bool
}

// DefaultAudioConfig covers the rates/layouts used by the demo fixtures.
func DefaultAudioConfig() AudioConfig {
	return AudioConfig{
		RateMap: map[uint32]uint32{
			44100: 48000, // non-canonical rate normalized to 48 kHz
		},
		LayoutMap: map[ChannelLayout]ChannelLayout{
			LayoutSurround: LayoutStereo, // downmix rule
		},
		AllowedRates: map[uint32]bool{48000: true, 24000: true, 16000: true},
		AllowedLayouts: map[ChannelLayout]bool{
			LayoutMono: true, LayoutStereo: true, LayoutSurround: true,
		},
	}
}

// AudioNormalizer applies one AudioConfig and records every event.
type AudioNormalizer struct {
	cfg    AudioConfig
	events []AudioEvent
	errs   int
}

// NewAudioNormalizer constructs a normalizer with the given config.
func NewAudioNormalizer(cfg AudioConfig) *AudioNormalizer {
	return &AudioNormalizer{cfg: cfg}
}

// Normalize resolves a descriptor. Unknown layouts and sample rates that have
// neither an allowed entry nor a mapping produce locatable errors.
func (n *AudioNormalizer) Normalize(d AudioDescriptor, offset int64) (*NormalizedAudio, *model.StreamError) {
	out := &NormalizedAudio{PID: d.PID, Channels: d.Channels}
	layout := d.Layout
	if mapped, ok := n.cfg.LayoutMap[layout]; ok {
		n.events = append(n.events, AudioEvent{
			Kind: "layout-remap", PID: d.PID, Offset: offset,
			Detail: layoutName(layout) + "->" + layoutName(mapped)})
		layout, out.LayoutRemapped = mapped, true
	}
	if !n.cfg.AllowedLayouts[layout] {
		n.errs++
		n.events = append(n.events, AudioEvent{
			Kind: "unknown-layout", PID: d.PID, Offset: offset,
			Detail: layoutName(d.Layout)})
		return nil, model.New(model.StageMetadata, model.SeverityRecoverable,
			"unknown audio channel layout").
			Wrap(model.ErrUnknownLayout).At(d.PID, offset)
	}
	out.Layout = layout

	rate := d.SampleRate
	if n.cfg.AllowedRates[rate] {
		// canonical rate, no action
	} else if mapped, ok := n.cfg.RateMap[rate]; ok {
		n.events = append(n.events, AudioEvent{
			Kind: "rate-conflict", PID: d.PID, Offset: offset,
			Detail: itoa(uint64(rate)) + "->" + itoa(uint64(mapped))})
		rate, out.RateRemapped = mapped, true
	} else {
		n.errs++
		n.events = append(n.events, AudioEvent{
			Kind: "rate-conflict", PID: d.PID, Offset: offset,
			Detail: "unmapped rate " + itoa(uint64(rate))})
		return nil, model.New(model.StageMetadata, model.SeverityRecoverable,
			"sample rate has no configured mapping").
			Wrap(model.ErrUnknownLayout).At(d.PID, offset)
	}
	out.SampleRate = rate
	return out, nil
}

// CheckDivisible verifies that payloadLen holds a whole number of frames.
func (n *AudioNormalizer) CheckDivisible(pid uint16, payloadLen, frameSize int, offset int64) *model.StreamError {
	if frameSize <= 0 || payloadLen%frameSize == 0 {
		return nil
	}
	n.errs++
	n.events = append(n.events, AudioEvent{
		Kind: "indivisible", PID: pid, Offset: offset,
		Detail: itoa(uint64(payloadLen)) + "%" + itoa(uint64(frameSize)) + "!=0"})
	return model.New(model.StageMetadata, model.SeverityRecoverable,
		"sample payload is not divisible by frame size").
		Wrap(model.ErrIndivisible).At(pid, offset)
}

// Events returns a copy of the recorded audit events.
func (n *AudioNormalizer) Events() []AudioEvent {
	return append([]AudioEvent(nil), n.events...)
}

// ErrorCount returns how many descriptors/samples were rejected.
func (n *AudioNormalizer) ErrorCount() int { return n.errs }

func layoutName(l ChannelLayout) string {
	switch l {
	case LayoutMono:
		return "mono"
	case LayoutStereo:
		return "stereo"
	case LayoutSurround:
		return "5.1"
	default:
		return "unknown(" + itoa(uint64(l)) + ")"
	}
}

func itoa(v uint64) string {
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
