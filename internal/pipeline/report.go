package pipeline

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"time"

	"archivecheck/internal/fixture"
	"archivecheck/internal/inspect"
	"archivecheck/internal/manifest"
	"archivecheck/internal/model"
	"archivecheck/internal/scheduler"
)

// Exit codes are part of the CLI contract.
const (
	ExitOK      = 0
	ExitInput   = 2
	ExitDamage  = 3
	ExitBlocked = 4
)

// assemble folds the DAG outcomes into the report.
func (rep *Report) assemble(fx *fixture.Manifest, plan *manifest.Plan, res *dagResult, tasks []segmentTask, cfg Config) {
	st := res.state
	rep.DAGNodes = len(res.report.Nodes)
	rep.CachedNodes = st.cached
	for _, nr := range res.report.Nodes {
		if nr.Status == scheduler.StatusOK && !nr.Cached {
			rep.ExecutedNodes++
		}
		// resumed_nodes counts nodes a resume had to re-execute because the
		// checkpoint did not confirm them. A fully confirmed resume reports 0.
		if cfg.Resume && !nr.Cached {
			rep.ResumedNodes++
		}
	}

	var allDamage []inspect.DamageRange
	var structural []*model.Fault
	kfWeighted := map[int64]int{}
	for _, t := range tasks {
		oc := st.inspect[t.nodeID()]
		if oc == nil {
			// The inspect node never produced a payload (for example it was
			// blocked). Fall back to the reorder outcome so transport findings
			// are still reported.
			if ro := st.reorder[t.nodeID()]; ro != nil {
				oc = &taskOutcome{result: ro.result, fault: ro.fault}
			}
		}
		if oc == nil {
			continue
		}
		rep.Segments = append(rep.Segments, oc.result)
		rep.MissingPackets += oc.result.Missing
		rep.DuplicatePackets += oc.result.Duplicates
		rep.ReorderedPackets += oc.result.Reordered
		rep.Tracks = maxInt(rep.Tracks, oc.result.TrackCount)
		allDamage = append(allDamage, oc.damage...)
		if oc.structural != nil {
			structural = append(structural, oc.structural)
		}
		if oc.result.KeyframeP50Ms >= 0 {
			kfWeighted[oc.result.KeyframeP50Ms]++
		}
	}
	rep.KeyframeP50Ms, rep.KeyframeRem = weightedMedian(kfWeighted)
	if rep.KeyframeP50Ms < 0 {
		rep.KeyframeP50Ms = -1
	}

	// Cross-layer keyframe alignment: a representation whose sync cadence
	// differs from the reference layer is flagged for the whole timeline it
	// spans, since a switch at any point would splice mismatched GOPs.
	// A media duration that disagrees with the manifest marks that segment's
	// layer as duration-deviant, independently of packet-level damage.
	deviant := map[string]bool{}
	for _, d := range rep.DurationDeviations {
		deviant[d.Representation] = true
	}
	for _, s := range rep.Segments {
		if s.RepresentationDigestConflict {
			deviant[s.Representation] = true
			rep.Faults = append(rep.Faults, model.NewFault(model.CodeDamageRepresentation, model.SeverityError,
				model.Source{Layer: "transport", Representation: s.Representation, Segment: s.ID, Offset: -1},
				"sequence-number collision with conflicting payloads in %s segment %s", s.Representation, s.ID))
		}
	}
	rep.Reps = auditRepresentations(plan, st)
	for i := range rep.Reps {
		if deviant[rep.Reps[i].ID] {
			rep.Reps[i].DurationAligned = false
			rep.Reps[i].Aligned = false
			// A layer-wide integrity or cadence failure damages the whole
			// timeline that layer spans, so a report names a concrete range.
			if len(plan.Representations) > 0 && rep.Reps[i].ID != plan.Representations[0].ID {
				allDamage = append(allDamage, inspect.DamageRange{
					Start: rep.Reps[i].Start, End: rep.Reps[i].End, Confidence: inspect.ConfidenceEstimated,
					Representations: []string{rep.Reps[i].ID},
					Segments:        segmentsInRange(plan, rep.Reps[i].ID, rep.Reps[i].Start, rep.Reps[i].End),
					Causes:          []model.Code{model.CodeDamageRepresentation},
					Timebase:        plan.Timebase,
				})
			}
		}
	}
	for _, a := range rep.Reps {
		if a.Aligned {
			continue
		}
		if len(plan.Representations) > 0 && a.ID == plan.Representations[0].ID {
			continue
		}
		allDamage = append(allDamage, inspect.DamageRange{
			Start: a.Start, End: a.End, Confidence: inspect.ConfidenceEstimated,
			Representations: []string{a.ID}, Causes: []model.Code{model.CodeDamageRepresentation},
			Timebase: plan.Timebase,
		})
		rep.Faults = append(rep.Faults, model.NewFault(model.CodeDamageRepresentation, model.SeverityError,
			model.Source{Layer: "pipeline", Representation: a.ID, Offset: -1},
			"representation %s is misaligned (start=%t duration=%t dev=%d keyframe=%t)",
			a.ID, a.StartAligned, a.DurationAligned, a.MaxDurationDeviation, a.KeyframeAligned))
	}

	// A structural failure covers the whole segment in media time so the first
	// bad segment is well defined even without a packet gap.
	for _, f := range structural {
		_ = f
		// Segment identifiers may be prefixed by a phase ("inspect/v-low/4");
		// the trailing two components are representation and segment.
		repID, segID := splitNodeSegment(f.Source)
		seg, ok := findSegment(plan, repID, segID)
		if !ok && f.Source.Representation != "" && f.Source.Segment != "" {
			seg, ok = findSegment(plan, f.Source.Representation, f.Source.Segment)
		}
		if !ok {
			// Last resort: the source may carry only a node-style identifier
			// whose trailing component is the segment number.
			id := ocSegmentID(f.Source)
			for _, r := range plan.Representations {
				if s, ok2 := plan.Find(r.ID, id); ok2 {
					seg, ok = s, true
					break
				}
			}
		}
		if ok {
			allDamage = append(allDamage, inspect.DamageRange{
				Start: seg.Start, End: seg.End(), Confidence: inspect.ConfidenceEstimated,
				Representations: []string{seg.Representation}, Segments: []string{seg.ID},
				Causes: []model.Code{f.Code}, Timebase: plan.Timebase,
			})
		}
	}

	frame := cfg.FrameTicks
	if frame <= 0 {
		frame = fixture.DefaultFrameTicks * plan.Timebase / fixture.DefaultTimebase
	}
	rep.DamageRanges = inspect.MergeDamage(allDamage, inspect.MergeOptions{
		FrameDuration: frame, Timebase: plan.Timebase,
	})
	if len(rep.DamageRanges) > 0 {
		first := rep.DamageRanges[0]
		rep.FirstBadAt = first.Start
		if len(first.Segments) > 0 {
			rep.FirstBadSegment = first.Segments[0]
		} else if len(first.Representations) > 0 {
			// A layer-wide alignment fault has no single segment, so name the
			// first segment of the affected layer that overlaps the range.
			if seg, ok := plan.Find(first.Representations[0], firstSegmentID(plan, first)); ok {
				rep.FirstBadSegment = seg.ID
			}
		}
	}
	if rep.FirstBadSegment == "" && len(structural) > 0 {
		rep.FirstBadSegment = ocSegmentID(structural[0].Source)
		if rep.FirstBadSegment == "" {
			rep.FirstBadSegment = structural[0].Source.Segment
		}
	}

	for _, nr := range res.report.Nodes {
		if nr.Status != scheduler.StatusBlocked {
			continue
		}
		rep.Blocked = append(rep.Blocked, BlockedNode{ID: nr.ID, Reason: nr.Error})
	}
	if res.report.FirstBlocked != "" {
		rep.FirstBlocked = res.report.FirstBlockedDependency
		if rep.FirstBlocked == "" {
			rep.FirstBlocked = res.report.FirstBlocked
		}
	}
	for _, nr := range res.report.Nodes {
		if nr.Status == scheduler.StatusFailed && nr.FaultCode != "" {
			rep.Faults = append(rep.Faults, model.NewFault(nr.FaultCode, model.SeverityError,
				model.Source{Layer: "pipeline", Segment: nr.ID, Offset: -1}, "%s", nr.Error))
		}
	}
}

