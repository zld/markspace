package remux

import (
	"bytes"
	"fmt"

	"github.com/broadcast/mediad/internal/model"
	"github.com/broadcast/mediad/internal/probe"
)

// Verification is the read-back result of a remuxed container.
type Verification struct {
	MatchRate       float64                      `json:"roundtrip_match_rate"`
	KeyframeHitRate float64                      `json:"keyframe_index_hit_rate"`
	TracksChecked   int                          `json:"tracks_checked"`
	PerTrack        map[uint32]TrackVerifyDetail `json:"per_track"`
	OK              bool                         `json:"ok"`
}

// TrackVerifyDetail explains per-track read-back differences.
type TrackVerifyDetail struct {
	SourceSamples int  `json:"source_samples"`
	OutputSamples int  `json:"output_samples"`
	DurationsMatch bool `json:"durations_match"`
	KeyframesMatch bool `json:"keyframes_match"`
	FormatMatch    bool `json:"format_match"`
}

// Verify re-parses the remuxed bytes and compares them against the plan's
// source fingerprints. Isolated samples are expected absences, not misses.
func Verify(output []byte, g *model.Graph, p *Plan, cfg probe.ProbeConfig) (*Verification, error) {
	errs := &model.ErrorSet{}
	outGraph, _, err := probe.Probe(bytes.NewReader(output), int64(len(output)), cfg, errs)
	if err != nil {
		return nil, fmt.Errorf("re-parse remux output: %w", err)
	}
	v := &Verification{PerTrack: map[uint32]TrackVerifyDetail{}, TracksChecked: 0}
	matchedTracks := 0
	var keyHits, keyTotal int

	for _, id := range p.Order {
		tp := p.Tracks[id]
		src := g.Tracks[id]
		dst, ok := outGraph.Tracks[id]
		if !ok {
			v.PerTrack[id] = TrackVerifyDetail{SourceSamples: len(tp.Kept)}
			continue
		}
		v.TracksChecked++
		srcFp := keptFingerprint(src, tp.Kept)
		dstFp := Fingerprint(dst)
		durMatch := srcFp.EqualDurations(dstFp)
		kfMatch := srcFp.EqualKeyframes(dstFp)
		fmtMatch := formatMatch(tp, dst)
		detail := TrackVerifyDetail{
			SourceSamples: len(tp.Kept), OutputSamples: dstFp.SampleCount,
			DurationsMatch: durMatch, KeyframesMatch: kfMatch, FormatMatch: fmtMatch,
		}
		v.PerTrack[id] = detail
		if durMatch && kfMatch && fmtMatch {
			matchedTracks++
		}
		if tp.Kind == model.KindVideo {
			keyTotal += len(srcFp.Keyframes)
			if kfMatch {
				keyHits += len(srcFp.Keyframes)
			}
		}
	}
	v.MatchRate = safeRatio(matchedTracks, len(p.Order))
	if keyTotal > 0 {
		v.KeyframeHitRate = safeRatio(keyHits, keyTotal)
	} else {
		v.KeyframeHitRate = 1 // no video keyframes to score is a clean pass
	}
	v.OK = v.MatchRate == 1 && v.KeyframeHitRate == 1
	return v, nil
}

// keptFingerprint builds a source fingerprint restricted to kept samples so
// the comparison is against what was actually written.
func keptFingerprint(t *model.Track, kept []int) TrackFingerprint {
	sub := &model.Track{ID: t.ID, Kind: t.Kind, Timescale: t.Timescale}
	for _, idx := range kept {
		sub.Samples = append(sub.Samples, t.Samples[idx])
	}
	if len(sub.Samples) > 0 {
		sub.StartTime = sub.Samples[0].DecodeTime
		last := sub.Samples[len(sub.Samples)-1]
		sub.EndTime = last.DecodeTime.Add(last.Duration)
	}
	return Fingerprint(sub)
}

func formatMatch(tp *TrackPlan, dst *model.Track) bool {
	switch tp.Kind {
	case model.KindVideo:
		if tp.Video == nil || dst.Video == nil {
			return false
		}
		return tp.Video.Width == dst.Video.Width &&
			tp.Video.Height == dst.Video.Height &&
			tp.Video.Codec == dst.Video.Codec
	case model.KindAudio:
		if tp.Audio == nil || dst.Audio == nil {
			return false
		}
		return tp.Audio.ChannelCount == dst.Audio.ChannelCount &&
			tp.Audio.SampleRate == dst.Audio.SampleRate &&
			tp.Audio.Codec == dst.Audio.Codec
	default:
		return false
	}
}

func safeRatio(a, b int) float64 {
	if b == 0 {
		return 1
	}
	return float64(a) / float64(b)
}

// EqualDurations compares only sample count and per-sample durations.
func (f TrackFingerprint) EqualDurations(o TrackFingerprint) bool {
	if f.SampleCount != o.SampleCount || len(f.Durations) != len(o.Durations) {
		return false
	}
	for i := range f.Durations {
		if f.Durations[i].Cmp(o.Durations[i]) != 0 {
			return false
		}
	}
	return true
}

// EqualKeyframes compares relative keyframe positions.
func (f TrackFingerprint) EqualKeyframes(o TrackFingerprint) bool {
	if len(f.Keyframes) != len(o.Keyframes) {
		return false
	}
	for i := range f.Keyframes {
		if f.Keyframes[i] != o.Keyframes[i] {
			return false
		}
	}
	return true
}
