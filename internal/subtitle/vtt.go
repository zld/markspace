package subtitle

// ParseVTT parses WebVTT bytes using the shared nanosecond timeline.
func ParseVTT(sample int, data []byte) (*Track, error) {
	return ParseVTTWithLimits(sample, data, DefaultLimits())
}

// ParseVTTWithLimits parses WebVTT with caller-supplied bounded limits.
func ParseVTTWithLimits(sample int, data []byte, limits Limits) (*Track, error) {
	return parseSubtitle(data, sample, FormatVTT, limits)
}
