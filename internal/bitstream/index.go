package bitstream

import (
	"encoding/json"
	"fmt"
	"sort"

	"mediacore/internal/fault"
)

// IndexVersion is the only keyframe index schema this build understands.
const IndexVersion = 1

// IndexEntry is one row of an external keyframe index.
type IndexEntry struct {
	Offset    int64 `json:"offset"`
	Size      int64 `json:"size"`
	Timestamp int64 `json:"timestamp"` // ticks at Index.Timescale
	Keyframe  bool  `json:"keyframe"`
}

// Index is the on-disk keyframe index. Timescale expresses all timestamps.
type Index struct {
	Version   int          `json:"version"`
	Codec     Codec        `json:"codec"`
	Timescale int64        `json:"timescale"`
	Entries   []IndexEntry `json:"entries"`
}

// IndexReport records index validation. Entries that fail are quarantined and
// never reach fingerprinting or archiving.
type IndexReport struct {
	Accepted    []IndexEntry  `json:"accepted"`
	Quarantined []Quarantined `json:"quarantined"`
}

// Quarantined pairs a rejected index row with the fault that rejected it.
type Quarantined struct {
	EntryIndex int          `json:"entry_index"`
	Entry      IndexEntry   `json:"entry"`
	Fault      *fault.Fault `json:"fault"`
}

// ParseIndex decodes and structurally validates an index document. A malformed
// or truncated document is a scanning fault carrying offset 0.
func ParseIndex(data []byte) (*Index, error) {
	if len(data) == 0 {
		return nil, fault.New(fault.CodeScanTruncatedHeader, 0, "", "keyframe index is empty")
	}
	var idx Index
	if err := json.Unmarshal(data, &idx); err != nil {
		return nil, fault.New(fault.CodeScanTruncatedHeader, 0, "", "keyframe index is not valid JSON: %v", err)
	}
	if idx.Version != IndexVersion {
		return nil, fault.Newf(fault.CodeScanIndexVersion, string(idx.Codec), "unsupported index version %d (want %d)", idx.Version, IndexVersion)
	}
	if idx.Timescale <= 0 {
		return nil, fault.Newf(fault.CodeScanIndexVersion, string(idx.Codec), "index timescale must be positive, got %d", idx.Timescale)
	}
	if len(idx.Entries) == 0 {
		return nil, fault.New(fault.CodeScanNoFrames, 0, string(idx.Codec), "keyframe index has no entries")
	}
	for i := range idx.Entries {
		e := &idx.Entries[i]
		if e.Offset < 0 || e.Size <= 0 {
			return nil, fault.New(fault.CodeScanIndexOutOfRange, e.Offset, string(idx.Codec), "index entry %d has offset %d and size %d", i, e.Offset, e.Size)
		}
	}
	return &idx, nil
}

// ValidateIndex checks every index entry against the scanned stream. It never
// fails wholesale: a bad row is quarantined with its first offending byte
// offset so the remaining rows can still be used. A codec or version mismatch
// is fatal because no row can be trusted.
func ValidateIndex(stream *Stream, idx *Index) (*IndexReport, error) {
	if idx.Codec != stream.Codec {
		return nil, fault.Newf(fault.CodeScanIndexCodec, string(stream.Codec), "index codec %q does not match stream codec %q", idx.Codec, stream.Codec)
	}
	report := &IndexReport{Accepted: make([]IndexEntry, 0, len(idx.Entries)), Quarantined: make([]Quarantined, 0)}
	var lastTS int64
	haveTS := false
	for i, e := range idx.Entries {
		var f *fault.Fault
		switch {
		case e.Offset < 0 || e.Size <= 0 || e.Offset+e.Size > int64(stream.Size):
			f = fault.New(fault.CodeScanIndexOutOfRange, max64(e.Offset, 0), string(stream.Codec),
				"index entry %d [%d,%d) lies outside a %d byte stream", i, e.Offset, e.Offset+e.Size, stream.Size)
		default:
			frame, ok := stream.FrameAtOffset(e.Offset)
			if !ok {
				f = fault.New(fault.CodeScanIndexNotBoundary, e.Offset, string(stream.Codec),
					"index entry %d offset %d is not a frame boundary", i, e.Offset)
			} else if frame.Size != e.Size {
				f = fault.New(fault.CodeScanIndexNotBoundary, e.Offset, string(stream.Codec),
					"index entry %d size %d does not match the %d byte frame at offset %d", i, e.Size, frame.Size, e.Offset)
			} else if e.Keyframe && !frame.Keyframe {
				f = fault.New(fault.CodeScanIndexNotKeyframe, e.Offset, string(stream.Codec),
					"index entry %d marks offset %d as a keyframe but it is not a random access frame", i, e.Offset)
			} else if haveTS && e.Timestamp < lastTS {
				f = fault.New(fault.CodeScanIndexTimeReorder, e.Offset, string(stream.Codec),
					"index entry %d timestamp %d precedes %d", i, e.Timestamp, lastTS)
			}
		}
		// Timestamp monotonicity is checked against the previously accepted
		// row, so an accepted row is always recorded after the boundary checks
		// pass.
		if f == nil && haveTS && e.Timestamp < lastTS {
			f = fault.New(fault.CodeScanIndexTimeReorder, e.Offset, string(stream.Codec),
				"index entry %d timestamp %d precedes %d", i, e.Timestamp, lastTS)
		}
		if f != nil {
			report.Quarantined = append(report.Quarantined, Quarantined{EntryIndex: i, Entry: e, Fault: f})
			continue
		}
		report.Accepted = append(report.Accepted, e)
		lastTS = e.Timestamp
		haveTS = true
	}
	return report, nil
}

// AcceptedKeyframeOffsets returns the offsets of accepted rows marked as
// keyframes, in index order.
func (r *IndexReport) AcceptedKeyframeOffsets() []int64 {
	out := make([]int64, 0, len(r.Accepted))
	for _, e := range r.Accepted {
		if e.Keyframe {
			out = append(out, e.Offset)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// QuarantinedCount reports how many rows were isolated.
func (r *IndexReport) QuarantinedCount() int { return len(r.Quarantined) }

// String renders the first quarantined fault for terminal output.
func (r *IndexReport) String() string {
	if len(r.Quarantined) == 0 {
		return "index clean"
	}
	q := r.Quarantined[0]
	return fmt.Sprintf("index quarantined %d row(s); first: %s", len(r.Quarantined), q.Fault.Error())
}

func max64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}
