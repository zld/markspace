// Package timeline defines the shared media time base, the deterministic
// sample-event model and the unified error classification used by the
// parser, clock-correction and watermark subsystems.
package timeline

import "fmt"

// MediaTime is a point on a track's native media time line, expressed in
// that track's timescale units (e.g. 90000 for video, 48000 for audio).
// Use FromMillis / ToMillis to compare tracks that share a common clock.
type MediaTime struct {
	Timescale uint32
	Value     uint64
}

// FromMillis converts a millisecond offset into the given timescale.
func FromMillis(ms int64, timescale uint32) MediaTime {
	return MediaTime{Timescale: timescale, Value: uint64(ms) * uint64(timescale) / 1000}
}

// ToMillis converts the media time to whole milliseconds.
func (t MediaTime) ToMillis() int64 {
	if t.Timescale == 0 {
		return 0
	}
	return int64(t.Value*1000) / int64(t.Timescale)
}

// Add returns t + d on the same timescale.
func (t MediaTime) Add(d uint64) MediaTime {
	return MediaTime{Timescale: t.Timescale, Value: t.Value + d}
}

// Sub returns t - d; the boolean result is false when the subtraction
// would underflow.
func (t MediaTime) Sub(d uint64) (MediaTime, bool) {
	if d > t.Value {
		return MediaTime{Timescale: t.Timescale}, false
	}
	return MediaTime{Timescale: t.Timescale, Value: t.Value - d}, true
}

// ErrorClass is the machine-readable classification of every error the
// pipeline can emit. Class names are part of the stable output contract.
type ErrorClass string

const (
	// ClassBoxTruncated: a box header or payload runs past the end of the
	// input or past its declared parent size.
	ClassBoxTruncated ErrorClass = "box_truncated"
	// ClassBoxLength: a declared box length is zero, negative, exceeds the
	// configured hard limit or cannot be parsed.
	ClassBoxLength ErrorClass = "box_length"
	// ClassFieldMissing: a required box or full-box field is absent.
	ClassFieldMissing ErrorClass = "field_missing"
	// ClassTimestampBackward: a tfdt value moves backwards without a
	// plausible wraparound explanation, indicating a device restart.
	ClassTimestampBackward ErrorClass = "timestamp_backward"
	// ClassWraparound: a 32-bit timestamp wrapped around legally.
	ClassWraparound ErrorClass = "timestamp_wraparound"
	// ClassDuplicateSequence: a fragment sequence number was already seen.
	ClassDuplicateSequence ErrorClass = "duplicate_sequence"
	// ClassLateFragment: a fragment arrived after the watermark advanced
	// past its event time and triggered recomputation.
	ClassLateFragment ErrorClass = "late_fragment"
	// ClassMappingConflict: explicit track mapping rules cannot be applied.
	ClassMappingConflict ErrorClass = "mapping_conflict"
	// ClassOverflow: an arithmetic operation would overflow uint64.
	ClassOverflow ErrorClass = "overflow"
	// ClassUnsafePath: an output path escapes the configured root.
	ClassUnsafePath ErrorClass = "unsafe_path"
)

// MediaError is the single error type used across all subsystems. It always
// carries a stable classification, the byte offset or time at which the
// problem was observed and a human-readable detail string.
type MediaError struct {
	Class   ErrorClass
	Offset  int64 // byte offset, -1 when not applicable
	TrackID uint32
	Detail  string
}

// Error implements the error interface.
func (e *MediaError) Error() string {
	loc := "offset=-1"
	if e.Offset >= 0 {
		loc = fmt.Sprintf("offset=%d", e.Offset)
	}
	return fmt.Sprintf("%s: track=%d %s: %s", e.Class, e.TrackID, loc, e.Detail)
}

// NewError builds a MediaError with no specific location.
func NewError(class ErrorClass, detail string) *MediaError {
	return &MediaError{Class: class, Offset: -1, Detail: detail}
}

// ErrorAt builds a MediaError located at a byte offset.
func ErrorAt(class ErrorClass, offset int64, detail string) *MediaError {
	return &MediaError{Class: class, Offset: offset, Detail: detail}
}

// AsMediaError extracts a *MediaError from err, or returns nil.
func AsMediaError(err error) *MediaError {
	if err == nil {
		return nil
	}
	if me, ok := err.(*MediaError); ok {
		return me
	}
	return nil
}

// SampleEvent is the unit that flows from the parser through clock
// correction into the watermark engine. One event describes one run of
// compressed samples of a single track inside one fragment; sample bytes
// are never embedded here, only their sizes and content hash.
type SampleEvent struct {
	// TrackID is the MP4 track identifier from tfhd.
	TrackID uint32
	// MediaType is "video" or "audio".
	MediaType string
	// CodecKey identifies the codec configuration, e.g. "avc1.42E01E".
	CodecKey string
	// Timescale is the track timescale taken from mdhd.
	Timescale uint32
	// Seq is the mfhd sequence number; -1 when mfhd was missing.
	Seq int
	// InputOrder is the zero-based position at which the fragment bytes
	// were fed to the pipeline. It is the primary tie breaker and makes
	// ordering deterministic even with missing/duplicate sequence numbers.
	InputOrder int
	// DTS is the first decode time in track timescale units (tfdt).
	DTS uint64
	// SampleDurations are the per-sample durations; variable frame rate
	// boundaries are preserved because they are never averaged.
	SampleDurations []uint32
	// SampleSizes are the compressed payload sizes in bytes.
	SampleSizes []uint32
	// SampleHashes are SHA-256 hashes of the individual sample payloads.
	SampleHashes [][32]byte
	// PayloadHash covers the concatenation of all sample payload bytes.
	PayloadHash [32]byte
	// PayloadOffset is the mdat-relative offset of the first sample.
	PayloadOffset uint32
	// Repeated is set when Seq duplicates an earlier sequence number.
	Repeated bool
	// Late is set when the event was observed behind the watermark.
	Late bool
	// CorrectedDTS is the clock-corrected decode time in track units.
	CorrectedDTS uint64
	// CorrectionApplied is the signed correction in track units.
	CorrectionApplied int64
	// ClockConfidence describes the RTP/clock correction confidence.
	ClockConfidence Confidence
}

// Duration returns the total sample duration of the event in track units.
func (e *SampleEvent) Duration() uint64 {
	var sum uint64
	for _, d := range e.SampleDurations {
		sum += uint64(d)
	}
	return sum
}

// SampleCount returns the number of samples described by the event.
func (e *SampleEvent) SampleCount() int { return len(e.SampleSizes) }

// Confidence qualifies a clock-correction decision.
type Confidence string

const (
	ConfidenceNone   Confidence = "none"
	ConfidenceLow    Confidence = "low"
	ConfidenceMedium Confidence = "medium"
	ConfidenceHigh   Confidence = "high"
)
