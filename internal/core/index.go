package core

import (
	"fmt"
	"sort"
)

// Sample is one media sample with both its raw source tick and the canonical
// nanosecond position derived by the epoch tracker. ByteOffset is 0-based and
// relative to the segment file.
type Sample struct {
	Track      Track
	Segment    string
	ByteOffset int64
	Size       uint32
	RawTicks   int64
	Timescale  uint32
	Canonical  Nanos
	Duration   Nanos
	Epoch      int
	Keyframe   bool
	DTSTicks   int64
	CTSTicks   int64
}

// Segment is one media fragment and everything the index learnt about it.
type Segment struct {
	// URI is the path relative to the media root.
	URI string
	// Representation is the DASH representation ID ("" for HLS/single-rate).
	Representation string
	Track          Track
	// Epoch is the epoch number assigned to the segment's first sample.
	Epoch int
	// Start and End are the canonical half-open bounds of the samples.
	Start, End Nanos
	// Keyframes lists sample indices (into Samples) flagged as sync.
	Keyframes []int
	// Samples in file order.
	Samples []Sample
	// SHA256 is the lowercase hex digest of the whole file.
	SHA256 string
	// Size is the file size in bytes.
	Size int64
	// Truncated reports that the declared box structure ran past EOF.
	Truncated bool
}

// Index is the immutable result of a scan, in manifest order.
type Index struct {
	Segments []*Segment
	// Epochs are the canonical timeline boundaries assigned during scanning.
	Epochs []Epoch
	byURI  map[string]*Segment
}

// NewIndex builds an index from an ordered segment list.
func NewIndex(segs []*Segment) *Index {
	idx := &Index{Segments: segs, byURI: make(map[string]*Segment, len(segs))}
	for _, s := range segs {
		idx.byURI[s.URI] = s
	}
	return idx
}

// Lookup returns the segment for a root-relative URI.
func (i *Index) Lookup(uri string) (*Segment, bool) {
	s, ok := i.byURI[uri]
	return s, ok
}

// Keyframes returns every keyframe sample in [from,to), in canonical order.
func (i *Index) Keyframes(from, to Nanos) []Sample {
	var out []Sample
	for _, seg := range i.Segments {
		for _, k := range seg.Keyframes {
			s := seg.Samples[k]
			if s.Canonical >= from && s.Canonical < to {
				out = append(out, s)
			}
		}
	}
	return out
}

// SegmentsIn returns, in manifest order, the segments overlapping [from,to).
func (i *Index) SegmentsIn(from, to Nanos) []*Segment {
	var out []*Segment
	for _, seg := range i.Segments {
		if seg.End > from && seg.Start < to {
			out = append(out, seg)
		}
	}
	return out
}

// Validate checks the invariants the streaming runner relies on: segments are
// in manifest order and canonical time never regresses.
func (i *Index) Validate() error {
	var last Nanos
	for n, s := range i.Segments {
		if s.URI == "" {
			return fmt.Errorf("segment %d has empty URI", n)
		}
		if s.End < s.Start {
			return fmt.Errorf("segment %s: end %s precedes start %s", s.URI, s.End, s.Start)
		}
		if n > 0 && s.Start < last {
			return fmt.Errorf("segment %s: start %s precedes previous end %s", s.URI, s.Start, last)
		}
		last = s.End
	}
	return nil
}

// SortedURIs returns segment URIs sorted lexicographically, for listings that
// must not depend on manifest order.
func (i *Index) SortedURIs() []string {
	out := make([]string, 0, len(i.Segments))
	for _, s := range i.Segments {
		out = append(out, s.URI)
	}
	sort.Strings(out)
	return out
}

// TrackTimescale returns the timescale of the segment's first sample, or zero
// when the segment has none.
func (s *Segment) TrackTimescale() uint32 {
	if len(s.Samples) == 0 {
		return 0
	}
	return s.Samples[0].Timescale
}
