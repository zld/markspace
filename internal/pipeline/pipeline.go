// Package pipeline wires the TS, clock, index, fanout and fanin subsystems
// into one streaming repair run. The core logic performs no terminal I/O and
// accepts an io.Reader, so it is reusable from the CLI, tests or a service.
package pipeline

import (
	"bytes"
	"io"

	"tsrepair/internal/fanin"
	"tsrepair/internal/fanout"
	"tsrepair/internal/fixture"
	"tsrepair/internal/index"
	"tsrepair/internal/mediaerr"
	"tsrepair/internal/mp4index"
	"tsrepair/internal/segment"
	"tsrepair/internal/ts"
)

// Config controls a repair run.
type Config struct {
	SamplesPerSegment int
	// BoundaryToleranceNanos defaults to the 1 ms fan-in rule.
	BoundaryToleranceNanos int64
	// QueryTargetsNanos are random-access targets used to measure hit rate.
	QueryTargetsNanos []int64
}

// DefaultConfig returns the standard configuration.
func DefaultConfig() Config {
	return Config{
		SamplesPerSegment:      8,
		BoundaryToleranceNanos: fanin.DefaultBoundaryToleranceNanos,
	}
}

// SegmentReport is the emitted, fully verified record for one segment.
type SegmentReport struct {
	Segment   segment.Segment
	SHA256    string
	Consistent bool
}

// Metrics aggregates the acceptance counters printed by the CLI.
type Metrics struct {
	Representations  int
	Segments         int
	IndexHits        int
	IndexMisses      int
	BoundaryMismatch int
	RoundTripOK      bool
	Deterministic    bool
}

// HitRate returns index hits over total queries.
func (m Metrics) HitRate() float64 {
	total := m.IndexHits + m.IndexMisses
	if total == 0 {
		return 1.0
	}
	return float64(m.IndexHits) / float64(total)
}

// Result is the complete output of a repair run.
type Result struct {
	Reports     []SegmentReport
	Errors      []*mediaerr.Error
	Metrics     Metrics
	// MP4 bytes per successful representation, keyed by representation id.
	MP4         map[int][]byte
	// Manifest used for serialization, retained for round-trip verification.
	Manifests   map[int]*mp4index.Manifest
}

// Run repairs one generated stream set. The fixture output is fed through the
// real TS reader, PSI tracker, PES demuxer and per-representation recovery.
func Run(reps []*fixture.Representation, cfg Config, ec *mediaerr.Collector) (*Result, error) {
	group := fanout.NewGroup()
	for _, rep := range reps {
		group.Add(rep.ID, fanout.DefaultConfig(rep.VideoPID))
	}

	// Per-representation local receiver tick line (lockstep synthetic clock).
	localTick := map[int]int64{}

	for _, rep := range reps {
		r := group.Get(rep.ID)
		reader := ts.NewReader(bytes.NewReader(rep.Data), ec)
		tracker := ts.NewPSITracker(ec)
		demux := ts.NewPESDemuxer(rep.VideoPID)

		for {
			pkt, err := reader.ReadPacket()
			if err != nil {
				if err == io.EOF {
					break
				}
				// Structural error: mark this representation failed but keep
				// processing the others.
				if me, ok := err.(*mediaerr.Error); ok {
					ec.Add(me)
				}
				break
			}
			tracker.HandlePacket(pkt)

			if pkt.HasPCR {
				localTick[rep.ID] += pkt.PCRRaw()
				r.ObservePCR(pkt.PCRRaw(), localTick[rep.ID], pkt.Offset)
			}
			if pes := demux.HandlePacket(pkt); pes != nil && pes.HasPTS {
				dts := pes.PTS
				if pes.HasDTS {
					dts = pes.DTS
				}
				r.AddSample(pes.PTS, dts, pes.Keyframe, pes.ByteOffset)
			}
		}
		if flush := demux.Flush(rep.VideoPID); flush != nil && flush.HasPTS {
			dts := flush.PTS
			if flush.HasDTS {
				dts = flush.DTS
			}
			r.AddSample(flush.PTS, dts, flush.Keyframe, flush.ByteOffset)
		}
	}

	return finalize(group, cfg, ec)
}

