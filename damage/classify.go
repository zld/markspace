// Package damage merges damaged byte/tick intervals and classifies
// their causes.
package damage

// Class categorises a damaged region.
type Class int

const (
	// ClassParseError is a malformed RTP/RTCP record.
	ClassParseError Class = iota
	// ClassInvalidLength is a zero-length or corrupt-length NAL unit.
	ClassInvalidLength
	// ClassNALTruncated is a NAL unit cut short by end of input.
	ClassNALTruncated
	// ClassSeqGap is an RTP sequence gap (transport loss).
	ClassSeqGap
	// ClassIndexIncomplete is a keyframe index missing entries.
	ClassIndexIncomplete
)

// String returns the stable lowercase class name used in reports.
func (c Class) String() string {
	switch c {
	case ClassParseError:
		return "parse_error"
	case ClassInvalidLength:
		return "invalid_length"
	case ClassNALTruncated:
		return "nal_truncated"
	case ClassSeqGap:
		return "seq_gap"
	case ClassIndexIncomplete:
		return "index_incomplete"
	default:
		return "unknown"
	}
}

// Recoverable reports whether a consumer can resume after the region.
func (c Class) Recoverable() bool {
	switch c {
	case ClassNALTruncated, ClassSeqGap, ClassIndexIncomplete:
		return true
	default:
		return false
	}
}
