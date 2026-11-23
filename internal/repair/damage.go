// Package repair consumes the probe graph and the shared ErrorSet, maps
// sample-level defects to timeline ranges and adjudicates which ranges can
// be recovered versus must be isolated. No defect is ever silently dropped:
// every output range is backed by an input error with verifiable evidence.
package repair

import (
	"github.com/broadcast/mediad/internal/model"
)

// Segment is one maximal piece of a track with a uniform disposition.
type Segment struct {
	TrackID     uint32       `json:"track_id"`
	FirstSample int          `json:"first_sample"`
	LastSample  int          `json:"last_sample"`
	Coverage    model.Range  `json:"-"`
	Status      Status       `json:"status"`
	Reasons     []model.ErrorKind `json:"reasons,omitempty"`
}

// Status is the repair disposition of a segment.
type Status string

const (
	// StatusValid needs no repair and is copied through.
	StatusValid Status = "valid"
	// StatusRecovered can be re-used after dependency-ordered repair.
	StatusRecovered Status = "recovered"
	// StatusIsolated is unrecoverable and is excluded from remux output.
	StatusIsolated Status = "isolated"
)

// TrackDamage summarizes one track's defective samples and ranges.
type TrackDamage struct {
	TrackID         uint32
	DefectSamples   map[int][]model.ErrorKind
	DamagedRanges   []model.Range
	ValidBefore     *model.Range
	ValidAfter      *model.Range
}

// Locate walks every track's samples and the shared ErrorSet to produce
// per-track damage maps. Samples adjacent in time to a backward/duplicate
// defect inherit isolation, because their decode order cannot be proven.
func Locate(g *model.Graph, errs *model.ErrorSet) map[uint32]*TrackDamage {
	out := make(map[uint32]*TrackDamage, len(g.IDs()))
	for _, id := range g.IDs() {
		t := g.Tracks[id]
		d := &TrackDamage{
			TrackID:       id,
			DefectSamples: make(map[int][]model.ErrorKind),
		}
		collectErrorRanges(d, errs.ByTrack(id), t)
		out[id] = d
	}
	return out
}

// collectErrorRanges folds the ErrorSet for one track into sample-level
// defect markers plus merged timeline ranges.
func collectErrorRanges(d *TrackDamage, trackErrs []model.MediaError, t *model.Track) {
	var damaged []model.Range
	for _, e := range trackErrs {
		if !isStructural(e.Kind) {
			continue
		}
		if e.SampleIndex >= 0 && e.SampleIndex < len(t.Samples) {
			d.DefectSamples[e.SampleIndex] = append(d.DefectSamples[e.SampleIndex], e.Kind)
		}
		damaged = append(damaged, e.Ranges...)
	}
	// Out-of-bounds / truncated table defects may carry no range; mark the
	// tail of the track as damaged so it is never claimed intact.
	for _, e := range trackErrs {
		if e.Kind == model.KindSampleOutOfBounds && e.SampleIndex >= 0 {
			if s := sampleOrNil(t, e.SampleIndex); s != nil {
				rg := model.SafeRange(s.DecodeTime, t.EndTime)
				damaged = append(damaged, rg)
				for i := e.SampleIndex; i < len(t.Samples); i++ {
					d.DefectSamples[i] = append(d.DefectSamples[i], e.Kind)
				}
			}
		}
	}
	d.DamagedRanges = model.JoinRanges(damaged)
	d.ValidBefore, d.ValidAfter = validFlanks(t, d.DamagedRanges)
}

// validFlanks returns the provably clean coverage before the first damaged
// range and after the last one. Either may be nil when no clean flank exists.
func validFlanks(t *model.Track, damaged []model.Range) (*model.Range, *model.Range) {
	if len(damaged) == 0 || len(t.Samples) == 0 {
		return nil, nil
	}
	first := damaged[0].Start
	last := damaged[len(damaged)-1].End
	var before, after *model.Range
	if t.StartTime.Cmp(first) < 0 {
		rg := model.SafeRange(t.StartTime, first)
		before = &rg
	}
	if last.Cmp(t.EndTime) < 0 {
		rg := model.SafeRange(last, t.EndTime)
		after = &rg
	}
	return before, after
}

// isStructural reports whether an error kind invalidates sample ordering or
// payload reachability. Metadata conflicts are policy warnings, not damage.
func isStructural(k model.ErrorKind) bool {
	switch k {
	case model.KindTimestampBackward, model.KindTimestampDuplicate,
		model.KindTimestampGap, model.KindSampleOutOfBounds,
		model.KindTruncatedBox, model.KindBadBoxLength:
		return true
	default:
		return false
	}
}

func sampleOrNil(t *model.Track, idx int) *model.Sample {
	if idx < 0 || idx >= len(t.Samples) {
		return nil
	}
	return t.Samples[idx]
}

// DamagedRangesFor exposes the merged ranges for reports in track order.
func DamagedRangesFor(damage map[uint32]*TrackDamage, order []uint32) []model.Range {
	var out []model.Range
	for _, id := range order {
		out = append(out, damage[id].DamagedRanges...)
	}
	return model.JoinRanges(out)
}
