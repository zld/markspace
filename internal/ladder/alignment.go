// Package ladder aligns segment boundaries across multiple bitrate
// representations. A boundary is only legal when every rendition can decode
// there, so the global cut list is the intersection of all renditions'
// common decodable points. Renditions unable to meet the target duration
// within tolerance are rejected rather than silently misaligned.
package ladder

import (
	"sort"

	"mediapack/internal/manifest"
	"mediapack/internal/model"
	"mediapack/internal/timeline"
)

// Representation is one bitrate level with its own aligned timeline.
type Representation struct {
	ID       string
	Bitrate  int64
	Timeline *timeline.MasterTimeline
}

// Result is the aligned ladder.
type Result struct {
	GlobalBoundaries []int64                   `json:"global_boundaries_ns"`
	Plans            map[string]*manifest.Plan `json:"plans"`
	Aligned          []string                  `json:"aligned_renditions"`
	Rejected         map[string]string         `json:"rejected_renditions,omitempty"`
}

// Config mirrors manifest.Config plus a rendition count cap.
type Config struct {
	TargetSegmentNS int64
	ToleranceNS     int64
	MaxRenditions   int
}

// Align computes the global boundary set and segments every representation on
// it. A representation whose plan cannot satisfy the tolerance is recorded in
// Rejected and the call returns the structured error after processing all
// renditions, so every failure is reported.
func Align(reps []Representation, cfg Config, es *model.Errors) (*Result, error) {
	if cfg.MaxRenditions > 0 && len(reps) > cfg.MaxRenditions {
		es.Newf(model.StageLadder, model.CodeLimitExceeded, "", 0, model.ByteRange{},
			"rendition count %d exceeds limit %d", len(reps), cfg.MaxRenditions)
		return nil, es.Err()
	}
	if len(reps) == 0 {
		es.Newf(model.StageLadder, model.CodeMissingTrack, "", 0, model.ByteRange{},
			"no representations to align")
		return nil, es.Err()
	}
	ordered := make([]Representation, len(reps))
	copy(ordered, reps)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].ID < ordered[j].ID })

	global := intersectAll(ordered)
	if len(global) < 2 {
		es.Newf(model.StageLadder, model.CodeAlignTolerance, "", 0, model.ByteRange{},
			"renditions share fewer than two decodable points (%d)", len(global))
		return nil, es.Err()
	}
	res := &Result{
		GlobalBoundaries: global,
		Plans:            make(map[string]*manifest.Plan, len(ordered)),
		Rejected:         map[string]string{},
	}
	for _, rep := range ordered {
		// Force this rendition onto the shared grid: every global boundary is
		// by construction one of its own decodable points.
		synthetic := &timeline.MasterTimeline{
			MasterTrack: rep.Timeline.MasterTrack,
			StartNS:     global[0], EndNS: global[len(global)-1],
			CommonPoints: global,
		}
		var local model.Errors
		plan, perr := manifest.Build(synthetic, manifest.Config{
			TargetSegmentNS: cfg.TargetSegmentNS, ToleranceNS: cfg.ToleranceNS,
		}, &local)
		if plan != nil {
			res.Plans[rep.ID] = plan
		}
		if perr != nil {
			reason := plan.RejectReason
			if reason == "" {
				reason = perr.Error()
			}
			res.Rejected[rep.ID] = reason
			es.Newf(model.StageLadder, model.CodeAlignTolerance, "", rep.Timeline.MasterTrack,
				model.ByteRange{}, "rendition %s rejected: %s", rep.ID, reason)
			continue
		}
		res.Aligned = append(res.Aligned, rep.ID)
	}
	if es.Len() > 0 {
		return res, es.Err()
	}
	return res, nil
}

// intersectAll returns the sorted common points shared by every rendition.
func intersectAll(reps []Representation) []int64 {
	if len(reps) == 0 {
		return nil
	}
	counts := map[int64]int{}
	for _, rep := range reps {
		seen := map[int64]struct{}{}
		for _, p := range rep.Timeline.CommonPoints {
			if _, ok := seen[p]; ok {
				continue
			}
			seen[p] = struct{}{}
			counts[p]++
		}
	}
	want := len(reps)
	out := make([]int64, 0)
	for p, n := range counts {
		if n == want {
			out = append(out, p)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}
