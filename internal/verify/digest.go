// Package verify turns a processing run into a deterministic, machine-checkable
// digest: block counts, the published track mapping, per-track and aggregate
// payload SHA-256 values, and the structured location of any fault. Two runs
// over the same input and parameters must render byte-identical reports.
package verify

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"hash"
	"sort"

	"mediapack/internal/demux"
	"mediapack/internal/fault"
	"mediapack/internal/mux"
	"mediapack/internal/track"
)

// TrackDigest summarizes one output track.
type TrackDigest struct {
	OutputTrack uint32
	CodecID     string
	Kind        string
	Blocks      int
	PayloadSHA  string // hex SHA-256 over the concatenation of that track's payloads
}

// Report is the full numeric acceptance record for one run.
type Report struct {
	BlockCount    int
	Mapping       []string // stable, pre-rendered mapping lines
	Tracks        []TrackDigest
	PayloadSHA256 string // hex SHA-256 over the ordered concatenation of all payload hashes
	// Fault, when non-nil, records where processing stopped.
	FaultKind   string
	FaultOffset int64
	FaultTrack  int
	Recoverable bool
}

// Accumulator folds packets in stream order. It is safe to feed a partial
// stream that ended in a fault: the accumulated state remains a faithful
// record of everything processed before the failure.
type Accumulator struct {
	mapping   *track.Mapping
	trackHash map[uint32]hash.Hash
	trackBlk  map[uint32]int
	root      hash.Hash
	count     int
}

// NewAccumulator binds an accumulator to the run's track mapping.
func NewAccumulator(mapping *track.Mapping) *Accumulator {
	return &Accumulator{
		mapping:   mapping,
		trackHash: make(map[uint32]hash.Hash),
		trackBlk:  make(map[uint32]int),
		root:      sha256.New(),
	}
}

// Add records one packet's payload and its per-block hash.
func (a *Accumulator) Add(p *demux.Packet) {
	h, ok := a.trackHash[p.OutputTrack]
	if !ok {
		h = sha256.New()
		a.trackHash[p.OutputTrack] = h
	}
	h.Write(p.Payload)
	blockHash := sha256.Sum256(p.Payload)
	a.root.Write(blockHash[:])
	a.trackBlk[p.OutputTrack]++
	a.count++
}

// AddResult folds a mux result (used when remuxing rather than streaming).
func (a *Accumulator) AddResult(res *mux.Result) {
	for _, d := range res.Digests {
		if _, ok := a.trackHash[d.OutputTrack]; !ok {
			a.trackHash[d.OutputTrack] = sha256.New()
		}
		a.trackHash[d.OutputTrack].Write(d.SHA256[:])
		a.trackBlk[d.OutputTrack]++
		a.root.Write(d.SHA256[:])
		a.count++
	}
}

// Finalize renders the deterministic report, optionally attaching a fault.
func (a *Accumulator) Finalize(runErr error) *Report {
	rep := &Report{
		BlockCount:  a.count,
		Mapping:     a.mapping.Render(),
		FaultOffset: -1,
	}
	rep.PayloadSHA256 = hex.EncodeToString(a.root.Sum(nil))

	indices := make([]uint32, 0, len(a.trackHash))
	for k := range a.trackHash {
		indices = append(indices, k)
	}
	sort.Slice(indices, func(i, j int) bool { return indices[i] < indices[j] })
	for _, idx := range indices {
		var codecID, kind string
		for _, t := range a.mapping.Tracks {
			if t.OutputIndex == idx {
				codecID = t.Codec.CodecID
				kind = t.Codec.Kind.String()
				break
			}
		}
		rep.Tracks = append(rep.Tracks, TrackDigest{
			OutputTrack: idx,
			CodecID:     codecID,
			Kind:        kind,
			Blocks:      a.trackBlk[idx],
			PayloadSHA:  hex.EncodeToString(a.trackHash[idx].Sum(nil)),
		})
	}
	if fe := fault.Of(runErr); fe != nil {
		rep.FaultKind = string(fe.Kind)
		rep.FaultOffset = fe.Offset
		rep.FaultTrack = fe.Track
		rep.Recoverable = fe.Recoverable
	}
	return rep
}

// Render produces stable, line-oriented terminal output. The ordering is
// fixed so identical runs produce byte-identical text.
func (r *Report) Render() string {
	out := fmt.Sprintf("blocks=%d\n", r.BlockCount)
	out += "track_mapping:\n"
	for _, line := range r.Mapping {
		out += "  " + line + "\n"
	}
	out += "track_digests:\n"
	for _, td := range r.Tracks {
		out += fmt.Sprintf("  out=%d codec=%s kind=%s blocks=%d sha256=%s\n",
			td.OutputTrack, td.CodecID, td.Kind, td.Blocks, td.PayloadSHA)
	}
	out += "payload_sha256=" + r.PayloadSHA256 + "\n"
	if r.FaultKind != "" {
		out += fmt.Sprintf("fault=%s offset=%d track=%d recoverable=%t\n",
			r.FaultKind, r.FaultOffset, r.FaultTrack, r.Recoverable)
	}
	return out
}

// Equal reports whether two reports carry identical numeric and digest fields.
func (r *Report) Equal(other *Report) bool {
	if r.BlockCount != other.BlockCount || r.PayloadSHA256 != other.PayloadSHA256 {
		return false
	}
	if len(r.Mapping) != len(other.Mapping) || len(r.Tracks) != len(other.Tracks) {
		return false
	}
	for i := range r.Mapping {
		if r.Mapping[i] != other.Mapping[i] {
			return false
		}
	}
	for i := range r.Tracks {
		if r.Tracks[i] != other.Tracks[i] {
			return false
		}
	}
	return true
}
