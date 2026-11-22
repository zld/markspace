package index

import (
	"errors"
	"sort"

	"github.com/pairwise/mediad/internal/fault"
	"github.com/pairwise/mediad/internal/mp4"
)

// Repair extends a track index past a damaged or truncated sync table by
// scanning the verified sample table. Entries recovered this way are marked
// inferred; declared sync samples keep their exact flag. The distinction is
// what lets verify report how many cuts truly landed on a declared sync frame.
func Repair(ix *Index, movie *mp4.Movie, pres Presentation) error {
	var problems []error
	for _, ti := range ix.Tracks {
		tr := trackByID(movie, ti.TrackID)
		if tr == nil || (tr.IndexComplete && tr.SyncTablePresent && !tailShort(ti, tr)) {
			continue
		}
		added, err := extend(ti, tr, pres)
		if err != nil {
			problems = append(problems, err)
			continue
		}
		if added > 0 {
			ti.Rebuilt = true
			ix.Rebuilt = true
			ix.Warnings = append(ix.Warnings, fault.Errorf("index.repair", fault.KindIndex,
				"track %d: scanned %d readable samples and added %d inferred sync points past sample %d",
				tr.ID, len(tr.Samples), added, lastSample(ti)).
				WithPath(movie.Path).WithTrack(tr.ID).Recovered().Wrapped(fault.ErrIncomplete))
		}
	}
	if len(problems) > 0 {
		return fault.New("index.repair", fault.KindIndex, errorsJoin(problems)).WithPath(movie.Path).Recovered()
	}
	return nil
}

func trackByID(m *mp4.Movie, id uint32) *mp4.Track {
	for _, t := range m.Tracks {
		if t.ID == id {
			return t
		}
	}
	return nil
}

func lastSample(ti *TrackIndex) int {
	if len(ti.Entries) == 0 {
		return -1
	}
	return ti.Entries[len(ti.Entries)-1].Sample
}

func tailShort(ti *TrackIndex, tr *mp4.Track) bool {
	n := len(tr.Samples)
	if n == 0 {
		return false
	}
	last := lastSample(ti)
	if last < 0 {
		return true
	}
	// More than two observed GOP intervals left unindexed.
	if len(ti.Entries) < 2 {
		return last < n-1
	}
	first := ti.Entries[0].Sample
	interval := (last - first) / (len(ti.Entries) - 1)
	if interval < 1 {
		interval = 1
	}
	return n-1-last > 2*interval
}

// extend appends inferred entries at verified chunk starts beyond the last
// indexed sample. Chunk starts are the strongest evidence of independent
// decodability available without parsing codec bitstreams.
func extend(ti *TrackIndex, tr *mp4.Track, pres Presentation) (int, error) {
	if len(tr.Samples) == 0 {
		return 0, nil
	}
	starts := make(map[int64]struct{}, len(tr.ChunkOffsets))
	for _, off := range tr.ChunkOffsets {
		starts[off] = struct{}{}
	}
	last := lastSample(ti)
	// Space inferred landings by one observed GOP interval (or a default)
	// rather than accepting every chunk start: a file with one sample per
	// chunk would otherwise gain an entry per frame and turn the index into a
	// sample list with no editorial meaning.
	spacing := gopSpacing(ti)
	var added []Entry
	nextAllowed := last + spacing
	for i := last + 1; i < len(tr.Samples); i++ {
		s := tr.Samples[i]
		if _, ok := starts[s.Offset]; !ok || i < nextAllowed {
			continue
		}
		presented, ok := pres.At(tr.ID, i)
		if !ok {
			continue
		}
		added = append(added, Entry{
			TrackID: tr.ID, Sample: i, DecodeTicks: s.DTS, Offset: s.Offset,
			Presented: presented, Exact: false,
		})
		nextAllowed = i + spacing
	}
	if len(added) == 0 {
		return 0, nil
	}
	ti.Entries = append(ti.Entries, added...)
	sort.SliceStable(ti.Entries, func(a, b int) bool { return ti.Entries[a].Presented < ti.Entries[b].Presented })
	ti.Inferred += len(added)
	ti.Duration = ti.Entries[len(ti.Entries)-1].Presented
	return len(added), nil
}

// gopSpacing estimates the declared group-of-pictures interval. Defaulting to
// twelve frames keeps repaired landings on editorially useful boundaries.
func gopSpacing(ti *TrackIndex) int {
	if len(ti.Entries) < 2 {
		return 12
	}
	first := ti.Entries[0].Sample
	last := ti.Entries[len(ti.Entries)-1].Sample
	spacing := (last - first) / (len(ti.Entries) - 1)
	if spacing < 1 {
		return 12
	}
	return spacing
}

func errorsJoin(errs []error) error {
	if len(errs) == 0 {
		return nil
	}
	if len(errs) == 1 {
		return errs[0]
	}
	return errors.Join(errs...)
}
