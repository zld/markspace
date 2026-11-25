package media

import "fmt"

// Category classifies a segment defect with a stable, machine-readable token.
// New categories may be added but existing tokens must never be renumbered.
type Category string

const (
	// CatTruncatedHeader covers RIFF/WAVE prefixes and chunk headers that end
	// before their declared fixed-size fields.
	CatTruncatedHeader Category = "truncated_header"
	// CatChunkOutOfBounds covers chunks whose declared size reaches past the
	// end of the RIFF container or of the input buffer.
	CatChunkOutOfBounds Category = "chunk_out_of_bounds"
	// CatPartialFrame covers data payloads whose byte length is not a whole
	// number of interleaved sample frames.
	CatPartialFrame Category = "partial_frame"
	// CatBadContainer covers missing or mismatched RIFF/WAVE identifiers and
	// missing mandatory fmt/data chunks.
	CatBadContainer Category = "bad_container"
	// CatRTPReorder covers packets whose extended sequence number is lower
	// than the highest one already observed.
	CatRTPReorder Category = "rtp_reordered"
	// CatRTPSeqWrap marks a forward sequence-number wrap (informational).
	CatRTPSeqWrap Category = "rtp_sequence_wrap"
	// CatRTPTSWrap marks a forward timestamp wrap (informational).
	CatRTPTSWrap Category = "rtp_timestamp_wrap"
	// CatRTPClockUnavailable covers RTP clocks that cannot be mapped onto the
	// PCM sample axis.
	CatRTPClockUnavailable Category = "rtp_clock_unavailable"
	// CatRTPMalformed covers buffers that fail RTP header unmarshalling.
	CatRTPMalformed Category = "rtp_malformed"
	// CatIntervalGap covers an expected-sample discontinuity between segments.
	CatIntervalGap Category = "interval_gap"
	// CatIntervalOverlap covers two segments claiming the same sample range.
	CatIntervalOverlap Category = "interval_overlap"
	// CatWorkerFailed covers processing failures inside the worker pool.
	CatWorkerFailed Category = "worker_failed"
	// CatCanceled covers work stopped by context cancellation.
	CatCanceled Category = "canceled"
)

// fatalCategories are quality-gate failures: a segment carrying any of them
// can never be delivered, regardless of remaining budget or byte headroom.
var fatalCategories = map[Category]bool{
	CatTruncatedHeader:      true,
	CatChunkOutOfBounds:     true,
	CatPartialFrame:         true,
	CatBadContainer:         true,
	CatRTPReorder:           true,
	CatRTPClockUnavailable:  true,
	CatRTPMalformed:         true,
	CatIntervalGap:          true,
	CatIntervalOverlap:      true,
	CatWorkerFailed:         true,
}

// QualityFatal reports whether the category blocks delivery at the quality
// gate. Wrap markers and explicit cancellation are not quality defects.
func QualityFatal(c Category) bool { return fatalCategories[c] }

// Fault is one precisely located defect. Byte ranges are half-open [Start,End)
// and refer to the raw input buffer; sample ranges are on the unified axis.
type Fault struct {
	Category Category `json:"category"`
	Message  string   `json:"message"`
	// ByteStart/ByteEnd are set for structural WAV faults and -1 otherwise.
	ByteStart int64 `json:"byte_start"`
	ByteEnd   int64 `json:"byte_end"`
	// SampleStart/SampleEnd are set when the fault maps onto the time axis
	// and -1 otherwise.
	SampleStart int64 `json:"sample_start"`
	SampleEnd   int64 `json:"sample_end"`
}

// NewByteFault builds a fault anchored to a half-open raw-byte interval.
func NewByteFault(c Category, start, end int64, format string, args ...any) Fault {
	return Fault{
		Category:    c,
		Message:     fmt.Sprintf(format, args...),
		ByteStart:   start,
		ByteEnd:     end,
		SampleStart: -1,
		SampleEnd:   -1,
	}
}

// NewSampleFault builds a fault anchored to a half-open sample interval.
func NewSampleFault(c Category, start, end int64, format string, args ...any) Fault {
	return Fault{
		Category:    c,
		Message:     fmt.Sprintf(format, args...),
		ByteStart:   -1,
		ByteEnd:     -1,
		SampleStart: start,
		SampleEnd:   end,
	}
}

// NewFault builds a fault without any spatial anchor.
func NewFault(c Category, format string, args ...any) Fault {
	return Fault{
		Category:    c,
		Message:     fmt.Sprintf(format, args...),
		ByteStart:   -1,
		ByteEnd:     -1,
		SampleStart: -1,
		SampleEnd:   -1,
	}
}

// HasFatal reports whether any fault in the list fails the quality gate.
func HasFatal(faults []Fault) bool {
	for _, f := range faults {
		if QualityFatal(f.Category) {
			return true
		}
	}
	return false
}

// FatalCategories returns the quality-gate failures in faults in the order
// they appear.
func FatalCategories(faults []Fault) []Fault {
	out := make([]Fault, 0, len(faults))
	for _, f := range faults {
		if QualityFatal(f.Category) {
			out = append(out, f)
		}
	}
	return out
}
