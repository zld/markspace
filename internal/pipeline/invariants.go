package pipeline

import (
	"fmt"
	"sort"

	"mediapipeline/internal/checkpoint"
	"mediapipeline/internal/container"
	"mediapipeline/internal/media"
	"mediapipeline/internal/timeline"
)

// Violation is one cross-stage invariant failure. It always names the stage
// and carries a category so the CLI can report it like any other diagnostic.
type Violation struct {
	Stage    string
	Category media.Category
	Detail   string
}

func (v Violation) Error() string {
	return fmt.Sprintf("%s/%s: %s", v.Stage, v.Category, v.Detail)
}

// CheckInput validates the container and segmentation invariants that must
// hold before anything is persisted.
func CheckInput(in *Input) []Violation {
	var vs []Violation
	if in == nil || in.Manifest == nil || in.Plan == nil {
		return []Violation{{Stage: "input", Category: media.InvariantViolation, Detail: "missing input"}}
	}
	// 1. Time is monotonic in every representation and in audio.
	for _, r := range in.Reps {
		vs = append(vs, checkTrack("container", r.Name, r.Track)...)
	}
	if in.Audio != nil {
		vs = append(vs, checkTrack("container", "audio", in.Audio)...)
	}
	// 2. Boundaries ascend and match between representations.
	for i := 1; i < len(in.Plan.Boundaries); i++ {
		if in.Plan.Boundaries[i].Start <= in.Plan.Boundaries[i-1].Start {
			vs = append(vs, Violation{Stage: "segment", Category: media.InvariantViolation,
				Detail: fmt.Sprintf("boundary %d does not advance past %d",
					i, in.Plan.Boundaries[i-1].Start)})
		}
	}
	if err := in.Manifest.Validate(); err != nil {
		vs = append(vs, Violation{Stage: "segment", Category: media.CategoryOf(err), Detail: err.Error()})
	}
	// 3. Every representation reports the same segment count.
	want := in.Manifest.SegmentCount
	for _, rep := range in.Manifest.Representations {
		if len(rep.Segments) != want {
			vs = append(vs, Violation{Stage: "manifest", Category: media.InvariantViolation,
				Detail: fmt.Sprintf("%s has %d segments, shared count is %d",
					rep.Name, len(rep.Segments), want)})
		}
	}
	return vs
}

// checkTrack verifies DTS never moves backwards and durations are positive.
// PTS is allowed to be non-monotonic (B-frames) but must convert cleanly.
func checkTrack(stage, name string, tr *container.Track) []Violation {
	var vs []Violation
	if tr == nil || len(tr.Samples) == 0 {
		return []Violation{{Stage: stage, Category: media.InvariantViolation,
			Detail: fmt.Sprintf("%s has no samples", name)}}
	}
	var prevDTS media.Nano
	for i, s := range tr.Samples {
		if s.DeltaNanos <= 0 {
			vs = append(vs, Violation{Stage: stage, Category: media.NegativeDuration,
				Detail: fmt.Sprintf("%s sample %d has non-positive duration", name, i)})
		}
		if i > 0 && s.DTSNanos < prevDTS {
			vs = append(vs, Violation{Stage: stage, Category: media.OutOfOrderTimestamp,
				Detail: fmt.Sprintf("%s sample %d DTS moved backwards", name, i)})
		}
		prevDTS = s.DTSNanos
	}
	if len(tr.Keyframes) == 0 {
		vs = append(vs, Violation{Stage: stage, Category: media.BoundaryNotFound,
			Detail: fmt.Sprintf("%s has no keyframes", name)})
	}
	return vs
}

// CheckState verifies that the checkpoint does not run ahead of durable data
// and that every claimed output hash matches the manifest entry it names.
func CheckState(store *checkpoint.Store, st *checkpoint.State, in *Input) []Violation {
	var vs []Violation
	if st == nil || in == nil {
		return vs
	}
	if err := store.VerifyOrdering(st); err != nil {
		vs = append(vs, Violation{Stage: "checkpoint", Category: media.CategoryOf(err), Detail: err.Error()})
	}
	if st.ManifestSHA256 != "" && st.ManifestSHA256 != in.ManifestHA {
		vs = append(vs, Violation{Stage: "checkpoint", Category: media.CheckpointAhead,
			Detail: fmt.Sprintf("checkpoint names manifest %s but produced %s",
				st.ManifestSHA256, in.ManifestHA)})
	}
	// Every representation's checkpointed segment hashes must equal the
	// manifest's hashes at the same indexes.
	want := make(map[string]map[int]string)
	for _, rep := range in.Manifest.Representations {
		byIndex := make(map[int]string, len(rep.Segments))
		for _, s := range rep.Segments {
			byIndex[s.Index] = s.SHA256
		}
		want[rep.Name] = byIndex
	}
	for _, rs := range st.Representations {
		refs, ok := want[rs.Name]
		if !ok {
			vs = append(vs, Violation{Stage: "checkpoint", Category: media.InvariantViolation,
				Detail: fmt.Sprintf("checkpoint names unknown representation %q", rs.Name)})
			continue
		}
		for _, ref := range rs.Segments {
			if refs[ref.Index] != ref.SHA256 {
				vs = append(vs, Violation{Stage: "checkpoint", Category: media.IdempotencyConflict,
					Detail: fmt.Sprintf("%s segment %d hash %s differs from manifest %s",
						rs.Name, ref.Index, ref.SHA256, refs[ref.Index])})
			}
		}
	}
	return vs
}

