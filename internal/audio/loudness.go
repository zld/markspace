package audio

import (
	"encoding/json"
	"math"
	"strconv"
	"strings"
)

// LoudnessParams fixes the EBU R128 target used by every processing run.
// Fixed parameters are part of the cache key, so changing them invalidates
// every cached artifact.
type LoudnessParams struct {
	IntegratedLUFS float64 // target integrated loudness, -16 per spec
	TruePeakDBTP   float64 // maximum true peak
	LoudnessRange  float64 // maximum loudness range in LU
}

// DefaultLoudness returns the mandated -16 LUFS target.
func DefaultLoudness() LoudnessParams {
	return LoudnessParams{IntegratedLUFS: -16, TruePeakDBTP: -1.5, LoudnessRange: 11}
}

// flexFloat accepts the quoted-number strings loudnorm emits as well as bare
// JSON numbers, e.g. both "-20.12" and -20.12. loudnorm's -inf readings are
// preserved as negative infinity so callers can detect an unmeasurable input.
type flexFloat float64

func (f *flexFloat) UnmarshalJSON(b []byte) error {
	s := strings.Trim(string(b), `"`)
	switch s {
	case "":
		*f = 0
		return nil
	case "-inf":
		*f = flexFloat(math.Inf(-1))
		return nil
	case "inf":
		*f = flexFloat(math.Inf(1))
		return nil
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return err
	}
	*f = flexFloat(v)
	return nil
}

// Finite reports whether the reading is a real number (loudnorm returns -inf
// when a clip is too short/quiet to measure integrated loudness).
func (f flexFloat) Finite() bool { return !math.IsInf(float64(f), 0) && !math.IsNaN(float64(f)) }

// Metrics is the subset of loudnorm's JSON report consumed by pass two and
// recorded into the provenance document.
type Metrics struct {
	InputI        flexFloat `json:"input_i"`
	InputTP       flexFloat `json:"input_tp"`
	InputLRA      flexFloat `json:"input_lra"`
	InputThresh   flexFloat `json:"input_thresh"`
	TargetOffset  flexFloat `json:"target_offset"`
	OutputI       flexFloat `json:"output_i"`
	OutputTP      flexFloat `json:"output_tp"`
	OutputLRA     flexFloat `json:"output_lra"`
	OutputThresh  flexFloat `json:"output_thresh"`
	Normalization string    `json:"normalization_type"`
}

// passOneFilter builds the measurement-pass loudnorm filter.
func (p LoudnessParams) passOneFilter() string {
	return "loudnorm=I=" + fnum(p.IntegratedLUFS) +
		":TP=" + fnum(p.TruePeakDBTP) +
		":LRA=" + fnum(p.LoudnessRange) +
		":print_format=json"
}

// passTwoFilter builds the linear correction pass using measured metrics.
func (p LoudnessParams) passTwoFilter(m Metrics) string {
	return "loudnorm=I=" + fnum(p.IntegratedLUFS) +
		":TP=" + fnum(p.TruePeakDBTP) +
		":LRA=" + fnum(p.LoudnessRange) +
		":measured_I=" + fnum(float64(m.InputI)) +
		":measured_TP=" + fnum(float64(m.InputTP)) +
		":measured_LRA=" + fnum(float64(m.InputLRA)) +
		":measured_thresh=" + fnum(float64(m.InputThresh)) +
		":offset=" + fnum(float64(m.TargetOffset)) +
		":linear=true:print_format=json"
}

// dynamicFilter is the fallback when pass one cannot measure integrated
// loudness (returns -inf). It lets loudnorm converge without measured inputs.
func (p LoudnessParams) dynamicFilter() string {
	return "loudnorm=I=" + fnum(p.IntegratedLUFS) +
		":TP=" + fnum(p.TruePeakDBTP) +
		":LRA=" + fnum(p.LoudnessRange) +
		":linear=false:print_format=json"
}

// HasMeasuredInput reports whether pass one produced finite metrics suitable
// for a linear second pass.
func (m Metrics) HasMeasuredInput() bool {
	return m.InputI.Finite() && m.InputTP.Finite() && m.InputLRA.Finite() && m.InputThresh.Finite()
}

// ParseLoudnessJSON extracts the last JSON object loudnorm prints on stderr.
// ffmpeg may interleave banner lines, so the parser brackets the final block.
func ParseLoudnessJSON(stderr string) (Metrics, error) {
	start := strings.LastIndex(stderr, "{")
	end := strings.LastIndex(stderr, "}")
	if start < 0 || end <= start {
		return Metrics{}, ErrNoLoudnessReport
	}
	var m Metrics
	if err := json.Unmarshal([]byte(stderr[start:end+1]), &m); err != nil {
		return Metrics{}, err
	}
	return m, nil
}

// fnum formats a float the way ffmpeg filter arguments expect it (no trailing
// zeroes, dot decimal point).
func fnum(f float64) string {
	s := strconv.FormatFloat(f, 'f', 3, 64)
	return strings.TrimRight(strings.TrimRight(s, "0"), ".")
}
