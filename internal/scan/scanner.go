// Package scan reads one MP4 asset and its RTP packet stream and produces an
// index with absolutely-positioned samples, fragment boundaries and RTP
// delivery events.
//
// The scanner never repairs anything. A byte range it cannot parse ends the
// scan at that offset and is reported through the shared fault type; every
// record produced before the failure is kept so the caller can still show
// what was confirmed.
package scan

import (
	"example.com/assetcheck/internal/clock"
	"example.com/assetcheck/internal/fault"
	"example.com/assetcheck/internal/index"
)

// TrackInfo is the container-side track description discovered from moov.
type TrackInfo struct {
	ID        uint32
	Handler   [4]byte
	Timescale uint32
	Codec     string
}

// Kind maps the handler type onto the coarse track class.
func (t TrackInfo) Kind() string {
	switch t.Handler {
	case [4]byte{'s', 'o', 'u', 'n'}:
		return "audio"
	case [4]byte{'s', 'u', 'b', 't'}, [4]byte{'t', 'e', 'x', 't'}:
		return "subtitle"
	case [4]byte{'v', 'i', 'd', 'e'}:
		return "video"
	}
	return "other"
}

// Input is one scan request.
type Input struct {
	MP4 []byte
	RTP []byte
}

// Result is a scan outcome. Index is always non-nil and keeps whatever was
// confirmed before the first failure.
type Result struct {
	Index  *index.Index
	Faults []*fault.Error
}

// Ok reports whether the scan completed with no faults.
func (r *Result) Ok() bool { return len(r.Faults) == 0 && r.Index.Complete }

// Scanner walks the container in file order.
type Scanner struct {
	data   []byte
	idx    *index.Index
	faults []*fault.Error
	track  map[uint32]*TrackInfo
	order  []uint32
	// sample cursor per track while fragments are materialised in file order.
	dts    map[uint32]clock.Nanos
	trIdx  map[uint32]int
	failed bool
}

// Run scans in and returns a digest-bound index plus every located fault.
func Run(in Input) *Result {
	digest, inputs := index.ComputeDigest(in.MP4, in.RTP)
	s := &Scanner{
		data: in.MP4,
		idx: &index.Index{
			Version:   index.FormatVersion,
			Digest:    digest,
			Inputs:    inputs,
			Tracks:    []index.Track{},
			Fragments: []index.Fragment{},
			Packets:   []index.Packet{},
		},
		track: make(map[uint32]*TrackInfo),
		dts:   make(map[uint32]clock.Nanos),
		trIdx: make(map[uint32]int),
	}
	s.scanContainer()
	s.finalizeTracks()
	if len(s.faults) == 0 {
		s.scanPackets(in.RTP)
	}
	s.idx.Complete = len(s.faults) == 0 && len(s.idx.Tracks) > 0
	s.idx.Faults = s.faults
	fault.Sort(s.faults)
	return &Result{Index: s.idx, Faults: s.faults}
}

// Rebuild re-derives an index from the input bytes after a stale or
// incomplete index was rejected, and marks the result so the report can say
// a rebuild happened.
func Rebuild(in Input) *Result {
	res := Run(in)
	res.Index.Rebuilt = true
	return res
}

func (s *Scanner) fail(err *fault.Error) {
	s.faults = append(s.faults, err)
}

// --------------------------------------------------------------- packets --