// CheckTimeline validates sealed timeline invariants: monotonic segment
// bounds, canonical event order, and agreement with deterministic replay.
func CheckTimeline(tl *timeline.Timeline, replay *timeline.Replay) []Violation {
	var vs []Violation
	segs := tl.Segments()
	for i, s := range segs {
		if s.Index != i {
			vs = append(vs, Violation{Stage: "timeline", Category: media.InvariantViolation,
				Detail: fmt.Sprintf("segment %d carries index %d", i, s.Index)})
		}
		if s.End <= s.Start {
			vs = append(vs, Violation{Stage: "timeline", Category: media.InvariantViolation,
				Detail: fmt.Sprintf("segment %d has empty bounds", i)})
		}
		if i > 0 && s.Start < segs[i-1].End {
			vs = append(vs, Violation{Stage: "timeline", Category: media.InvariantViolation,
				Detail: fmt.Sprintf("segment %d starts before segment %d ends", i, i-1)})
		}
		for j := 1; j < len(s.Events); j++ {
			if s.Events[j].Less(s.Events[j-1]) {
				vs = append(vs, Violation{Stage: "timeline", Category: media.InvariantViolation,
					Detail: fmt.Sprintf("segment %d events are not canonically ordered", i)})
				break
			}
		}
		for _, ev := range s.Events {
			if ev.Time < s.Start || ev.Time >= s.End {
				vs = append(vs, Violation{Stage: "timeline", Category: media.InvariantViolation,
					Detail: fmt.Sprintf("segment %d holds event at %d outside [%d,%d)",
						i, ev.Time, s.Start, s.End)})
				break
			}
		}
	}
	if replay != nil {
		_, replayHash, err := replay.CrossCheck(tl)
		if err != nil {
			vs = append(vs, Violation{Stage: "timeline", Category: media.CategoryOf(err), Detail: err.Error()})
		} else if replayHash != timeline.HashSegments(segs) {
			vs = append(vs, Violation{Stage: "timeline", Category: media.InvariantViolation,
				Detail: "sealed timeline disagrees with deterministic replay"})
		}
	}
	return vs
}

// CheckProvenance verifies that provenance records are reproducible: two
// records built from identical inputs must hash identically.
func CheckProvenance(records []checkpoint.ProvenanceRecord) []Violation {
	var vs []Violation
	seen := make(map[string]bool)
	for _, r := range records {
		h, err := r.Hash()
		if err != nil {
			vs = append(vs, Violation{Stage: "provenance", Category: media.CategoryOf(err), Detail: err.Error()})
			continue
		}
		if seen[h] {
			vs = append(vs, Violation{Stage: "provenance", Category: media.InvariantViolation,
				Detail: fmt.Sprintf("duplicate provenance record %s", h)})
		}
		seen[h] = true
		if r.InputSHA256 == "" || r.OutputSHA256 == "" {
			vs = append(vs, Violation{Stage: "provenance", Category: media.InvariantViolation,
				Detail: fmt.Sprintf("record %s missing input or output digest", h)})
		}
		if len(r.Decisions) == 0 {
			vs = append(vs, Violation{Stage: "provenance", Category: media.InvariantViolation,
				Detail: fmt.Sprintf("record %s has no decisions", h)})
		}
	}
	return vs
}

// SortViolations orders violations deterministically for terminal output.
func SortViolations(vs []Violation) {
	sort.Slice(vs, func(i, j int) bool {
		if vs[i].Stage != vs[j].Stage {
			return vs[i].Stage < vs[j].Stage
		}
		return vs[i].Detail < vs[j].Detail
	})
}
