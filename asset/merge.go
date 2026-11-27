package asset

import "sort"

// MergedInterval is one resolved interval of a track's timeline.
type MergedInterval struct {
	ProgramKey  string   `json:"program_key"`
	TrackID     string   `json:"track_id"`
	Interval    Interval `json:"interval"`
	Selected    Version  `json:"selected"`
	Alternatives []Version `json:"alternatives,omitempty"`
	Conflict    bool     `json:"conflict"`
}

// MergeResult is the deterministic output of mapping all versions.
type MergeResult struct {
	Intervals []MergedInterval `json:"intervals"`
	Issues    []Issue          `json:"issues"`
}

// Merge resolves every track:
//   - intervals without any covering version become ASSET_GAP issues;
//   - overlapping versions with different digests become ASSET_CONFLICT and
//     the highest Priority (then stable source order) selects the source;
//   - identical digests collapse into one interval with alternatives listed.
//
// Output ordering is stable: program, track, interval start.
func Merge(m *Map) MergeResult {
	var res MergeResult
	progKeys := make([]string, 0, len(m.Programs))
	for pk := range m.Programs {
		progKeys = append(progKeys, pk)
	}
	sort.Strings(progKeys)
	for _, pk := range progKeys {
		trackIDs := make([]string, 0, len(m.Programs[pk]))
		for tid := range m.Programs[pk] {
			trackIDs = append(trackIDs, tid)
		}
		sort.Strings(trackIDs)
		for _, tid := range trackIDs {
			mergeTrack(pk, tid, m.Programs[pk][tid], &res)
		}
	}
	sort.SliceStable(res.Issues, func(i, j int) bool {
		if res.Issues[i].ProgramKey != res.Issues[j].ProgramKey {
			return res.Issues[i].ProgramKey < res.Issues[j].ProgramKey
		}
		if res.Issues[i].TrackID != res.Issues[j].TrackID {
			return res.Issues[i].TrackID < res.Issues[j].TrackID
		}
		return res.Issues[i].Interval.StartNS < res.Issues[j].Interval.StartNS
	})
	return res
}

func mergeTrack(pk, tid string, tm *TrackMap, res *MergeResult) {
	vs := sortedVersions(tm.Versions)
	cursor := tm.Expected.StartNS
	for i := 0; i < len(vs); {
		v := vs[i]
		// Gap between the coverage cursor and this version's start.
		if v.Interval.StartNS > cursor {
			res.Issues = append(res.Issues, Issue{
				Code: IssueGap, ProgramKey: pk, TrackID: tid,
				Interval: Interval{StartNS: cursor, EndNS: v.Interval.StartNS},
				Detail:   "no asset version covers interval",
			})
		}
		// Gather every version overlapping the current interval.
		group := []Version{v}
		j := i + 1
		for j < len(vs) && overlap(v.Interval, vs[j].Interval) {
			group = append(group, vs[j])
			j++
		}
		mi := resolveGroup(pk, tid, group)
		res.Intervals = append(res.Intervals, mi)
		if mi.Conflict {
			res.Issues = append(res.Issues, Issue{
				Code: IssueConflict, ProgramKey: pk, TrackID: tid,
				Interval: mi.Interval, Detail: "content digests differ; priority rule selected source",
			})
		}
		end := mi.Interval.EndNS
		if end > cursor {
			cursor = end
		}
		i = j
	}
	if cursor < tm.Expected.EndNS {
		res.Issues = append(res.Issues, Issue{
			Code: IssueGap, ProgramKey: pk, TrackID: tid,
			Interval: Interval{StartNS: cursor, EndNS: tm.Expected.EndNS},
			Detail:   "trailing interval without coverage",
		})
	}
}

func resolveGroup(pk, tid string, group []Version) MergedInterval {
	// group is already start/priority ordered; determine digest agreement.
	digests := map[string]struct{}{}
	for _, v := range group {
		digests[v.Digest] = struct{}{}
	}
	conflict := len(digests) > 1
	// The highest-priority member wins; sortedVersions already put it first
	// among same-start members, but pick explicitly for overlapping groups.
	selected := group[0]
	for _, v := range group[1:] {
		if v.Priority > selected.Priority ||
			(v.Priority == selected.Priority && v.Source < selected.Source) {
			selected = v
		}
	}
	// Union span of the group.
	span := group[0].Interval
	for _, v := range group[1:] {
		if v.Interval.StartNS < span.StartNS {
			span.StartNS = v.Interval.StartNS
		}
		if v.Interval.EndNS > span.EndNS {
			span.EndNS = v.Interval.EndNS
		}
	}
	alts := make([]Version, 0, len(group)-1)
	for _, v := range group {
		if v.Source != selected.Source {
			alts = append(alts, v)
		}
	}
	return MergedInterval{
		ProgramKey: pk, TrackID: tid, Interval: span,
		Selected: selected, Alternatives: alts, Conflict: conflict,
	}
}
