package plan

import "regress/internal/scan"

// parsedDimensions extracts frame width/height from the parameter-set bytes
// captured on the first keyframe. Parsing full SPS RBSP is large; the trial
// encoder is authoritative for geometry, so this helper returns false unless
// a cheap, reliable signal is available. The ladder falls back to a fixed
// basis otherwise, which is sufficient because candidate dimensions are
// re-validated by the actual FFmpeg encode.
func parsedDimensions(_ *scan.ScanResult) (int, int, bool) {
	// Intentionally conservative: dimension truth comes from FFmpeg at encode
	// time (see worker). Returning false keeps the ladder basis deterministic
	// without duplicating SPS entropy-decoding logic in the planner.
	return 0, 0, false
}

// FrameCount is the total number of access units, i.e. encoded pictures.
func FrameCount(res *scan.ScanResult) int { return len(res.AccessUnits) }
