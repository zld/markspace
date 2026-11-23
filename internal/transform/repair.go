// Package transform repairs fragment sequence defects deterministically and
// derives a reproducible offscreen frame sequence.
package transform

import (
	"fmt"

	"mediacore/internal/fault"
	"mediacore/internal/timeline"
)

// RepairOptions configures the deterministic repair strategy. Only in-order
// repair is supported: input order is the authority for ordering, and original
// source times are always preserved.
type RepairOptions struct {
	// TimelineTimescale, when non-zero, is enforced as the single timeline
	// timescale. A track mapped to any other timescale cannot be merged
	// without rescaling after repair, so it is rejected instead.
	TimelineTimescale uint32
}

// Discontinuity is a fragment-level defect found while repairing.
type Discontinuity struct {
	Kind          string `json:"kind"` // "sequence_gap", "duplicate", "tfdt_regression"
	TrackID       uint32 `json:"track_id"`
	FragmentIndex int    `json:"fragment_index"`
	Sequence      uint32 `json:"sequence"`
	PreviousSeq   uint32 `json:"previous_seq"`
	File          string `json:"file"`
	Offset        int64  `json:"offset"`
	MappedTime    int64  `json:"mapped_time"`
	PreviousEnd   int64  `json:"previous_end"`
	Repaired      bool   `json:"repaired"`
	Detail        string `json:"detail,omitempty"`
}

// RepairedTrack is one track with monotonically non-decreasing synthesized
// times. Dropped duplicates are removed from Samples but retained in
// Discontinuities with their original offsets.
type RepairedTrack struct {
	ID              uint32            `json:"track_id"`
	Kind            string            `json:"kind"`
	Mergeable       bool              `json:"mergeable"`
	Reason          string            `json:"reason,omitempty"`
	Samples         []timeline.Sample `json:"samples"`
	Duplicates      []timeline.Sample `json:"duplicates,omitempty"`
	Discontinuities []Discontinuity   `json:"discontinuities,omitempty"`
	Errors          []*fault.Error    `json:"-"`
}

// RepairReport summarizes all decisions.
type RepairReport struct {
	SequenceGaps      int             `json:"sequence_gaps"`
	Duplicates        int             `json:"duplicates"`
	Regressions       int             `json:"regressions"`
	RepairedSamples   int             `json:"repaired_samples"`
	UnmergeableTracks []uint32        `json:"unmergeable_tracks,omitempty"`
	Discontinuities   []Discontinuity `json:"discontinuities,omitempty"`
	Errors            []*fault.Error  `json:"-"`
}

// Repair applies three deterministic rules per track:
//
//  1. A fragment whose sequence, tfdt and sample declaration exactly repeat
//     the immediately preceding fragment is a duplicate; the first appearance
//     is kept and the repeat is dropped.
//  2. A sequence gap (increasing mfhd with a missing number) is recorded. Time
//     is not altered because tfdt remains authoritative.
//  3. A tfdt regression (mapped start earlier than the previous fragment's
//     end) is repaired by placing the fragment at that end; every sample keeps
//     its original source time.
//
// A repeat that contradicts its predecessor (same or earlier sequence with a
// different declaration) cannot be ordered without guessing, so the track is
// marked unmergeable with the offending offset retained.
func Repair(tracks []*timeline.Track, opts RepairOptions) (*RepairReport, []*RepairedTrack) {
	report := &RepairReport{}
	out := make([]*RepairedTrack, 0, len(tracks))
	for _, tr := range tracks {
		rt := repairTrack(tr, opts, report)
		out = append(out, rt)
	}
	return report, out
}

func firstFragmentFile(tr *timeline.Track) string {
	if len(tr.Samples) == 0 {
		return ""
	}
	return tr.Samples[0].File
}

func firstFragmentOffset(tr *timeline.Track) int64 {
	if len(tr.Samples) == 0 {
		return 0
	}
	return tr.Samples[0].FragmentOffset
}

// fragmentGroup holds the samples of one fragment in input order.
type fragmentGroup struct {
	index      int
	seq        uint32
	file       string
	offset     int64
	tfdtOffset int64
	samples    []timeline.Sample
}

func groupFragments(samples []timeline.Sample) []fragmentGroup {
	var groups []fragmentGroup
	for _, s := range samples {
		if n := len(groups); n > 0 && groups[n-1].index == s.FragmentIndex {
			groups[n-1].samples = append(groups[n-1].samples, s)
			continue
		}
		groups = append(groups, fragmentGroup{
			index:      s.FragmentIndex,
			seq:        s.FragmentSeq,
			file:       s.File,
			offset:     s.FragmentOffset,
			tfdtOffset: s.TfdtOffset,
			samples:    []timeline.Sample{s},
		})
	}
	return groups
}

func fragmentSignature(g fragmentGroup) string {
	sig := fmt.Sprintf("%d|%d|%d", g.seq, g.samples[0].SourceTime, len(g.samples))
	for _, s := range g.samples {
		sig += fmt.Sprintf("|%d:%d", s.SourceDuration, s.ByteLength)
	}
	return sig
}

