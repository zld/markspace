// Package subtitle parses SRT and WebVTT cue files onto the shared nanosecond
// timeline while preserving every source line and byte offset.
//
// Parsing is deliberately recovering: a structurally broken cue is reported as a
// stable model.Error and scanning resumes at the next plausible cue header, so
// the caller can still recover the surrounding content.
package subtitle

// ParseWithFormat dispatches to the parser implied by fmt.
func ParseWithFormat(data []byte, format Format) Result {
	if format == FormatVTT {
		return ParseWebVTT(data)
	}
	return ParseSRT(data)
}

// Parse auto-detects the format and parses.
func Parse(data []byte) Result {
	return ParseWithFormat(data, Detect(data))
}
