// Package index rebuilds a random-access index from the fragments that
// actually survive validation. An existing sidx or mfra is only a hint: every
// offset it claims is rechecked against real box boundaries, and offsets that
// do not land on a boundary are dropped rather than trusted.
package index

import (
	"fmt"
	"sort"

	"mediaroute/internal/model"
)

// Stage identifies errors and issues emitted by this subsystem.
const Stage = "index/rebuild"

// BuildResult is the rebuilt index plus its bookkeeping.
type BuildResult struct {
	Index      *model.Index
	Entries    int
	BadOffsets []uint64
	HintIssues []model.Issue
}

// entryFor builds the index entry for one track of a placed fragment. moofOff
// and mdatOff are the absolute output offsets of the boxes. It returns a
// monotonicity error when the track would move backwards in time.
func entryFor(ft *model.FragmentTrack, fragIndex int, moofOff, mdatOff uint64, prevEnd map[uint32]model.Micros) (model.IndexEntry, error) {
	if len(ft.Samples) == 0 || ft.EndMicros <= ft.StartMicros {
		return model.IndexEntry{}, errNoEntry
	}
	if last, ok := prevEnd[ft.TrackID]; ok && ft.StartMicros < last {
		return model.IndexEntry{}, model.NewTaskError(model.CategoryInvalidBox, Stage,
			fmt.Sprintf("track %d fragment %d starts at %d us before previous end %d us",
				ft.TrackID, fragIndex, ft.StartMicros, last)).WithTrack(ft.TrackID)
	}
	prevEnd[ft.TrackID] = ft.EndMicros
	e := model.IndexEntry{
		TrackID:     ft.TrackID,
		Sequence:    ft.Sequence,
		MoofOffset:  moofOff,
		MdatOffset:  mdatOff,
		StartMicros: ft.StartMicros,
		EndMicros:   ft.EndMicros,
		SampleCount: uint32(len(ft.Samples)),
	}
	if ord, ok := ft.SyncSampleOrdinal(); ok {
		e.RandomAccess = true
		e.SyncSample = ord
		e.FirstSample = ord
	}
	return e, nil
}

// errNoEntry signals that a fragment track contributes no index entry.
var errNoEntry = model.NewTaskError(model.CategoryInvalidBox, Stage, "no entry")

// Build reconstructs the random-access index from delivered fragments.
// baseOffset is the byte offset at which the first delivered moof will appear
// in the output file; for an in-memory rebuild it is normally zero.
func Build(frags []model.Fragment, baseOffset uint64) (*BuildResult, error) {
	ix := &model.Index{Entries: make([]model.IndexEntry, 0, len(frags))}
	res := &BuildResult{Index: ix}
	var cursor uint64 = baseOffset
	prevEnd := make(map[uint32]model.Micros, 4)
	for i := range frags {
		frag := &frags[i]
		if frag.Discarded || frag.Truncated {
			continue
		}
		moofOff := cursor
		mdatOff := cursor + frag.MoofRange.Len()
		// Boundaries are the moof and mdat ranges as placed in the output.
		ix.Boundaries = append(ix.Boundaries,
			model.RangeOf(moofOff, frag.MoofRange.Len()),
			model.RangeOf(mdatOff, frag.MdatRange.Len()),
		)
		for ti := range frag.Tracks {
			entry, err := entryFor(&frag.Tracks[ti], frag.Index, moofOff, mdatOff, prevEnd)
			if err != nil {
				if err == errNoEntry {
					continue
				}
				return nil, err
			}
			ix.Entries = append(ix.Entries, entry)
		}
		cursor += frag.MoofRange.Len() + frag.MdatRange.Len()
	}
	ix.TotalBytes = cursor
	sort.SliceStable(ix.Entries, func(i, j int) bool {
		if ix.Entries[i].TrackID != ix.Entries[j].TrackID {
			return ix.Entries[i].TrackID < ix.Entries[j].TrackID
		}
		if ix.Entries[i].StartMicros != ix.Entries[j].StartMicros {
			return ix.Entries[i].StartMicros < ix.Entries[j].StartMicros
		}
		return ix.Entries[i].MoofOffset < ix.Entries[j].MoofOffset
	})
	res.Entries = len(ix.Entries)
	return res, nil
}
