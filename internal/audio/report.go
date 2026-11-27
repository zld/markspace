// Package audio: the normalization report and its terminal rendering.
package audio

import (
	"fmt"
	"math"

	"example.com/assetpack/internal/fault"
)

// Report is the verifiable account of a normalization run.
//
// Loudness is reported as a finite figure even for silence: the floor is
// LoudnessFloor and the corresponding Silent flag carries the truth.  This
// keeps the report serialisable in manifests, where -Inf is not valid JSON.
type Report struct {
	InRate          int     `json:"in_rate"`
	OutRate         int     `json:"out_rate"`
	InLayout        string  `json:"in_layout"`
	OutLayout       string  `json:"out_layout"`
	InFrames        int64   `json:"in_frames"`
	OutFrames       int64   `json:"out_frames"`
	InPeak          float64 `json:"in_peak"`
	OutPeak         float64 `json:"out_peak"`
	InLoudnessLUFS  float64 `json:"in_loudness_lufs"`
	OutLoudnessLUFS float64 `json:"out_loudness_lufs"`
	InSilent        bool    `json:"in_silent"`
	OutSilent       bool    `json:"out_silent"`
	GainDB          float64 `json:"gain_db"`
	GapSamples      int64   `json:"gap_samples"`
	Gaps            []Gap   `json:"gaps"`
	PeakLimited     bool    `json:"peak_limited"`
	Valid           bool    `json:"valid"`
}

// LoudnessFloor is the finite value reported for silent programmes.
const LoudnessFloor = -70.0

// finiteLoudness maps -Inf/NaN to the floor and flags silence.
func finiteLoudness(v float64) (float64, bool) {
	if math.IsInf(v, -1) || math.IsNaN(v) {
		return LoudnessFloor, true
	}
	return round3(v), false
}

// String renders the report's headline figures for terminal output.
func (r Report) String() string {
	lufs := fmt.Sprintf("%.1f", r.OutLoudnessLUFS)
	if r.OutSilent {
		lufs = "-inf"
	}
	return fmt.Sprintf("%d->%d Hz %s->%s frames %d->%d peak %.4f->%.4f lufs %s gain %+.1f dB gaps %d",
		r.InRate, r.OutRate, r.InLayout, r.OutLayout, r.InFrames, r.OutFrames,
		r.InPeak, r.OutPeak, lufs, r.GainDB, r.GapSamples)
}

// FromPCM16LE deinterleaves little-endian signed 16-bit PCM into the
// float representation used by this package.  Only complete frames are
// consumed; a partial trailing frame is reported, never silently dropped
// from a declared duration.
func FromPCM16LE(data []byte, rate int, layout Layout) (PCM, bool, error) {
	ch := layout.Count()
	if ch == 0 {
		return PCM{}, false, fault.New(fault.StageAudio, fault.KindInvalidArg,
			"unknown layout %q", string(layout))
	}
	if rate <= 0 {
		return PCM{}, false, fault.New(fault.StageAudio, fault.KindInvalidArg, "invalid rate %d", rate)
	}
	frameBytes := ch * 2
	frames := len(data) / frameBytes
	partial := len(data)%frameBytes != 0
	out := Silence(Format{Rate: rate, Layout: layout}, frames)
	for i := 0; i < frames; i++ {
		for c := 0; c < ch; c++ {
			off := (i*ch + c) * 2
			v := int16(uint16(data[off]) | uint16(data[off+1])<<8)
			out.Channels[c][i] = float64(v) / 32768
		}
	}
	return out, partial, nil
}