func repairTrack(tr *timeline.Track, opts RepairOptions, report *RepairReport) *RepairedTrack {
	rt := &RepairedTrack{ID: tr.ID, Kind: tr.Kind, Mergeable: tr.Mergeable, Reason: tr.Reason}
	if opts.TimelineTimescale != 0 && tr.Mapping.TimelineTimescale != opts.TimelineTimescale {
		f := fault.New(fault.CodeUnmergeableTrack, firstFragmentFile(tr), firstFragmentOffset(tr), "",
			fmt.Sprintf("track %d is mapped to timescale %d, not the configured %d",
				tr.ID, tr.Mapping.TimelineTimescale, opts.TimelineTimescale))
		rt.Mergeable = false
		rt.Reason = f.Error()
		rt.Errors = append(rt.Errors, f)
		report.Errors = append(report.Errors, f)
		report.UnmergeableTracks = append(report.UnmergeableTracks, tr.ID)
		return rt
	}
	if len(tr.Samples) == 0 {
		rt.Samples = nil
		return rt
	}
	groups := groupFragments(tr.Samples)
	var (
		cursor  int64
		prevEnd int64
		prevSeq uint32
		prevSig string
		first   = true
	)
	for _, g := range groups {
		sig := fragmentSignature(g)
		if !first && sig == prevSig {
			// Exact repeat: drop, but keep provenance of the dropped bytes.
			report.Duplicates++
			rt.Duplicates = append(rt.Duplicates, g.samples...)
			d := Discontinuity{
				Kind: "duplicate", TrackID: tr.ID, FragmentIndex: g.index, Sequence: g.seq,
				PreviousSeq: prevSeq, File: g.file, Offset: g.offset,
				MappedTime: g.samples[0].TimelineTime, PreviousEnd: prevEnd,
				Repaired: true, Detail: "duplicate fragment dropped; first appearance kept",
			}
			rt.Discontinuities = append(rt.Discontinuities, d)
			report.Discontinuities = append(report.Discontinuities, d)
			continue
		}
		if !first && g.seq <= prevSeq {
			// Cannot order this fragment without guessing.
			f := fault.New(fault.CodeUnmergeableTrack, g.file, g.tfdtOffset, "mfhd",
				fmt.Sprintf("fragment %d has sequence %d after %d with a different declaration", g.index, g.seq, prevSeq)).WithFragment(g.seq)
			rt.Mergeable = false
			rt.Reason = f.Error()
			rt.Errors = append(rt.Errors, f)
			report.Errors = append(report.Errors, f)
			report.UnmergeableTracks = append(report.UnmergeableTracks, tr.ID)
			d := Discontinuity{
				Kind: "unorderable", TrackID: tr.ID, FragmentIndex: g.index, Sequence: g.seq,
				PreviousSeq: prevSeq, File: g.file, Offset: g.tfdtOffset,
				MappedTime: g.samples[0].TimelineTime, PreviousEnd: prevEnd, Repaired: false,
				Detail: f.Error(),
			}
			rt.Discontinuities = append(rt.Discontinuities, d)
			report.Discontinuities = append(report.Discontinuities, d)
			return rt
		}
		mappedStart := g.samples[0].TimelineTime
		switch {
		case first:
			cursor = mappedStart
		case g.seq > prevSeq+1:
			// mfhd gap. tfdt is still authoritative, so only record it.
			report.SequenceGaps++
			// The first byte that reveals a skipped decode time is the tfdt
			// of the fragment following the missing one; that is what a
			// caller can correlate with the input.
			d := Discontinuity{
				Kind: "sequence_gap", TrackID: tr.ID, FragmentIndex: g.index, Sequence: g.seq,
				PreviousSeq: prevSeq, File: g.file, Offset: g.tfdtOffset,
				MappedTime: mappedStart, PreviousEnd: prevEnd, Repaired: false,
				Detail: fmt.Sprintf("mfhd jumped from %d to %d", prevSeq, g.seq),
			}
			rt.Discontinuities = append(rt.Discontinuities, d)
			report.Discontinuities = append(report.Discontinuities, d)
			cursor = mappedStart
		default:
			cursor = mappedStart
		}
		if !first && mappedStart < prevEnd {
			report.Regressions++
			d := Discontinuity{
				Kind: "tfdt_regression", TrackID: tr.ID, FragmentIndex: g.index, Sequence: g.seq,
				PreviousSeq: prevSeq, File: g.file, Offset: g.tfdtOffset,
				MappedTime: mappedStart, PreviousEnd: prevEnd, Repaired: true,
				Detail: fmt.Sprintf("tfdt mapped to %d before previous end %d; continued at end", mappedStart, prevEnd),
			}
			rt.Discontinuities = append(rt.Discontinuities, d)
			report.Discontinuities = append(report.Discontinuities, d)
			cursor = prevEnd
		}
		for _, s := range g.samples {
			if s.TimelineTime != cursor {
				s.Repaired = true
				s.Repair = "monotonic continuation"
				report.RepairedSamples++
			}
			s.TimelineTime = cursor
			cursor += s.TimelineDuration
			rt.Samples = append(rt.Samples, s)
		}
		prevEnd = cursor
		prevSeq = g.seq
		prevSig = sig
		first = false
	}
	return rt
}