// finalize builds segments, runs fan-in, indexes, MP4 round-trip and hashing.
func finalize(group *fanout.Group, cfg Config, ec *mediaerr.Collector) (*Result, error) {
	res := &Result{
		MP4:       map[int][]byte{},
		Manifests: map[int]*mp4index.Manifest{},
	}

	// Independent per-representation segments.
	perRep := group.SegmentsFor(cfg.SamplesPerSegment)

	// Fan-in boundary cross-check across representations at each position.
	checker := fanin.NewChecker(cfg.BoundaryToleranceNanos)
	verdicts := checker.CheckAll(perRep)
	consistentByID := map[int]bool{}
	for _, v := range verdicts {
		consistentByID[v.SegmentID] = v.Consistent
	}

	// Collect reports in a deterministic representation then segment order.
	repIDs := sortedRepIDs(perRep)
	var totalSegments int
	for _, repID := range repIDs {
		segs := perRep[repID]
		r := group.Get(repID)
		idx := r.Index()

		// Measure hit rate over the configured query targets.
		for _, target := range cfg.QueryTargetsNanos {
			idx.Lookup(target)
		}
		st := idx.Stats()
		res.Metrics.IndexHits += st.Hits
		res.Metrics.IndexMisses += st.Misses

		manifest := buildManifest(repID, r, idx)
		res.Manifests[repID] = manifest
		for _, s := range segs {
			manifest.SegmentCanonical = append(manifest.SegmentCanonical, segment.Canonical(s))
		}
		wire, err := mp4index.Serialize(manifest)
		if err != nil {
			return nil, err
		}
		res.MP4[repID] = wire

		for _, s := range segs {
			totalSegments++
			res.Reports = append(res.Reports, SegmentReport{
				Segment:    *s,
				SHA256:     segment.Hash(s),
				Consistent: consistentByID[s.ID],
			})
		}
	}

	// Round-trip verification: every representation must re-parse identically.
	res.Metrics.RoundTripOK = true
	for repID, manifest := range res.Manifests {
		if err := mp4index.RoundTrip(manifest); err != nil {
			res.Metrics.RoundTripOK = false
			if me, ok := err.(*mediaerr.Error); ok {
				me.PID = uint16(repID)
				ec.Add(me)
			}
		}
	}

	res.Metrics.Representations = len(group.Successful())
	res.Metrics.Segments = totalSegments
	res.Metrics.BoundaryMismatch = checker.MismatchCount()

	// Merge per-representation errors (PCR/PTS/anchor events live in each
	// representation's own collector) into the run-level error list.
	for _, repID := range repIDs {
		for _, e := range group.Get(repID).Errors() {
			ec.Add(e)
		}
	}
	res.Errors = ec.All()
	return res, nil
}

func buildManifest(repID int, r *fanout.Representation, idx *index.Index) *mp4index.Manifest {
	samples := r.Samples()
	m := &mp4index.Manifest{
		Representation: repID,
		Timescale:      mp4index.Timescale,
	}
	var duration uint64
	keyframeSet := map[int]bool{}
	for _, e := range idx.Entries() {
		keyframeSet[e.SampleIndex] = true
	}
	for i, s := range samples {
		var delta uint32
		if i > 0 {
			delta = uint32(s.PTS90k - samples[i-1].PTS90k)
		}
		m.Samples = append(m.Samples, mp4index.SampleRecord{Delta90k: delta, Size: 0})
		if keyframeSet[i] {
			m.KeyframeNumbers = append(m.KeyframeNumbers, uint32(i+1)) // 1-based
		}
		duration += uint64(delta)
	}
	m.Duration90k = duration
	return m
}

func sortedRepIDs(m map[int][]*segment.Segment) []int {
	var ids []int
	for id := range m {
		ids = append(ids, id)
	}
	for i := 1; i < len(ids); i++ {
		for j := i; j > 0 && ids[j-1] > ids[j]; j-- {
			ids[j-1], ids[j] = ids[j], ids[j-1]
		}
	}
	return ids
}
