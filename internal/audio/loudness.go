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
	InputI         string `json:"input_i"`
	InputTP        string `json:"input_tp"`
	InputLRA       string `json:"input_lra"`
	InputThresh    string `json:"input_thresh"`
	OutputI        string `json:"output_i"`
	OutputTP       string `json:"output_tp"`
	OutputLRA      string `json:"output_lra"`
	OutputThresh   string `json:"output_thresh"`
	NormType       string `json:"normalization_type"`
	TargetOffset   string `json:"target_offset"`
}

// ParseLoudnessJSON extracts the first-pass measured source stats. The
// output_* fields describe what linear normalization would achieve and are
// not needed for the second pass parameters.
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
	i, err := strconv.ParseFloat(j.InputI, 64)
	if err != nil {
		return LoudnessStats{}, fmt.Errorf("loudnorm input_i: %w", err)
	}
	tp, _ := strconv.ParseFloat(j.InputTP, 64)
	lra, _ := strconv.ParseFloat(j.InputLRA, 64)
	thr, _ := strconv.ParseFloat(j.InputThresh, 64)
	return LoudnessStats{Integrated: i, TruePeak: tp, Range: lra, Threshold: thr}, nil
}

var (
	reIntegrated = regexp.MustCompile(`I:\s*(-?[0-9.]+)\s*LUFS`)
	reTruePeak   = regexp.MustCompile(`TPK:\s*(-?[0-9.]+)\s*dB(FS|TP)`)
)

// ParseEBUR128 reads the final per-frame ebur128 line. The last frame holds
// the integrated measurement over the whole program.
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
