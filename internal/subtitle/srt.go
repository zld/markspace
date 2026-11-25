package subtitle

// ParseSRT parses SubRip bytes using the shared nanosecond timeline.
func ParseSRT(sample int, data []byte) (*Track, error) {
	return ParseSRTWithLimits(sample, data, DefaultLimits())
}

// ParseSRTWithLimits parses SRT with caller-supplied bounded limits.
func ParseSRTWithLimits(sample int, data []byte, limits Limits) (*Track, error) {
	return parseSubtitle(data, sample, FormatSRT, limits)
}
