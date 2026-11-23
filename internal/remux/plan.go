// Package remux turns adjudicated segments into a new, self-consistent
// MP4 container: samples in isolated segments are dropped, surviving samples
// keep their payload bytes verbatim, and all sample tables are rebuilt from
// the unified timeline. Output is always re-read and verified before a run
// can report success.
package remux

import (
	"fmt"
	"sort"

	"github.com/broadcast/mediad/internal/model"
	"github.com/broadcast/mediad/internal/repair"
)

// TrackPlan is the per-track write instruction derived from one adjudicated
// track. Kept sample indices reference the source graph.
type TrackPlan struct {
	TrackID     uint32
	Kind        model.MediaKind
	Timescale   uint32
	Kept        []int
	Keyframes   []int // kept-sample-relative keyframe positions (video)
	HasStss     bool
	Video       *model.VideoFormat
	Audio       *model.AudioFormat
	Conflicts   []model.ConflictDecision
}

// Plan is the full dependency-ordered remux instruction set.
type Plan struct {
	Order  []uint32
	Tracks map[uint32]*TrackPlan
	graph  *model.Graph
}

// BuildPlan applies topo order and turns segment dispositions into kept
// sample lists. It rejects any plan whose track references a failed
// prerequisite, so a downstream track can never be packaged alone.
func BuildPlan(g *model.Graph, dec *repair.Decisions) (*Plan, error) {
	if dec == nil {
		return nil, fmt.Errorf("remux: nil repair decisions")
	}
	p := &Plan{Order: append([]uint32(nil), dec.Order...), Tracks: map[uint32]*TrackPlan{}, graph: g}
	for _, id := range p.Order {
		t := g.Tracks[id]
		if blocked := dec.FailedDependencies[id]; len(blocked) > 0 {
			return nil, fmt.Errorf("remux: track %d blocked by failed prerequisites %v", id, blocked)
		}
		tp := &TrackPlan{
			TrackID:   id,
			Kind:      t.Kind,
			Timescale: t.Timescale,
			Video:     t.Video,
			Audio:     t.Audio,
			HasStss:   t.KeyframeIndexPresent && t.Kind == model.KindVideo,
		}
		keptSet := map[int]bool{}
		for _, seg := range dec.Segments[id] {
			if seg.Status == repair.StatusIsolated {
				continue
			}
			for i := seg.FirstSample; i <= seg.LastSample; i++ {
				keptSet[i] = true
			}
		}
		tp.Kept = sortedKeys(keptSet)
		for rel, srcIdx := range tp.Kept {
			if t.Samples[srcIdx].IsKeyframe {
				tp.Keyframes = append(tp.Keyframes, rel)
			}
		}
		p.Tracks[id] = tp
	}
	return p, nil
}

// KeptCount returns the total number of surviving samples across tracks.
func (p *Plan) KeptCount() int {
	n := 0
	for _, tp := range p.Tracks {
		n += len(tp.Kept)
	}
	return n
}

// TrackFingerprint is the read-back comparison record. Rational timelines
// are compared as relative (re-based) duration sequences so a legitimate
// zero-rebase remux does not look like drift.
type TrackFingerprint struct {
	TrackID      uint32
	SampleCount  int
	Durations    []model.Rat
	Keyframes    []int
	Timescale    uint32
	Kind         model.MediaKind
	TotalDur     model.Rat
}

// Fingerprint builds the comparison record from any (re-parsed) graph.
func Fingerprint(t *model.Track) TrackFingerprint {
	fp := TrackFingerprint{
		TrackID: t.ID, SampleCount: len(t.Samples), Timescale: t.Timescale,
		Kind: t.Kind, Keyframes: []int{}, Durations: make([]model.Rat, 0, len(t.Samples)),
	}
	for i, s := range t.Samples {
		fp.Durations = append(fp.Durations, s.Duration)
		if s.IsKeyframe {
			fp.Keyframes = append(fp.Keyframes, i)
		}
		if i == len(t.Samples)-1 {
			fp.TotalDur = s.DecodeTime.Add(s.Duration).Sub(t.Samples[0].DecodeTime)
		}
	}
	return fp
}

// Equal compares two fingerprints, allowing only declared metadata fields to
// differ. Sample counts, durations and keyframe positions must match exactly.
func (f TrackFingerprint) Equal(o TrackFingerprint) bool {
	if f.TrackID != o.TrackID || f.SampleCount != o.SampleCount ||
		f.Timescale != o.Timescale || f.Kind != o.Kind ||
		len(f.Durations) != len(o.Durations) || len(f.Keyframes) != len(o.Keyframes) {
		return false
	}
	for i := range f.Durations {
		if f.Durations[i].Cmp(o.Durations[i]) != 0 {
			return false
		}
	}
	for i := range f.Keyframes {
		if f.Keyframes[i] != o.Keyframes[i] {
			return false
		}
	}
	return f.TotalDur.Cmp(o.TotalDur) == 0
}

func sortedKeys(m map[int]bool) []int {
	out := make([]int, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Ints(out)
	return out
}
