package timeline

import (
	"sort"

	"medfix/internal/clock"
)

// conflictSlot groups one track's metadata events sharing a content hash.
type conflictSlot struct {
	track string
	evs   []Event
}

// markCrossTrackConflicts pairs metadata events across tracks whose content
// hashes agree; pairs farther apart than tolerance are marked conflicting.
// All iteration is order-deterministic so the conflict list (and every
// downstream index hash) is stable across runs.
func (a *Aligner) markCrossTrackConflicts() {
	trackIDs := make([]string, 0, len(a.byTrack))
	for id := range a.byTrack {
		trackIDs = append(trackIDs, id)
	}
	sort.Strings(trackIDs)

	byHash := map[string][]conflictSlot{}
	hashSet := map[string]bool{}
	var hashOrder []string
	for _, id := range trackIDs {
		groups := map[string][]Event{}
		var localHashes []string
		for _, ev := range a.byTrack[id] {
			if ev.Kind == KindSample {
				continue
			}
			if groups[ev.ContentHash] == nil {
				localHashes = append(localHashes, ev.ContentHash)
			}
			groups[ev.ContentHash] = append(groups[ev.ContentHash], ev)
		}
		for _, h := range localHashes {
			byHash[h] = append(byHash[h], conflictSlot{track: id, evs: groups[h]})
			if !hashSet[h] {
				hashSet[h] = true
				hashOrder = append(hashOrder, h)
			}
		}
	}
	sort.Strings(hashOrder)

	// Conflict identities: trackID -> set of event Seqs to mark.
	mark := map[string]map[int]bool{}
	for _, h := range hashOrder {
		slots := byHash[h]
		if len(slots) < 2 {
			continue
		}
		for i := 0; i < len(slots); i++ {
			for j := i + 1; j < len(slots); j++ {
				// Compare in original timescale units: per-track anchor
				// offsets must not hide real divergence.
				ri := slots[i].evs[0].RawTS
				rj := slots[j].evs[0].RawTS
				d := ri - rj
				if d < 0 {
					d = -d
				}
				tickDist := clock.ToTicks(d, a.scales[slots[i].track])
				if tickDist > a.cfg.ToleranceTicks {
					// One structured error per offending track so every
					// source offset stays locatable.
					for _, s := range []conflictSlot{slots[i], slots[j]} {
						if mark[s.track] == nil {
							mark[s.track] = map[int]bool{}
						}
						for _, ev := range s.evs {
							mark[s.track][ev.Seq] = true
						}
						a.conflicts = append(a.conflicts, *NewError(
							CodeTimescaleConflict, "transform",
							s.track, s.evs[0].Offset.Begin,
							"cross-track event beyond tolerance", nil))
					}
				}
			}
		}
	}
	markIDs := make([]string, 0, len(mark))
	for id := range mark {
		markIDs = append(markIDs, id)
	}
	sort.Strings(markIDs)
	for _, trackID := range markIDs {
		seqs := mark[trackID]
		evs := a.byTrack[trackID]
		for i := range evs {
			if seqs[evs[i].Seq] {
				evs[i].Conflict = true
			}
		}
		a.byTrack[trackID] = evs
	}
}
