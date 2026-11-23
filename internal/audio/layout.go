package audio

import (
	"fmt"
)

// Layout names a channel arrangement.
type Layout string

// Supported layouts.
const (
	Mono   Layout = "mono"
	Stereo Layout = "stereo"
	Five1  Layout = "5.1"
)

// Layouts lists the layouts this library can produce.
var Layouts = []Layout{Mono, Stereo, Five1}

// Channels returns the channel count for a layout.
func (l Layout) Channels() (int, error) {
	switch l {
	case Mono:
		return 1, nil
	case Stereo:
		return 2, nil
	case Five1:
		return 6, nil
	default:
		return 0, fmt.Errorf("audio: unknown layout %q", l)
	}
}

// Valid reports whether the layout is supported.
func (l Layout) Valid() bool {
	_, err := l.Channels()
	return err == nil
}

// ParseLayout accepts the manifest spellings.
func ParseLayout(s string) (Layout, error) {
	switch s {
	case "mono":
		return Mono, nil
	case "stereo":
		return Stereo, nil
	case "5.1", "5_1", "five1":
		return Five1, nil
	default:
		return "", fmt.Errorf("audio: unknown layout %q", s)
	}
}

// ChannelMap describes how source channels map onto destination channels.
// Each destination index holds a list of (source channel, gain) contributions.
type ChannelMap struct {
	From, To Layout
	fromN    int
	toN      int
	weights  [][]contribution
	// Dropped lists source channels that contribute to no destination; must
	// stay empty or the conversion is refused.
	Dropped []int
	// Filled lists destination channels with no source contribution.
	Filled []int
}

type contribution struct {
	channel int
	gain    float64
}

// BuildChannelMap derives the matrix for a conversion.  Conversions never
// silently discard a source channel: every source channel either contributes
// to a destination or appears in Dropped, and a non-empty Dropped list is an
// error at conversion time.
func BuildChannelMap(from, to Layout) (*ChannelMap, error) {
	fromN, err := from.Channels()
	if err != nil {
		return nil, err
	}
	toN, err := to.Channels()
	if err != nil {
		return nil, err
	}
	m := &ChannelMap{From: from, To: to, fromN: fromN, toN: toN}
	m.weights = make([][]contribution, toN)
	switch {
	case from == to:
		for i := 0; i < toN; i++ {
			m.weights[i] = []contribution{{channel: i, gain: 1}}
		}
	case from == Mono && to == Stereo:
		// Mono is duplicated onto both front channels; nothing is invented.
		for i := 0; i < toN; i++ {
			m.weights[i] = []contribution{{channel: 0, gain: 1}}
		}
	case from == Mono && to == Five1:
		// Mono feeds the centre channel; the other five are zero-filled.
		m.weights[0] = nil
		m.weights[1] = nil
		m.weights[2] = []contribution{{0, 1}}
		for i := 3; i < toN; i++ {
			m.weights[i] = nil
		}
	case from == Stereo && to == Five1:
		// Front L/R pass through unchanged; the four absent channels are
		// zero-filled rather than upmixed from unrelated content.
		m.weights[0] = []contribution{{0, 1}}
		m.weights[1] = []contribution{{1, 1}}
		for i := 2; i < toN; i++ {
			m.weights[i] = nil
		}
	case from == Stereo && to == Mono:
		// Every source channel contributes; a downmix cannot zero-fill.
		m.weights[0] = []contribution{{0, 0.5}, {1, 0.5}}
	case from == Five1 && to == Stereo:
		// L = FL + 0.5*C + 0.25*SL + 0.125*LFE,
		// R = FR + 0.5*C + 0.25*SR + 0.125*LFE.  LFE is attenuated, never
		// dropped, so no source channel vanishes silently.
		m.weights[0] = []contribution{{0, 1}, {2, 0.5}, {3, 0.25}, {5, 0.125}}
		m.weights[1] = []contribution{{1, 1}, {2, 0.5}, {4, 0.25}, {5, 0.125}}
	case from == Five1 && to == Mono:
		m.weights[0] = []contribution{{0, 0.4}, {1, 0.4}, {2, 0.5}, {3, 0.15}, {4, 0.15}, {5, 0.07}}
	default:
		return nil, fmt.Errorf("audio: unsupported %s to %s conversion", from, to)
	}
	// Verify no source channel is dropped without declaration.
	m.computeDiagnostics()
	if len(m.Dropped) > 0 {
		return nil, fmt.Errorf("audio: conversion %s->%s would drop source channels %v", from, to, m.Dropped)
	}
	return m, nil
}

func (m *ChannelMap) computeDiagnostics() {
	used := make([]bool, m.fromN)
	for _, dst := range m.weights {
		for _, c := range dst {
			if c.gain != 0 && c.channel < len(used) {
				used[c.channel] = true
			}
		}
	}
	m.Dropped = nil
	for i, u := range used {
		if !u {
			m.Dropped = append(m.Dropped, i)
		}
	}
	m.Filled = nil
	filled := make([]bool, m.toN)
	for i, dst := range m.weights {
		for _, c := range dst {
			if c.gain != 0 {
				filled[i] = true
			}
		}
	}
	for i, f := range filled {
		if !f {
			m.Filled = append(m.Filled, i)
		}
	}
}

// Apply maps interleaved frames into the destination layout.  The input length
// must be a whole number of source frames.  Destination channels without any
// source contribution are explicitly zero-filled.
func (m *ChannelMap) Apply(in []float64) ([]float64, error) {
	if len(in)%m.fromN != 0 {
		return nil, ErrBadBlock
	}
	frames := len(in) / m.fromN
	out := make([]float64, frames*m.toN)
	for f := 0; f < frames; f++ {
		base := f * m.fromN
		obase := f * m.toN
		for d, contribs := range m.weights {
			var acc float64
			for _, c := range contribs {
				acc += c.gain * in[base+c.channel]
			}
			out[obase+d] = acc
		}
	}
	return out, nil
}

// SourceChannels and DestChannels expose counts for buffer sizing.
func (m *ChannelMap) SourceChannels() int { return m.fromN }
func (m *ChannelMap) DestChannels() int   { return m.toN }
