package audio

import (
	"encoding/json"
	"strings"
)

// loudStats holds the measured loudnorm values used to build the second pass.
type loudStats struct {
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

// parseLoudnormJSON extracts the final JSON object emitted by loudnorm. ffmpeg
// prints it on stderr after other log lines, so we locate the last '{'.
func parseLoudnormJSON(stderr string) (loudStats, error) {
	idx := strings.LastIndex(stderr, "{")
	if idx < 0 {
		// no stats (e.g. trivial input): fall back to linear single-pass values
		return loudStats{}, nil
	}
	dec := json.NewDecoder(strings.NewReader(stderr[idx:]))
	var s loudStats
	if err := dec.Decode(&s); err != nil {
		return loudStats{}, err
	}
	return s, nil
}

// ApplyFilter renders the second-pass loudnorm with measured values. When no
// measurement is available it requests a simple linear normalization.
func (s loudStats) ApplyFilter() string {
	if s.InputI == "" {
		return "loudnorm=I=-16.0:TP=-1.0:LRA=11:linear=true"
	}
	return "loudnorm=I=-16.0:TP=-1.0:LRA=11:" +
		"measured_I=" + s.InputI + ":measured_TP=" + s.InputTP +
		":measured_LRA=" + s.InputLRA + ":measured_thresh=" + s.InputThresh +
		":offset=" + s.TargetOffset + ":linear=true:print_format=summary"
}
