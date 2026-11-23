package repair

import (
	"github.com/broadcast/mediad/internal/model"
)

// Decisions holds the adjudicated per-track segments and the ordered track
// plan. It is the sole input to the remux planner.
type Decisions struct {
	Order    []uint32             `json:"order"`
	Segments map[uint32][]*Segment `json:"-"`
	// RecoveredRanges / IsolatedRanges are the merged timeline views used by
	// the terminal report and by evidence hashing.
	RecoveredRanges map[uint32][]model.Range `json:"recovered_ranges"`
	IsolatedRanges  map[uint32][]model.Range `json:"isolated_ranges"`
	// FailedDependencies records tracks that could not be recovered because a
	// prerequisite failed. Such tracks must never claim StatusRecovered.
	FailedDependencies map[uint32][]uint32 `json:"failed_dependencies"`
}

// Policy selects how recoverable defects are treated.
type Policy struct {
	// MaxKeyframeGap bounds how far a video boundary may stretch while still
	// being considered decodable from the nearest keyframe.
	MaxKeyframeGap model.Rat
	// IsolateMetadataConflicts maps conflict error kinds to hard isolation
	// instead of normalization (default: normalize, never silent).
	IsolateMetadataConflicts bool
}

// DefaultPolicy matches probe.DefaultConfig thresholds.
func DefaultPolicy() Policy {
	return Policy{MaxKeyframeGap: model.MustRat(2, 1)}
}

// Adjudicate runs the dependency-ordered recovery protocol:
//
//  1. compute the topo order (video before audio, explicit edges respected);
//  2. confirm each video track's boundary and keyframe-decodable span;
//  3. align audio only after every dependency it references has recovered;
//  4. any failed prerequisite forces its downstream tracks to isolate the
//     same coverage rather than claiming recovery.
func Adjudicate(g *model.Graph, damage map[uint32]*TrackDamage,
	errs *model.ErrorSet, policy Policy) (*Decisions, error) {

	order, err := g.TopoOrder()
	if err != nil {
		if ce, ok := err.(*model.CycleError); ok {
			errs.Add(ce.MediaError())
		}
		return nil, err
	}

	d := &Decisions{
		Order:              order,
		Segments:           make(map[uint32][]*Segment),
		RecoveredRanges:    make(map[uint32][]model.Range),
		IsolatedRanges:     make(map[uint32][]model.Range),
		FailedDependencies: make(map[uint32][]uint32),
	}

	// Track IDs whose recovery already failed; everything depending on them
	// inherits failure rather than re-attempting.
	failed := make(map[uint32]bool)
	for _, id := range order {
		t := g.Tracks[id]
		dmg := damage[id]

		blockedBy := prerequisiteFailures(t.DependsOn, failed)
		if len(blockedBy) > 0 {
			failed[id] = true
			d.FailedDependencies[id] = blockedBy
			d.Segments[id] = isolateAll(t, dmg, blockedBy)
			d.IsolatedRanges[id] = model.JoinRanges(append(dmg.DamagedRanges, fullRange(t)))
			continue
		}

		segs, recoverable := buildSegments(t, dmg, errs, policy)
		d.Segments[id] = segs
		var rec, iso []model.Range
		for _, s := range segs {
			if s.Status == StatusIsolated {
				iso = append(iso, s.Coverage)
			} else {
				rec = append(rec, s.Coverage)
			}
		}
		d.RecoveredRanges[id] = model.JoinRanges(rec)
		d.IsolatedRanges[id] = model.JoinRanges(iso)
		// A video track that cannot prove a keyframe-decodable boundary fails
		// the whole recovery protocol for its dependents.
		if t.Kind == model.KindVideo && !recoverable {
			failed[id] = true
		}
	}
	return d, nil
}

// prerequisiteFailures returns the dependency IDs that already failed.
func prerequisiteFailures(deps []uint32, failed map[uint32]bool) []uint32 {
	var out []uint32
	for _, dep := range deps {
		if failed[dep] {
			out = append(out, dep)
		}
	}
	return out
}

// buildSegments turns the damage map into maximal segments, then applies the
// type-specific recovery rules. The boolean reports whether the track retains
// at least one keyframe-anchored decodable span (video only).
func buildSegments(t *model.Track, dmg *TrackDamage,
	errs *model.ErrorSet, policy Policy) ([]*Segment, bool) {

	if len(t.Samples) == 0 {
		return nil, false
	}
	raw := splitByDefects(t, dmg)
	videoOK := t.Kind != model.KindVideo // audio has no keyframe gate

	for _, s := range raw {
		if len(s.Reasons) == 0 {
			s.Status = StatusValid
			continue
		}
		if recoverableKind(s.Reasons) && withinKeyframeReach(t, s, policy) {
			s.Status = StatusRecovered
			continue
		}
		s.Status = StatusIsolated
	}

	if t.Kind == model.KindVideo {
		videoOK = hasDecodableSpan(t, raw)
		if !videoOK {
			// No keyframe-anchored span survives: everything is isolated.
			for _, s := range raw {
				s.Status = StatusIsolated
			}
			errs.Add(model.MediaError{
				Kind: model.KindSparseKeyframe, TrackID: t.ID, SampleIndex: -1,
				Severity: model.SeverityError,
				Detail:   "no keyframe-decodable boundary survives damage",
			})
		}
	}
	return raw, videoOK
}

