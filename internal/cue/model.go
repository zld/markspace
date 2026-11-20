// Package cue defines the shared subtitle cue and time model used by
// parsers, serializers, the timeline processor and the verifier.
package cue

import "fmt"

// Cue is a single subtitle entry. Times are milliseconds since 00:00:00.000.
type Cue struct {
	ID       string   // optional index (SRT) or cue identifier (WebVTT)
	StartMs  int64    // inclusive start of the half-open interval
	EndMs    int64    // exclusive end of the half-open interval
	Settings string   // WebVTT cue settings (empty for SRT)
	Lines    []string // payload text lines, order preserved
}

// Validate checks basic cue sanity.
func (c Cue) Validate() error {
	if c.StartMs < 0 {
		return fmt.Errorf("negative start time %dms", c.StartMs)
	}
	if c.EndMs <= c.StartMs {
		return fmt.Errorf("end %dms is not after start %dms", c.EndMs, c.StartMs)
	}
	if len(c.Lines) == 0 {
		return fmt.Errorf("cue has no text")
	}
	return nil
}

// SplitParts decomposes a non-negative millisecond timestamp.
func SplitParts(ms int64) (h, m, s, milli int64) {
	h = ms / 3600000
	ms %= 3600000
	m = ms / 60000
	ms %= 60000
	s = ms / 1000
	milli = ms % 1000
	return h, m, s, milli
}

// FromParts composes a millisecond timestamp, validating ranges.
func FromParts(h, m, s, milli int64) (int64, error) {
	if h < 0 || m < 0 || m > 59 || s < 0 || s > 59 || milli < 0 || milli > 999 {
		return 0, fmt.Errorf("invalid time parts %02d:%02d:%02d.%03d", h, m, s, milli)
	}
	return ((h*60+m)*60+s)*1000 + milli, nil
}

// FormatSRT renders ms as HH:MM:SS,mmm.
func FormatSRT(ms int64) string {
	h, m, s, milli := SplitParts(ms)
	return fmt.Sprintf("%02d:%02d:%02d,%03d", h, m, s, milli)
}

// FormatVTT renders ms as HH:MM:SS.mmm.
func FormatVTT(ms int64) string {
	h, m, s, milli := SplitParts(ms)
	return fmt.Sprintf("%02d:%02d:%02d.%03d", h, m, s, milli)
}
