package audio

import (
	"fmt"
)

// Mix is one destination channel's contribution rule. A mix entry maps a
// destination position to weighted source positions.
type Mix struct {
	Dst    Channel
	Inputs []WeightedInput
}

// WeightedInput is one source channel and its linear gain.
type WeightedInput struct {
	Src    Channel
	Weight float64
}

// Layout converter strategy is fixed: remap by position when both layouts name
// their channels, downmix with explicit weights to mono, and reject anything
// whose order cannot be proven.
var (
	monoMix = []WeightedInput{
		{Src: ChFrontLeft, Weight: 0.5},
		{Src: ChFrontRight, Weight: 0.5},
		{Src: ChFrontCenter, Weight: 1.0},
		{Src: ChLFE, Weight: 0.0},
		{Src: ChBackLeft, Weight: 0.5},
		{Src: ChBackRight, Weight: 0.5},
		{Src: ChSideLeft, Weight: 0.5},
		{Src: ChSideRight, Weight: 0.5},
	}
	stereoMix = []Mix{
		{Dst: ChFrontLeft, Inputs: []WeightedInput{
			{Src: ChFrontLeft, Weight: 1.0},
			{Src: ChFrontCenter, Weight: 0.707},
			{Src: ChLFE, Weight: 0.0},
			{Src: ChBackLeft, Weight: 0.707},
			{Src: ChSideLeft, Weight: 0.707},
		}},
		{Dst: ChFrontRight, Inputs: []WeightedInput{
			{Src: ChFrontRight, Weight: 1.0},
			{Src: ChFrontCenter, Weight: 0.707},
			{Src: ChLFE, Weight: 0.0},
			{Src: ChBackRight, Weight: 0.707},
			{Src: ChSideRight, Weight: 0.707},
		}},
	}
)

// ChannelMapError reports a layout conversion that cannot be proven safe.
type ChannelMapError struct {
	From, To string
	Channel  Channel
	Reason   string
}

func (e *ChannelMapError) Error() string {
	return fmt.Sprintf("audio: cannot map %s to %s: channel %s %s", e.From, e.To, e.Channel, e.Reason)
}

// ConvertLayout maps src into dst. Rules, in order:
//
//  1. Identical layouts are returned unchanged (no copy of sample values is
//     needed for mono/stereo passthrough other than the interleave order).
//  2. Same channel count with every destination position present in the
//     source is a pure permutation.
//  3. Mono and stereo destinations use the fixed downmix weights above.
//  4. Anything else is an error naming the first unmappable channel.
//
// Silence is never used to fill a channel the source does not carry.
func ConvertLayout(src *Buffer, dst Layout) (*Buffer, error) {
	if err := src.Validate(); err != nil {
		return nil, err
	}
	if !dst.Valid() {
		return nil, fmt.Errorf("audio: invalid destination layout %q", dst.Name)
	}
	if src.Layout.Name == dst.Name && sameChannels(src.Layout, dst) {
		out := src.Clone()
		return out, nil
	}
	if canPermute(src.Layout, dst) {
		return permute(src, dst), nil
	}
	switch dst.Name {
	case Mono.Name:
		return downmixMono(src, dst)
	case Stereo.Name:
		return downmixStereo(src, dst)
	}
	for _, c := range dst.Channels {
		if !src.Layout.Has(c) {
			return nil, &ChannelMapError{From: src.Layout.Name, To: dst.Name, Channel: c,
				Reason: "is absent from the source"}
		}
	}
	return nil, &ChannelMapError{From: src.Layout.Name, To: dst.Name, Channel: dst.Channels[0],
		Reason: "has no defined conversion"}
}

func sameChannels(a, b Layout) bool {
	if len(a.Channels) != len(b.Channels) {
		return false
	}
	for i := range a.Channels {
		if a.Channels[i] != b.Channels[i] {
			return false
		}
	}
	return true
}

func canPermute(src, dst Layout) bool {
	if len(src.Channels) != len(dst.Channels) {
		return false
	}
	for _, c := range dst.Channels {
		if !src.Has(c) {
			return false
		}
	}
	return true
}

func permute(src *Buffer, dst Layout) *Buffer {
	out, _ := NewBuffer(dst, src.Frames)
	out.Start = src.Start
	out.Metadata = src.Metadata
	index := make(map[Channel]int, len(src.Layout.Channels))
	for i, c := range src.Layout.Channels {
		index[c] = i
	}
	for f := 0; f < src.Frames; f++ {
		in := src.Frame(f)
		outFrame := out.Frame(f)
		for j, c := range dst.Channels {
			outFrame[j] = in[index[c]]
		}
	}
	return out
}

func downmixMono(src *Buffer, dst Layout) (*Buffer, error) {
	weights := make([]float64, len(src.Layout.Channels))
	srcIdx := make(map[Channel]int, len(src.Layout.Channels))
	for i, c := range src.Layout.Channels {
		srcIdx[c] = i
	}
	total := 0.0
	for _, w := range monoMix {
		if i, ok := srcIdx[w.Src]; ok {
			weights[i] = w.Weight
			total += w.Weight
		}
	}
	if total == 0 {
		return nil, &ChannelMapError{From: src.Layout.Name, To: dst.Name, Channel: ChFrontCenter,
			Reason: "has no source channel to draw from"}
	}
	out, _ := NewBuffer(dst, src.Frames)
	out.Start = src.Start
	out.Metadata = src.Metadata
	for f := 0; f < src.Frames; f++ {
		in := src.Frame(f)
		v := 0.0
		for i := range weights {
			v += in[i] * weights[i]
		}
		out.Frame(f)[0] = v
	}
	return out, nil
}

func downmixStereo(src *Buffer, dst Layout) (*Buffer, error) {
	srcIdx := make(map[Channel]int, len(src.Layout.Channels))
	for i, c := range src.Layout.Channels {
		srcIdx[c] = i
	}
	out, _ := NewBuffer(dst, src.Frames)
	out.Start = src.Start
	out.Metadata = src.Metadata
	for f := 0; f < src.Frames; f++ {
		in := src.Frame(f)
		outFrame := out.Frame(f)
		for j, m := range stereoMix {
			v := 0.0
			for _, wi := range m.Inputs {
				if i, ok := srcIdx[wi.Src]; ok {
					v += in[i] * wi.Weight
				}
			}
			outFrame[j] = v
		}
	}
	return out, nil
}
