// Package damage converts sequence, codec, timestamp and index failures into
// locatable damage regions on shared time and byte coordinates.
package damage

import (
	"fmt"

	"example.com/media-kernel/codec"
	"example.com/media-kernel/index"
	"example.com/media-kernel/ingest"
	"example.com/media-kernel/media"
)

// Kind is a stable damage classification.
type Kind string

const (
	KindMissingPacket   Kind = "missing_packet"
	KindMalformedPacket Kind = "malformed_packet"
	KindTimestamp       Kind = "timestamp_anomaly"
	KindMissingIndex    Kind = "missing_index"
	KindCorruptKeyframe Kind = "corrupt_keyframe"
)

// Region is one auditable damaged interval. TimeRange may be nil when only a
// sequence/byte location is knowable; callers must not invent a media duration.
type Region struct {
	ID        string           `json:"id"`
	Kind      Kind             `json:"kind"`
	Code      media.Code       `json:"code"`
	Message   string           `json:"message"`
	FirstSeq  *uint64          `json:"first_seq,omitempty"`
	LastSeq   *uint64          `json:"last_seq,omitempty"`
	TimeRange *media.TimeRange `json:"time_range,omitempty"`
	ByteRange *media.ByteRange `json:"byte_range,omitempty"`
	Confirmed bool             `json:"confirmed"`
}

// Report aggregates damage regions and summary counts.
type Report struct {
	Regions        []Region `json:"regions"`
	MissingPackets int      `json:"missing_packets"`
	ErrorCount     int      `json:"error_count"`
}

// Scanner assigns stable IDs and accumulates regions.
type Scanner struct {
	regions []Region
	missing int
}

// NewScanner creates an empty damage scanner.
func NewScanner() *Scanner { return &Scanner{} }

// Regions returns accumulated regions.
func (s *Scanner) Regions() []Region { return append([]Region(nil), s.regions...) }

// Report returns a copy of the accumulated report.
func (s *Scanner) Report() Report {
	return Report{Regions: s.Regions(), MissingPackets: s.missing, ErrorCount: len(s.regions)}
}

// Add appends a region with a stable sequential ID.
func (s *Scanner) Add(r Region) Region {
	if r.ID == "" {
		r.ID = fmt.Sprintf("D%04d", len(s.regions)+1)
	}
	s.regions = append(s.regions, r)
	if r.Kind == KindMissingPacket && r.FirstSeq != nil && r.LastSeq != nil {
		s.missing += int(*r.LastSeq - *r.FirstSeq + 1)
	}
	return r
}

// ScanAudio converts codec parse errors into regions.
func (s *Scanner) ScanAudio(packets []codec.Analyzed) []Region {
	var added []Region
	for _, p := range packets {
		if p.Error == nil {
			continue
		}
		r := Region{
			Kind:      KindMalformedPacket,
			Code:      p.Error.Code,
			Message:   p.Error.Message,
			FirstSeq:  &p.ExtSequence,
			LastSeq:   &p.ExtSequence,
			TimeRange: p.Error.TimeRange,
			ByteRange: p.Error.ByteRange,
			Confirmed: true,
		}
		added = append(added, s.Add(r))
	}
	return added
}

// ScanTimeline rejects overlapping/non-monotonic successful packet ranges.
// Gaps are allowed because they represent missing packets, not timestamp faults.
func (s *Scanner) ScanTimeline(packets []codec.Analyzed) []Region {
	var added []Region
	var lastEnd media.Timestamp
	have := false
	for _, p := range packets {
		if p.Range == nil || p.Error != nil {
			continue
		}
		if have {
			if err := media.CheckAdjacent(lastEnd, p.Range.Start); err != nil {
				e, _ := media.AsError(err)
				r := Region{
					Kind:      KindTimestamp,
					Code:      e.Code,
					Message:   e.Message,
					FirstSeq:  &p.ExtSequence,
					LastSeq:   &p.ExtSequence,
					TimeRange: e.TimeRange,
					Confirmed: true,
				}
				added = append(added, s.Add(r))
			}
		}
		lastEnd = p.Range.End
		have = true
	}
	return added
}

// ScanGap records one sequence gap. hole is the confirmable time interval from
// the previous known end to the next known start; it may be nil.
func (s *Scanner) ScanGap(gap ingest.Gap, hole *media.TimeRange) Region {
	first, last := gap.First, gap.Last
	return s.Add(Region{
		Kind:      KindMissingPacket,
		Code:      media.CodeSequenceGap,
		Message:   gap.String(),
		FirstSeq:  &first,
		LastSeq:   &last,
		TimeRange: hole,
		Confirmed: true,
	})
}

// ScanGaps records gaps without inventing time intervals.
func (s *Scanner) ScanGaps(gaps []ingest.Gap) {
	for _, g := range gaps {
		s.ScanGap(g, nil)
	}
}

// ScanIndex converts missing-index and keyframe verification errors to regions.
func (s *Scanner) ScanIndex(idx *index.Index) []Region {
	if idx == nil {
		return nil
	}
	var added []Region
	if idx.MissingIndex {
		added = append(added, s.Add(Region{
			Kind:      KindMissingIndex,
			Code:      media.CodeIndexMissing,
			Message:   "sync sample table is missing",
			Confirmed: true,
		}))
	}
	for _, kf := range idx.Keyframes {
		if kf.Error == nil {
			continue
		}
		e := kf.Error
		kind := KindCorruptKeyframe
		if e.Code == media.CodeIndexMismatch {
			kind = KindCorruptKeyframe
		}
		r := Region{
			Kind:      kind,
			Code:      e.Code,
			Message:   e.Message,
			ByteRange: e.ByteRange,
			TimeRange: e.TimeRange,
			Confirmed: true,
		}
		added = append(added, s.Add(r))
	}
	return added
}
