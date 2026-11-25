// Package pipeline wires every subsystem into one deterministic offline
// processing kernel: manifests parse into unified descriptors, representations
// fan out into independent RTP lanes, recovered segments fan into the shared
// clock with boundary validation, and the verifier produces round-trip and
// hash evidence. Resource conflicts follow the fixed precedence quality gate,
// then processing budget, then delivery size; every trade-off is recorded.
package pipeline

import (
	"sort"

	"mediasync/internal/model"
	"mediasync/internal/rtp"
	"mediasync/internal/segment"
	"mediasync/internal/sync"
	"mediasync/internal/verify"
)

// Config carries the command-line resource limits.
type Config struct {
	Seed     int64
	LossPct  int     // simulated packet loss percentage
	JitterMS int     // jitter buffer window expressed in arrival disorder
	BudgetMS int64   // processing budget in virtual milliseconds
	MaxBytes int64   // delivery size cap in bytes
}

// BudgetDecision records one resource trade-off with the priority rule that
// produced it, so acceptance output can explain every dropped representation.
type BudgetDecision struct {
	Representation string
	Priority       model.Priority
	Action         string // "kept" | "suspended-budget" | "suspended-size"
	Reason         string
	CostMS         int64
	SizeBytes      int64
}

// Counters aggregates the RTP and boundary statistics reported at the end.
type Counters struct {
	Recovered      int
	Lost           int
	Duplicate      int
	Late           int
	OutOfOrder     int
	Evicted        int
	BoundaryErrors int
}

// Report is the complete, deterministic processing result.
type Report struct {
	RoundTripRate    float64
	KeyframeHitRate  float64
	SegmentHashes    []verify.SegmentHash
	Counters         Counters
	BudgetDecisions  []BudgetDecision
	Errors           []*model.MediaError
	AlignedSegments  []model.SegmentDescriptor
	BudgetUsedMS     int64
	SizeUsedBytes    int64
}

// LaneInput is one representation's recovered descriptor stream plus the RTP
// evidence its lane produced.
type LaneInput struct {
	Representation string
	Kind           model.AssetKind
	Priority       model.Priority
	Segments       []model.SegmentDescriptor
	BufferStats    rtp.Counters
	Recovery       rtp.RecoveryStats
	Losses         []*model.MediaError
	MP4Tracks       []verify.TrackSpec
	MP4Fragments    []verify.FragmentSpec
	CorruptMP4      bool
	CorruptMP4Data  []byte // real truncated MP4 bytes; parsed when CorruptMP4 is set
}

// Run executes the full kernel over pre-built per-representation lane inputs.
// It never invokes randomness itself; the caller generates fixtures from the
// seed, guaranteeing byte-identical reports for identical arguments.
func Run(cfg Config, lanes []LaneInput) *Report {
	rep := &Report{}
	fanout := sync.NewFanout()
	clock := sync.NewClock()

	// Deterministic processing order: core first, then high, then low; ties
	// break by representation name so decisions never depend on map order.
	sorted := make([]LaneInput, len(lanes))
	copy(sorted, lanes)
	sort.SliceStable(sorted, func(i, j int) bool {
		if sorted[i].Priority != sorted[j].Priority {
			return sorted[i].Priority < sorted[j].Priority
		}
		return sorted[i].Representation < sorted[j].Representation
	})

	active := map[string]bool{}
	for _, lane := range sorted {
		cost := processingCost(lane)
		size := deliverySize(lane)
		decision := BudgetDecision{
			Representation: lane.Representation, Priority: lane.Priority,
			CostMS: cost, SizeBytes: size, Action: "kept",
		}

		// Precedence: quality gate first (corrupt core data is kept but
		// reported), then budget, then size. Core lanes are never suspended.
		if lane.Priority != model.PriorityCore && rep.BudgetUsedMS+cost > cfg.BudgetMS {
			decision.Action = "suspended-budget"
			decision.Reason = "processing budget exhausted before this lane"
		} else if lane.Priority != model.PriorityCore && rep.SizeUsedBytes+size > cfg.MaxBytes {
			decision.SizeBytes = size
			decision.Action = "suspended-size"
			decision.Reason = "delivery size cap would be exceeded"
		}

		if decision.Action == "kept" {
			rep.BudgetUsedMS += cost
			rep.SizeUsedBytes += size
			active[lane.Representation] = true
			fanout.AddLane(lane.Representation, lane.Kind, lane.Priority,
				rtp.NewJitterBuffer(lane.Representation, 32, 16),
				rtp.NewRecoverer(lane.Representation, 16, nil))
			for _, s := range lane.Segments {
				fanout.Dispatch(s)
			}
		} else {
			fanout.AddLane(lane.Representation, lane.Kind, lane.Priority,
				rtp.NewJitterBuffer(lane.Representation, 32, 16),
				rtp.NewRecoverer(lane.Representation, 16, nil))
			fanout.Suspend(lane.Representation)
		}
		rep.BudgetDecisions = append(rep.BudgetDecisions, decision)

		rep.Counters.Recovered += lane.Recovery.Recovered
		rep.Counters.Lost += lane.Recovery.Lost
		rep.Counters.Duplicate += lane.BufferStats.Duplicate
		rep.Counters.Late += lane.BufferStats.Late
		rep.Counters.OutOfOrder += lane.BufferStats.OutOfOrder
		rep.Counters.Evicted += lane.BufferStats.Evicted
		rep.Errors = append(rep.Errors, lane.Losses...)
	}

	// Fan in active lanes, then validate boundaries on the aligned timeline.
	rep.AlignedSegments = fanout.MergeInto(clock)
	for _, report := range segment.ValidateAll(rep.AlignedSegments) {
		rep.Counters.BoundaryErrors += len(report.Errors)
		rep.Errors = append(rep.Errors, report.Errors...)
	}

	// Verification: MP4 round trip per lane (corrupt lanes fail but never block
	// the others), then deterministic hashing of the aligned output.
	rep.runVerification(sorted, active)
	rep.SegmentHashes = verify.HashAll(rep.AlignedSegments)
	return rep
}

