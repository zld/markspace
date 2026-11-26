package core

import "sort"

// Category classifies an anomaly record independently of the fault Kind that
// produced it. Reports order and count by category, so the set must stay
// small and stable.
type Category string

const (
	// CatTimeJump covers representation switches, discontinuities, wraps and
	// backwards steps.
	CatTimeJump Category = "time-jump"
	// CatTruncated: the segment ended before the boxes it declared.
	CatTruncated Category = "segment-truncated"
	// CatBoxParse: an fMP4 box could not be parsed.
	CatBoxParse Category = "box-parse"
	// CatHashMismatch: the recomputed SHA-256 differs from the index.
	CatHashMismatch Category = "hash-mismatch"
	// CatNoKeyframe: recovery could not find a verified keyframe.
	CatNoKeyframe Category = "no-keyframe"
)

// Anomaly is the traceable record every stage emits. It always names the
// segment, the track and the original byte offset and raw timestamp that were
// observed, never a rewritten or estimated value.
type Anomaly struct {
	Category Category
	// Reason is the specific trigger, e.g. "representation-switch",
	// "hls-discontinuity", "timestamp-wrap", "timestamp-backwards".
	Reason string
	// Pos is the manifest/file position the anomaly points at.
	Pos   Pos
	Track Track
	// Segment is the root-relative segment URI.
	Segment string
	// ByteOffset is the 0-based offset inside the segment; -1 when unknown.
	ByteOffset int64
	// RawTimestamp is the original source tick value at the anomaly.
	RawTimestamp int64
	// Timescale is the timescale the raw value is expressed in.
	Timescale uint32
	// Canonical is the canonical nanosecond position at the anomaly.
	Canonical Nanos
	// BoundaryOffset is the epoch delta change introduced by a time jump.
	BoundaryOffset Nanos
	// Expected and Actual are set for hash mismatches.
	Expected string
	Actual   string
	Detail   string
}

// Less orders anomalies deterministically: canonical time, then segment URI,
// then byte offset, then category and reason.
func (a Anomaly) Less(b Anomaly) bool {
	if a.Canonical != b.Canonical {
		return a.Canonical < b.Canonical
	}
	if a.Segment != b.Segment {
		return a.Segment < b.Segment
	}
	if a.ByteOffset != b.ByteOffset {
		return a.ByteOffset < b.ByteOffset
	}
	if a.Category != b.Category {
		return a.Category < b.Category
	}
	return a.Reason < b.Reason
}

// SortAnomalies orders a slice in place using Less.
func SortAnomalies(as []Anomaly) {
	sort.SliceStable(as, func(i, j int) bool { return as[i].Less(as[j]) })
}

// Recovery records a local recovery after a failed segment. Offsets are the
// original values recorded in the index; nothing is rewritten.
type Recovery struct {
	// FailedSegment is the segment whose verification failed.
	FailedSegment string
	// FailedByteOffset is where damage was detected inside it.
	FailedByteOffset int64
	// UnrecoveredStart and UnrecoveredEnd bound the canonical interval that
	// could not be reconstructed.
	UnrecoveredStart Nanos
	UnrecoveredEnd   Nanos
	// ResumeSegment is the next segment that verified.
	ResumeSegment string
	// ResumeByteOffset is the original byte offset of the resume keyframe.
	ResumeByteOffset int64
	// ResumeCanonical is the canonical position of the resume keyframe.
	ResumeCanonical Nanos
	// Resumed reports that a verified keyframe was found.
	Resumed bool
}