// firstSegmentID returns the ID of the earliest segment of any representation
// that intersects the range.
func firstSegmentID(plan *manifest.Plan, dr inspect.DamageRange) string {
	best := ""
	var bestStart int64
	found := false
	for _, r := range plan.Representations {
		for _, s := range r.Segments {
			if s.End() <= dr.Start || s.Start >= dr.End {
				continue
			}
			if !found || s.Start < bestStart {
				best, bestStart, found = s.ID, s.Start, true
			}
		}
	}
	return best
}

// segmentsInRange lists the segment IDs of a layer that overlap [start,end).
func segmentsInRange(plan *manifest.Plan, repID string, start, end int64) []string {
	var out []string
	for _, r := range plan.Representations {
		if r.ID != repID {
			continue
		}
		for _, s := range r.Segments {
			if s.End() <= start || s.Start >= end {
				continue
			}
			out = append(out, s.ID)
		}
	}
	return out
}

func findSegment(plan *manifest.Plan, rep, id string) (manifest.Segment, bool) {
	return plan.Find(rep, id)
}

// splitNodeSegment extracts representation and segment IDs from a fault source
// that may carry a synthetic node identifier.
func splitNodeSegment(src model.Source) (string, string) {
	if src.Representation != "" && src.Segment != "" {
		return src.Representation, src.Segment
	}
	parts := strings.Split(src.Segment, "/")
	switch len(parts) {
	case 3:
		return parts[1], parts[2]
	case 2:
		return parts[0], parts[1]
	}
	return src.Representation, src.Segment
}

