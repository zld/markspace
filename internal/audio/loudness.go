package audio

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// LoudnessStats is the EBU R128 measurement set used by the quality gate.
type LoudnessStats struct {
	Integrated float64 // LUFS
	TruePeak   float64 // dBTP (reported as dBFS on PCM, equivalent post-encode)
	Range      float64 // LU
	Threshold  float64 // LUFS
}

// PassesGate reports whether a finished candidate meets the delivery
// specification: integrated loudness inside -16 ±1 LUFS and true peak no
// higher than -1 dBTP.
func (s LoudnessStats) PassesGate() bool {
	loudOK := s.Integrated >= TargetLoudness-LoudnessTol &&
		s.Integrated <= TargetLoudness+LoudnessTol
	peakOK := s.TruePeak <= MaxTruePeakDBTP
	return loudOK && peakOK
}

// loudnormJSON mirrors the print_format=json object FFmpeg emits.
type loudnormJSON struct {
	InputI       string `json:"input_i"`
	InputTP      string `json:"input_tp"`
	InputLRA     string `json:"input_lra"`
	InputThresh  string `json:"input_thresh"`
	OutputI      string `json:"output_i"`
	OutputTP     string `json:"output_tp"`
	OutputLRA    string `json:"output_lra"`
	OutputThresh string `json:"output_thresh"`
	NormType     string `json:"normalization_type"`
	TargetOffset string `json:"target_offset"`
}

// ParseLoudnessJSON extracts the first-pass measured source stats.
// Non-finite values (-inf on silence or programs shorter than the EBU
// window) are clamped to the filter's legal range so the second pass stays
// runnable instead of failing on an out-of-range parameter.
func ParseLoudnessJSON(stderr []byte) (LoudnessStats, error) {
	start := bytesIndex(stderr, []byte("{"))
	end := bytesLastIndex(stderr, []byte("}"))
	if start < 0 || end <= start {
		return LoudnessStats{}, errors.New("loudnorm JSON block not found in ffmpeg output")
	}
	var j loudnormJSON
	if err := json.Unmarshal(stderr[start:end+1], &j); err != nil {
		return LoudnessStats{}, fmt.Errorf("loudnorm JSON parse: %w", err)
	}
	return LoudnessStats{
		Integrated: clampMeasured(parseLooseFloat(j.InputI), -99, 0),
		TruePeak:   clampMeasured(parseLooseFloat(j.InputTP), -99, 99),
		Range:      clampMeasured(parseLooseFloat(j.InputLRA), 0, 99),
		Threshold:  clampMeasured(parseLooseFloat(j.InputThresh), -99, 0),
	}, nil
}

// parseLooseFloat tolerates -inf / non-numeric measurements by returning 0.
func parseLooseFloat(s string) float64 {
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0
	}
	return v
}

func clampMeasured(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

var (
	reIntegrated  = regexp.MustCompile(`I:\s*(-?[0-9.]+)\s*LUFS`)
	reTruePeak    = regexp.MustCompile(`TPK:\s*(-?[0-9.]+)(?:\s+-?[0-9.]+)?\s*dB(FS|TP)`)
	reSummaryI    = regexp.MustCompile(`(?m)^\s*I:\s*(-?[0-9.]+)\s*LUFS\s*$`)
	reSummaryPeak = regexp.MustCompile(`(?m)^\s*Peak:\s*(-?[0-9.]+)\s*dB(FS|TP)\s*$`)
)

// ParseEBUR128 reads the final per-frame ebur128 line, falling back to the
// human-readable summary block. The last frame holds the integrated
// measurement over the whole program.
func ParseEBUR128(stderr []byte) (LoudnessStats, error) {
	lines := strings.Split(string(stderr), "\n")
	var stats LoudnessStats
	found := false
	for _, line := range lines {
		if !strings.Contains(line, "ebur128") {
			continue
		}
		if m := reIntegrated.FindStringSubmatch(line); m != nil {
			stats.Integrated, _ = strconv.ParseFloat(m[1], 64)
		}
		if m := reTruePeak.FindStringSubmatch(line); m != nil {
			stats.TruePeak, _ = strconv.ParseFloat(m[1], 64)
			found = true
		}
	}
	// Summary fallback (some FFmpeg versions only emit final values there).
	if stats.Integrated == 0 {
		if m := reSummaryI.FindStringSubmatch(string(stderr)); m != nil {
			stats.Integrated, _ = strconv.ParseFloat(m[1], 64)
		}
	}
	if !found {
		if m := reSummaryPeak.FindStringSubmatch(string(stderr)); m != nil {
			stats.TruePeak, _ = strconv.ParseFloat(m[1], 64)
			found = true
		}
	}
	if !found {
		return LoudnessStats{}, errors.New("ebur128 true-peak measurement not found")
	}
	return stats, nil
}

func bytesIndex(b, sep []byte) int {
	return strings.Index(string(b), string(sep))
}

func bytesLastIndex(b, sep []byte) int {
	return strings.LastIndex(string(b), string(sep))
}