func (r *Report) runVerification(lanes []LaneInput, active map[string]bool) {
	var attempted, succeeded int
	var kfAttempts, kfHits int
	for _, lane := range lanes {
		if !active[lane.Representation] {
			continue
		}
		attempted++
		if lane.CorruptMP4 {
			// Parse the real truncated bytes so the failure is a located parser
			// error, not a synthetic flag. The error never stops sibling lanes.
			if _, perr := verify.ParseFragmentedMP4(lane.CorruptMP4Data); perr != nil {
				r.Errors = append(r.Errors, model.NewError(model.Classify(perr),
					"verify", model.Locate(perr),
					"rep %s corrupt input: %v", lane.Representation, perr))
			} else {
				r.Errors = append(r.Errors, model.NewError(model.KindMalformedMP4,
					"verify", 0, "rep %s corrupt fixture unexpectedly parsed", lane.Representation))
			}
			continue
		}
		if len(lane.MP4Tracks) == 0 {
			continue
		}
		res, err := verify.RoundTrip(lane.Representation, lane.MP4Tracks, lane.MP4Fragments)
		if err != nil {
			r.Errors = append(r.Errors, model.NewError(model.Classify(err),
				"verify", model.Locate(err), "rep %s: %v", lane.Representation, err))
			continue
		}
		if !res.OK {
			for _, reason := range res.Reasons {
				r.Errors = append(r.Errors, model.NewError(model.KindMalformedMP4,
					"verify", 0, "rep %s: %s", lane.Representation, reason))
			}
			continue
		}
		succeeded++
		// Keyframe hit rate: valid video keyframe indices that the parser
		// returned against the indices the writer declared.
		for _, tr := range lane.MP4Tracks {
			if tr.Kind != model.KindVideo || len(tr.KeyframeIndices) == 0 {
				continue
			}
			parsed := findTrack(res.Metadata, tr.TrackID)
			if parsed == nil {
				kfAttempts += len(tr.KeyframeIndices)
				continue
			}
			for _, want := range tr.KeyframeIndices {
				kfAttempts++
				if containsU32(parsed.KeyframeIndices, want) {
					kfHits++
				}
			}
		}
	}
	if attempted > 0 {
		r.RoundTripRate = float64(succeeded) / float64(attempted)
	}
	if kfAttempts > 0 {
		r.KeyframeHitRate = float64(kfHits) / float64(kfAttempts)
	} else {
		r.KeyframeHitRate = 1.0 // no video indices to miss is a perfect score
	}
}

func processingCost(lane LaneInput) int64 {
	// Deterministic virtual cost: per-segment work plus a fixed lane overhead.
	return int64(10+len(lane.Segments)*5) * int64(1+lane.Priority)
}

func deliverySize(lane LaneInput) int64 {
	var size int64
	for _, s := range lane.Segments {
		size += int64(len(s.Payload)) + 64 // fixed per-segment framing overhead
	}
	if size == 0 {
		size = 128 // every lane has minimal manifest overhead
	}
	return size
}

func findTrack(meta *verify.MP4Metadata, id uint32) *verify.TrackSpec {
	for i := range meta.Tracks {
		if meta.Tracks[i].TrackID == id {
			return &meta.Tracks[i]
		}
	}
	return nil
}

func containsU32(xs []uint32, want uint32) bool {
	for _, x := range xs {
		if x == want {
			return true
		}
	}
	return false
}
