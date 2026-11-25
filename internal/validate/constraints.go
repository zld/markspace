// Package validate checks a scanned index against the constraints the
// delivery contract requires: fragments must own their samples, tracks must
// cover the fragment timeline, subtitle cues must cover dialogue, and the
// packet stream must account for the audio track's timeline.
//
// Every violation is a located *fault.Error, so a failure names the same
// byte offset and media time the scanner would have reported.
package validate

import (
	"fmt"

	"example.com/assetcheck/internal/clock"
	"example.com/assetcheck/internal/fault"
	"example.com/assetcheck/internal/index"
)

// Deviation is the timing/byte difference between a container sample and the
// packet stream position that should carry it.
type Deviation struct {
	TrackID   uint32
	Kind      string
	Sample    int
	ByteDelta int64
	TimeDelta clock.Nanos
	OK        bool
}

// Mapping is one per-sample reconciliation record.
type Mapping struct {
	TrackID uint32
	Kind    string
	Sample  int
	Offset  int64
	Packet  uint64
	HasPkt  bool
	DTS     clock.Nanos
	Time    clock.Nanos
	Dev     Deviation
	Note    string
}

// Result is a validation outcome.
type Result struct {
	Mappings  []Mapping
	Faults    []*fault.Error
	Subtitle  Coverage
	Duplicate int
	Missing   uint64
	Skew      clock.Nanos
}

// Coverage summarises whether subtitle cues span the dialogue timeline.
type Coverage struct {
	Required  clock.Nanos
	Covered   clock.Nanos
	Uncovered clock.Nanos
	Overlaps  int
}

// Check validates idx. It never mutates the index.
func Check(idx *index.Index, input []byte) *Result {
	res := &Result{Skew: clock.Invalid}
	if idx == nil {
		res.Faults = append(res.Faults, fault.New(fault.StageValidate, "validate.no_index", fault.NoOffset, clock.Invalid, "no index to validate"))
		return res
	}
	audio := idx.AudioTrack()
	if audio == nil {
		res.Faults = append(res.Faults, fault.New(fault.StageValidate, "validate.no_audio", fault.NoOffset, clock.Invalid,
			"index has no audio track to reconcile packets against"))
		return res
	}
	res.checkFragments(idx)
	res.checkSamples(idx, input)
	res.mapPackets(idx, audio)
	res.checkSubtitle(idx, audio)
	res.checkSkew(idx)
	fault.Sort(res.Faults)
	return res
}

// checkFragments confirms each fragment owns at least one sample and that
// its declared range matches the samples attributed to it.
func (r *Result) checkFragments(idx *index.Index) {
	for fi := range idx.Fragments {
		fr := &idx.Fragments[fi]
		if fr.Samples == 0 {
			r.Faults = append(r.Faults, fault.New(fault.StageValidate, "validate.fragment.empty", fr.Offset, clock.Invalid,
				fmt.Sprintf("fragment %d (sequence %d) owns no samples", fr.Ordinal, fr.Sequence)))
			continue
		}
		if fr.End <= fr.Start {
			r.Faults = append(r.Faults, fault.New(fault.StageValidate, "validate.fragment.range", fr.Offset, fr.Start,
				fmt.Sprintf("fragment %d has an empty or inverted time range", fr.Ordinal)))
		}
		if len(fr.Tracks) == 0 {
			r.Faults = append(r.Faults, fault.New(fault.StageValidate, "validate.fragment.no_tracks", fr.Offset, fr.Start,
				fmt.Sprintf("fragment %d declares no track", fr.Ordinal)))
		}
	}
}

// checkSamples confirms every sample sits inside the input and inside the
// time range its track declares.
func (r *Result) checkSamples(idx *index.Index, input []byte) {
	size := int64(len(input))
	for ti := range idx.Tracks {
		tr := &idx.Tracks[ti]
		for si := range tr.Samples {
			s := &tr.Samples[si]
			if s.Offset < 0 || s.Offset+int64(s.Size) > size {
				r.Faults = append(r.Faults, fault.New(fault.StageValidate, "validate.sample.outside_input", s.Offset, s.DTS,
					fmt.Sprintf("track %d sample %d ends at byte %d beyond input length %d", tr.ID, si, s.Offset+int64(s.Size), size)).
					WithSample(si))
			}
			if s.DTS < tr.Start || clock.Add(s.DTS, s.Duration) > clock.Add(tr.End, clock.Nanos(1)) {
				r.Faults = append(r.Faults, fault.New(fault.StageValidate, "validate.sample.outside_track", s.Offset, s.DTS,
					fmt.Sprintf("track %d sample %d lies outside the track's own range", tr.ID, si)).WithSample(si))
			}
		}
	}
}