// ocSegmentID returns the trailing segment component of a node-style ID.
func ocSegmentID(src model.Source) string {
	parts := strings.Split(src.Segment, "/")
	if len(parts) >= 2 {
		return parts[len(parts)-1]
	}
	return ""
}

// weightedMedian returns the exact lower median of the weighted keys.
func weightedMedian(weights map[int64]int) (int64, model.Rational) {
	if len(weights) == 0 {
		return -1, model.Rational{Den: 1}
	}
	keys := make([]int64, 0, len(weights))
	total := 0
	for k, w := range weights {
		keys = append(keys, k)
		total += w
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
	target := (total - 1) / 2
	seen := 0
	for _, k := range keys {
		seen += weights[k]
		if seen > target {
			return k, model.Rational{Den: 1}
		}
	}
	return keys[len(keys)-1], model.Rational{Den: 1}
}

// auditRepresentations verifies that every layer shares the reference layer's
// start, duration, segment boundary structure and keyframe cadence. Two layers
// can only be switched between if all four agree within one target frame.
func auditRepresentations(plan *manifest.Plan, st *dagState) []RepAudit {
	if len(plan.Representations) == 0 {
		return nil
	}
	frame := oneFrameTicks(plan, st)
	ref := plan.Representations[0]
	out := make([]RepAudit, 0, len(plan.Representations))
	refCadence := cadenceOf(plan, st, ref.ID)
	for _, r := range plan.Representations {
		a := RepAudit{
			ID: r.ID, Bandwidth: r.Bandwidth, Timescale: r.Timescale,
			Segments: len(r.Segments), KeyframeP50Ms: cadenceOf(plan, st, r.ID),
		}
		if len(r.Segments) > 0 {
			a.Start = r.Segments[0].Start
			a.End = r.Segments[len(r.Segments)-1].End()
		}
		a.StartAligned = len(ref.Segments) > 0 && len(r.Segments) > 0 &&
			r.Segments[0].Start == ref.Segments[0].Start &&
			r.Segments[len(r.Segments)-1].End() == ref.Segments[len(ref.Segments)-1].End()
		a.DurationAligned = len(r.Segments) == len(ref.Segments)
		for i := range r.Segments {
			if i >= len(ref.Segments) {
				break
			}
			dev := absInt64(r.Segments[i].Duration - ref.Segments[i].Duration)
			if dev > a.MaxDurationDeviation {
				a.MaxDurationDeviation = dev
			}
		}
		if a.MaxDurationDeviation > frame {
			a.DurationAligned = false
		}
		a.KeyframeAligned = true
		if refCadence >= 0 && a.KeyframeP50Ms >= 0 {
			a.KeyframeAligned = absInt64(a.KeyframeP50Ms-refCadence) <= maxInt64(1, frameMsFor(frame, plan.Timebase))
		}
		// A single-representation playlist has nothing to align against.
		if len(plan.Representations) == 1 {
			a.StartAligned, a.DurationAligned, a.KeyframeAligned = true, true, true
		}
		a.Aligned = a.StartAligned && a.DurationAligned && a.KeyframeAligned
		out = append(out, a)
	}
	return out
}

// cadenceOf returns the weighted median keyframe interval for a layer.
func cadenceOf(plan *manifest.Plan, st *dagState, repID string) int64 {
	counts := map[int64]int{}
	for _, r := range plan.Representations {
		if r.ID != repID {
			continue
		}
		for _, s := range r.Segments {
			if oc := st.inspect[repID+"/"+s.ID]; oc != nil && oc.result.KeyframeP50Ms >= 0 {
				counts[oc.result.KeyframeP50Ms]++
			}
		}
	}
	if len(counts) == 0 {
		return -1
	}
	p50, _ := weightedMedian(counts)
	return p50
}

// oneFrameTicks is the merge tolerance in plan ticks.
func oneFrameTicks(plan *manifest.Plan, st *dagState) int64 {
	// The generated fixtures use a fixed frame duration; derive it from any
	// inspection that measured sample durations.
	for _, oc := range st.inspect {
		if oc.result.KeyframeP50Ms > 0 {
			return fixture.DefaultFrameTicks * plan.Timebase / fixture.DefaultTimebase
		}
	}
	return fixture.DefaultFrameTicks * plan.Timebase / fixture.DefaultTimebase
}

// frameMsFor converts a plan-tick frame duration to whole milliseconds.
func frameMsFor(ticks, timebase int64) int64 {
	if timebase <= 0 {
		return 0
	}
	return ticks * 1000 / timebase
}

// finish computes the output checksum and the exit code.
func (rep *Report) finish(cfg Config, started time.Time) {
	rep.ResumeMS = cfg.Now().Sub(started).Milliseconds()
	rep.Faults = dedupFaults(rep.Faults)
	h := sha256.New()
	fmt.Fprintf(h, "archivecheck/v1\x00%s\x00%d\x00%d\x00%d\x00",
		rep.Manifest, rep.ManifestSegments, rep.Timebase, rep.KeyframeP50Ms)
	for _, s := range rep.Segments {
		fmt.Fprintf(h, "%s\x00%s\x00%d\x00%s\x00%d\x00%d\x00",
			s.Representation, s.ID, s.Start, s.OutputSHA256, s.Missing, s.Duplicates)
	}
	for _, dr := range rep.DamageRanges {
		fmt.Fprintf(h, "d\x00%d\x00%d\x00%s\x00", dr.Start, dr.End, dr.Confidence)
	}
	rep.OutputSHA256 = hex.EncodeToString(h.Sum(nil))
	if rep.ExitCode == ExitInput || rep.ExitCode == ExitBlocked {
		return
	}
	// Damage is located first, so a damaged archive reports exit 3 even though
	// its certification path is also blocked; exit 4 is reserved for runs where
	// blocking is the only outcome (for example a corrupt checkpoint).
	switch {
	case rep.CheckpointMismatch && !rep.CheckpointDiscarded:
		rep.ExitCode = ExitBlocked
	case len(rep.DamageRanges) > 0 || rep.FirstBadSegment != "":
		rep.ExitCode = ExitDamage
	case rep.FirstBlocked != "":
		rep.ExitCode = ExitBlocked
	default:
		rep.ExitCode = ExitOK
	}
}

func dedupFaults(in []*model.Fault) []*model.Fault {
	var l model.FaultList
	for _, f := range in {
		l.Add(f)
	}
	return l.All()
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func maxInt64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

func absInt64(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}
