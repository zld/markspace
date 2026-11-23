package pipeline

import (
	"bytes"
	"errors"
	"fmt"
	"sort"

	"medfix/internal/decode"
	"medfix/internal/index"
	"medfix/internal/journal"
	"medfix/internal/timeline"
)

// SegmentSize groups this many events into one output segment.
const SegmentSize = 8

// sink collects streamed decoder output one event at a time; payloads are
// handed straight to the aligner and never aggregated here. Event sequence
// ordinals are re-assigned globally so events from different sources never
// collide (each parser numbers its own events from one).
type sink struct {
	aligner *timeline.Aligner
	losses  []timeline.Error
	nextSeq int
}

func (s *sink) OnTrack(t timeline.Track) { s.aligner.AddTrack(t) }

func (s *sink) OnEvent(e timeline.Event) {
	s.nextSeq++
	e.Seq = s.nextSeq
	if err := s.aligner.AddEvent(e); err != nil {
		var te *timeline.Error
		if errors.As(err, &te) {
			s.losses = append(s.losses, *te)
		}
	}
}

func (s *sink) OnLoss(id string, seq uint16, off int64) {
	s.losses = append(s.losses, *timeline.NewError(timeline.CodePacketLoss,
		"parse", id, off, fmt.Sprintf("missing sequence %d", seq), nil))
}

// stageParse streams every input through its decoder into a fresh aligner.
func (r *Run) stageParse() (*timeline.Aligner, error) {
	aligner := timeline.NewAligner(r.cfg)
	sk := &sink{aligner: aligner}
	for _, in := range r.inputs {
		switch in.Kind {
		case "mp4":
			if err := decode.ParseMP4(bytes.NewReader(in.Data), in.ID, sk); err != nil {
				return nil, err // structured error already carries raw offset
			}
		case "rtp":
			p := decode.NewRTPParser(in.ID, in.ClockRate)
			if err := p.Parse(bytes.NewReader(in.Data), sk); err != nil {
				return nil, err
			}
		default:
			return nil, fmt.Errorf("pipeline: unknown input kind %q", in.Kind)
		}
	}
	r.parseLosses = sk.losses
	return aligner, nil
}

// stageTransform runs repair; conflicts are returned as an error so the
// pipeline stops before validate/commit.
func (r *Run) stageTransform(aligner *timeline.Aligner) (*timeline.AlignmentResult, error) {
	result, err := aligner.Align()
	if err != nil && !errors.Is(err, timeline.ErrConflict) {
		return nil, err
	}
	if errors.Is(err, timeline.ErrConflict) {
		// Conflicts block publication; keep result for diagnostics.
		return result, fmt.Errorf("%w: %d conflict(s)", timeline.ErrConflict, len(result.Conflicts))
	}
	return result, nil
}

// stageVerifyInvariants checks ordering, sorting and provenance invariants.
func (r *Run) stageValidate(result *timeline.AlignmentResult) error {
	if result == nil {
		return timeline.NewError(timeline.CodeValidation, "validate", "", -1,
			"nil alignment result", nil)
	}
	var failures int
	for i := 1; i < len(result.Events); i++ {
		if result.Events[i].Ticks < result.Events[i-1].Ticks {
			failures++
		}
	}
	for _, ev := range result.Events {
		if ev.ContentHash == "" || ev.RuleID == "" || ev.Offset.End <= ev.Offset.Begin {
			failures++
		}
	}
	if len(result.Conflicts) > 0 {
		return timeline.NewError(timeline.CodeValidation, "validate", "",
			result.Conflicts[0].Offset, "unresolved cross-track conflicts", timeline.ErrConflict)
	}
	if failures > 0 {
		return timeline.NewError(timeline.CodeValidation, "validate", "", -1,
			fmt.Sprintf("%d invariant failures", failures), nil)
	}
	return nil
}

// stageCommit builds the versioned index and publishes it atomically, then
// writes the provenance log mapping segments back to input offsets.
func (r *Run) stageCommit(result *timeline.AlignmentResult) (*index.Index, error) {
	idx, err := index.Build(result, r.inputSHA, r.configHash)
	if err != nil {
		return nil, timeline.NewError(timeline.CodeValidation, "commit", "", -1,
			"index build refused", err)
	}
	if err := idx.Save(r.indexPath); err != nil {
		return nil, timeline.NewError(timeline.CodeTruncatedIndex, "commit", "", -1,
			"atomic index publish failed", err)
	}
	if err := r.writeProvenance(result, idx); err != nil {
		return nil, err
	}
	return idx, nil
}