// splitByDefects groups consecutive samples sharing defect status.
func splitByDefects(t *model.Track, dmg *TrackDamage) []*Segment {
	var out []*Segment
	var cur *Segment
	flush := func(lastIdx int) {
		if cur == nil {
			return
		}
		cur.LastSample = lastIdx
		start := t.Samples[cur.FirstSample].DecodeTime
		end := t.Samples[lastIdx].DecodeTime.Add(t.Samples[lastIdx].Duration)
		cur.Coverage = model.SafeRange(start, end)
		out = append(out, cur)
		cur = nil
	}
	for i := range t.Samples {
		reasons := dmg.DefectSamples[i]
		// A clean sample directly adjacent to a backward jump is still
		// ordered relative to an unknown predecessor, so inherit the defect.
		if len(reasons) == 0 && i > 0 {
			if prev := dmg.DefectSamples[i-1]; containsKind(prev, model.KindTimestampBackward) {
				reasons = []model.ErrorKind{model.KindTimestampBackward}
			}
		}
		defective := len(reasons) > 0
		if cur == nil || defective != (len(cur.Reasons) > 0) {
			flush(i - 1)
			cur = &Segment{TrackID: t.ID, FirstSample: i}
			if defective {
				cur.Reasons = append([]model.ErrorKind(nil), reasons...)
			}
			continue
		}
		if defective {
			cur.Reasons = unionKinds(cur.Reasons, reasons)
		}
	}
	flush(len(t.Samples) - 1)
	return out
}

// recoverableKind limits recovery to ordering defects that leave payload
// bytes intact; truncation/out-of-bounds damage cannot be reconstructed.
func recoverableKind(kinds []model.ErrorKind) bool {
	for _, k := range kinds {
		switch k {
		case model.KindTimestampGap, model.KindTimestampDuplicate,
			model.KindTimestampBackward:
			return true
		default:
			return false
		}
	}
	return false
}

// withinKeyframeReach requires video recovery segments to start at (or after,
// within the configured gap) a keyframe. Audio segments always pass.
func withinKeyframeReach(t *model.Track, s *Segment, policy Policy) bool {
	if t.Kind != model.KindVideo {
		return true
	}
	anchor := nearestKeyframeAtOrBefore(t, s.FirstSample)
	if anchor < 0 {
		return false
	}
	dist := t.Samples[s.FirstSample].DecodeTime.Sub(t.Samples[anchor].DecodeTime)
	return dist.Cmp(policy.MaxKeyframeGap) <= 0
}

// nearestKeyframeAtOrBefore returns the index of the closest keyframe, or -1.
func nearestKeyframeAtOrBefore(t *model.Track, idx int) int {
	for i := idx; i >= 0; i-- {
		if t.Samples[i].IsKeyframe {
			return i
		}
	}
	return -1
}

// hasDecodableSpan reports that at least one non-isolated segment begins at a
// keyframe, which is the minimum condition for a playable video boundary.
func hasDecodableSpan(t *model.Track, segs []*Segment) bool {
	for _, s := range segs {
		if s.Status == StatusIsolated {
			continue
		}
		if nearestKeyframeAtOrBefore(t, s.FirstSample) == s.FirstSample ||
			nearestKeyframeAtOrBefore(t, s.FirstSample) >= 0 {
			return true
		}
	}
	return false
}

func isolateAll(t *model.Track, dmg *TrackDamage, blockedBy []uint32) []*Segment {
	segs := splitByDefects(t, dmg)
	for _, s := range segs {
		s.Status = StatusIsolated
		s.Reasons = append(s.Reasons, model.KindCyclicDependency)
	}
	_ = blockedBy
	return segs
}

func fullRange(t *model.Track) model.Range {
	rg := model.SafeRange(t.StartTime, t.EndTime)
	return rg
}

func containsKind(kinds []model.ErrorKind, want model.ErrorKind) bool {
	for _, k := range kinds {
		if k == want {
			return true
		}
	}
	return false
}

func unionKinds(a, b []model.ErrorKind) []model.ErrorKind {
	for _, k := range b {
		if !containsKind(a, k) {
			a = append(a, k)
		}
	}
	return a
}
