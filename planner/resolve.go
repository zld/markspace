package planner

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"

	"continuity/media"
)

// resolveAudio builds the per-track conversion plan. A track whose
// layout or rate differs from the explicit target is converted only when
// a mapping rule exists; otherwise the track is refused. When no target
// is given, all inputs must already agree.
func (p *Plan) resolveAudio(inputs []*media.Container, c Constraints) error {
	type key struct {
		ch   uint16
		rate uint32
	}
	seen := map[key]bool{}
	for _, in := range inputs {
		for _, t := range in.Tracks {
			if t.IsVideo() {
				continue
			}
			seen[key{t.ChannelCount, t.SampleRate}] = true
			toCh, toRate := t.ChannelCount, t.SampleRate
			if c.TargetChannels != 0 {
				toCh = c.TargetChannels
			}
			if c.TargetSampleRate != 0 {
				toRate = c.TargetSampleRate
			}
			if toCh != t.ChannelCount && !audioRules[[2]uint16{t.ChannelCount, toCh}] {
				return &media.Error{Kind: media.KindParamConflict,
					Op: "planner.audio", TrackID: t.TrackID,
					Msg: fmt.Sprintf("no mapping rule for channel layout %d -> %d; refusing track",
						t.ChannelCount, toCh)}
			}
			p.Audio = append(p.Audio, AudioConversion{
				TrackID: t.TrackID, FromChannels: t.ChannelCount, ToChannels: toCh,
				FromRate: t.SampleRate, ToRate: toRate,
			})
		}
	}
	if len(seen) > 1 && (c.TargetChannels == 0 || c.TargetSampleRate == 0) {
		return &media.Error{Kind: media.KindParamConflict, Op: "planner.audio",
			Msg: "audio layouts/sample rates conflict and no explicit target was given"}
	}
	return nil
}

// resolveColor records every representation's color metadata as its
// source and requires an explicit output selection when they conflict.
func (p *Plan) resolveColor(inputs []*media.Container, c Constraints) error {
	distinct := map[media.ColorMeta]bool{}
	for _, in := range inputs {
		col := in.VideoTrack().Color
		p.ColorSources = append(p.ColorSources, col)
		distinct[col] = true
	}
	switch {
	case len(distinct) <= 1 && c.ColorOutput == nil:
		p.ColorOutput = p.ColorSources[0]
	case c.ColorOutput != nil:
		p.ColorOutput = *c.ColorOutput
	default:
		return &media.Error{Kind: media.KindParamConflict, Op: "planner.color",
			Msg: "color metadata conflicts across representations; explicit output selection required"}
	}
	return nil
}

// hash derives the plan identity from the plan parameters and the input
// hashes, so state recorded under one plan is never reused under another.
func (p *Plan) hash(inputs []*media.Container) string {
	payload := struct {
		SegmentNanos int64             `json:"segment_nanos"`
		Audio        []AudioConversion `json:"audio"`
		ColorOutput  media.ColorMeta   `json:"color_output"`
		Inputs       []string          `json:"inputs"`
	}{p.SegmentNanos, p.Audio, p.ColorOutput, nil}
	for _, in := range inputs {
		payload.Inputs = append(payload.Inputs, in.FileSHA256)
	}
	raw, _ := json.Marshal(payload)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
