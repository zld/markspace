// Package measure implements loudness and true-peak measurement.
package measure

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

// surroundWeight is the BS.1770 weight for surround channels (~+1.5 dB).
const surroundWeight = 1.41

// ChannelWeights returns BS.1770 channel weights for a channel count.
// Supported layouts: mono (1), stereo (2), 5.1 (6, LFE at index 3 with
// weight 0 — the LFE never feeds the loudness sum). Other channel
// counts are rejected as unknown layouts.
func ChannelWeights(channels int) ([]float64, error) {
	switch channels {
	case 1:
		return []float64{1}, nil
	case 2:
		return []float64{1, 1}, nil
	case 6:
		return []float64{1, 1, 1, 0, surroundWeight, surroundWeight}, nil
	}
	return nil, fmt.Errorf("unknown channel layout: %d channels (supported: mono, stereo, 5.1)", channels)
}

// ParseWeights parses a comma-separated per-channel weight override.
// Every weight must be a finite, non-negative number.
func ParseWeights(s string) ([]float64, error) {
	parts := strings.Split(s, ",")
	w := make([]float64, len(parts))
	for i, p := range parts {
		p = strings.TrimSpace(p)
		v, err := strconv.ParseFloat(p, 64)
		if err != nil || math.IsNaN(v) || math.IsInf(v, 0) || v < 0 {
			return nil, fmt.Errorf("invalid weight %q: must be a finite non-negative number", p)
		}
		w[i] = v
	}
	return w, nil
}