// writeProvenance groups repaired events into segments and records the
// input-offset → output-segment chain with stable per-segment hashes.
func (r *Run) writeProvenance(result *timeline.AlignmentResult, idx *index.Index) error {
	log := journal.ProvenanceLog{RunID: r.RunID}
	known := map[string]bool{}
	for _, rule := range result.Rules {
		known[rule.ID] = true
	}
	var segID int
	var hashes []string
	var rec journal.ProvenanceRecord
	flush := func() {
		if len(rec.EventSeqs) == 0 {
			return
		}
		// Deterministic internal ordering so re-runs hash identically even
		// when tick-equal events arrive in a different ordinal order.
		sort.Ints(rec.EventSeqs)
		sort.Slice(rec.InputOffsets, func(i, j int) bool {
			return rec.InputOffsets[i] < rec.InputOffsets[j]
		})
		sort.Strings(rec.RuleIDs)
		sort.Slice(rec.OffsetRanges, func(i, j int) bool {
			return rec.OffsetRanges[i][0] < rec.OffsetRanges[j][0]
		})
		rec.SegmentID = segID
		rec.SegmentSHA256 = journal.SegmentHash(hashes)
		rec.InputSHA256 = r.inputSHA
		rec.IndexVersion = idx.Version
		_ = log.Add(rec)
		segID++
	}
	for _, ev := range result.Events {
		if len(rec.EventSeqs) >= SegmentSize {
			flush()
			hashes, rec = nil, journal.ProvenanceRecord{SourceID: ev.SourceID}
		}
		if len(rec.EventSeqs) == 0 {
			rec = journal.ProvenanceRecord{SourceID: ev.SourceID}
		}
		rec.InputOffsets = append(rec.InputOffsets, ev.Offset.Begin)
		rec.OffsetRanges = append(rec.OffsetRanges,
			[2]int64{ev.Offset.Begin, ev.Offset.End})
		rec.RuleIDs = append(rec.RuleIDs, ev.RuleID)
		rec.EventSeqs = append(rec.EventSeqs, ev.Seq)
		hashes = append(hashes, ev.ContentHash)
	}
	flush()
	if errs := log.Verify(r.inputSize(), known); len(errs) > 0 {
		return timeline.NewError(timeline.CodeValidation, "commit", "", -1,
			"provenance verification failed", errs[0])
	}
	return log.Save(r.provPath)
}

func (r *Run) inputSize() int64 {
	var n int64
	for _, in := range r.inputs {
		n += int64(len(in.Data))
	}
	return n
}

// buildReport assembles the acceptance metrics from the committed index.
func (r *Run) buildReport(idx *index.Index, result *timeline.AlignmentResult) (*Report, error) {
	prov, err := journal.LoadProvenance(r.provPath)
	if err != nil {
		return nil, err
	}
	rep := &Report{
		RunID:            r.RunID,
		ResumedStage:     r.resumedStage,
		Segments:         len(prov.Records),
		KeyframeHitRate:  idx.KeyframeHitRate(),
		NoKeyframeTracks: result.NoKeyframeTracks,
		IndexSHA256:      idx.SHA256,
		SourceOffsets:    collectOffsets(result),
	}
	for _, rec := range prov.Records {
		rep.SegmentSHA256 = append(rep.SegmentSHA256, rec.SegmentSHA256)
	}
	rep.RoundtripMatchRate = r.roundtripRate(result)
	rep.Conflicts = result.Conflicts
	return rep, nil
}

func collectOffsets(result *timeline.AlignmentResult) []int64 {
	seen := map[int64]bool{}
	var out []int64
	for _, ev := range result.Events {
		if !seen[ev.Offset.Begin] {
			seen[ev.Offset.Begin] = true
			out = append(out, ev.Offset.Begin)
		}
	}
	return out
}

// roundtripRate verifies each repaired tick converts back into the track's
// anchor-relative raw timestamp within one timescale unit; the acceptance
// target is 100%.
func (r *Run) roundtripRate(result *timeline.AlignmentResult) float64 {
	if len(result.Events) == 0 {
		return 1
	}
	scaleByTrack := map[string]uint32{}
	baseByTrack := map[string]int64{}
	for _, tr := range result.Tracks {
		scaleByTrack[tr.ID] = tr.Timescale
	}
	for _, ev := range result.Events {
		if _, ok := baseByTrack[ev.TrackID]; !ok {
			baseByTrack[ev.TrackID] = ev.RawTS
		}
		if ev.RawTS < baseByTrack[ev.TrackID] {
			baseByTrack[ev.TrackID] = ev.RawTS
		}
	}
	match := 0
	for _, ev := range result.Events {
		scale := scaleByTrack[ev.TrackID]
		if scale == 0 {
			continue
		}
		wantRel := ev.RawTS - baseByTrack[ev.TrackID]
		backRel := clockFromTicks(ev.Ticks, scale)
		if d := backRel - wantRel; d <= 1 && d >= -1 {
			match++
		}
	}
	return float64(match) / float64(len(result.Events))
}
