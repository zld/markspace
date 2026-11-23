// Package fanin merges the representation layers back onto the shared clock
// and validates switch boundaries.
//
// A switch boundary is a point where the consumer moves from one
// representation to another. Because both representations have been repaired
// onto the same clock, the merge can cross-check that the two sides agree on
// the switch instant. A boundary that disagrees beyond the tolerance is
// rejected — the merge stays on the outgoing representation — and the
// rejection is reported with the track, both representations and the time.
package fanin

import (
	"sort"

	"mediad/internal/media"
	"mediad/internal/merr"
)

// key identifies one output instant.
type key struct {
	track media.TrackID
	time  int64
}

// Merge combines per-representation streams already repaired onto the shared
// clock.
//
// Blocks are emitted in (time, track) order. When several representations
// carry the same instant, the active representation for that track supplies
// it; planned switches are applied as their time is reached and each is
// cross-checked before the merge moves onto the new representation.
func Merge(streams map[string][]media.Block, ladderOrder []string, opts Options) (Result, error) {
	opts = opts.withDefaults()
	if len(streams) == 0 {
		return Result{}, merr.New("fanin.merge", merr.CodeBadInput, -1, "no representation streams")
	}
	order := ladderOrder
	if len(order) == 0 {
		order = make([]string, 0, len(streams))
		for name := range streams {
			order = append(order, name)
		}
		sort.Strings(order)
	}
	for _, name := range order {
		if _, ok := streams[name]; !ok {
			return Result{}, merr.New("fanin.merge", merr.CodeBadInput, -1,
				"ladder order names unknown representation %q", name)
		}
		if err := checkOrdered(streams[name]); err != nil {
			return Result{}, merr.Wrap(err, "fanin.merge", merr.CodeBadInput, -1)
		}
	}

	plans := make(map[media.TrackID][]Switch)
	for _, s := range opts.Plan {
		if _, ok := streams[s.ToRep]; !ok {
			return Result{}, merr.New("fanin.merge", merr.CodeBadInput, -1,
				"switch plan names unknown representation %q", s.ToRep)
		}
		plans[s.Track] = append(plans[s.Track], s)
	}
	for tid := range plans {
		sort.SliceStable(plans[tid], func(i, j int) bool { return plans[tid][i].AtNs < plans[tid][j].AtNs })
	}

	// Default representation per track: the first in ladder order carrying it.
	base := make(map[media.TrackID]string)
	for _, name := range order {
		for _, b := range streams[name] {
			if _, ok := base[b.Track]; !ok {
				base[b.Track] = name
			}
		}
	}

	// Every distinct (track, time) across representations, indexed by rep.
	index := make(map[key]map[string]media.Block)
	var keys []key
	for _, name := range order {
		for _, b := range streams[name] {
			k := key{b.Track, b.TimeNs}
			if index[k] == nil {
				index[k] = make(map[string]media.Block)
				keys = append(keys, k)
			}
			index[k][name] = b
		}
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].time != keys[j].time {
			return keys[i].time < keys[j].time
		}
		return keys[i].track < keys[j].track
	})

	res := Result{Blocks: make([]media.Block, 0, len(keys))}
	tracks := make([]media.TrackID, 0, len(base))
	for tid := range base {
		tracks = append(tracks, tid)
	}
	sort.Slice(tracks, func(i, j int) bool { return tracks[i] < tracks[j] })

	active := make(map[media.TrackID]string, len(base))
	for _, tid := range tracks {
		active[tid] = base[tid]
	}
	planIdx := make(map[media.TrackID]int, len(plans))
	lastTime := make(map[media.TrackID]int64)
	haveLast := make(map[media.TrackID]bool)

	for _, k := range keys {
		tid := k.track
		for planIdx[tid] < len(plans[tid]) && plans[tid][planIdx[tid]].AtNs <= k.time {
			sw := plans[tid][planIdx[tid]]
			planIdx[tid]++
			if sw.ToRep == active[tid] {
				continue
			}
			applySwitch(&res, streams, active, tid, sw, k.time, opts)
		}
		rep := active[tid]
		b, ok := index[k][rep]
		if !ok {
			// The active representation lacks this instant: fall back to the
			// first representation in ladder order that has it.
			res.Missing++
			for _, name := range order {
				if cand, ok := index[k][name]; ok {
					b = cand
					rep = name
					break
				}
			}
		}
		cp := b
		cp.Rep = rep
		if haveLast[tid] {
			if gap := cp.TimeNs - lastTime[tid]; gap > opts.MaxGapNs {
				res.Missing++
			}
		}
		lastTime[tid] = cp.TimeNs
		haveLast[tid] = true
		res.Blocks = append(res.Blocks, cp)
	}
	res.Dropouts, res.MaxDropoutMs = countDropouts(res.Blocks)
	return res, nil
}

// countDropouts reports cadence gaps in the merged stream.
//
// A missing block anywhere upstream — dropped at transport, lost at parse —
// leaves a hole that is wider than the track's usual step. The median step is
// used as the reference so the check needs no knowledge of container
// metadata, and gaps over 1.5 steps are counted as dropouts.
func countDropouts(blocks []media.Block) (int, float64) {
	byTrack := make(map[media.TrackID][]int64)
	for _, b := range blocks {
		byTrack[b.Track] = append(byTrack[b.Track], b.TimeNs)
	}
	total := 0
	var worst int64
	for _, times := range byTrack {
		if len(times) < 3 {
			continue
		}
		deltas := make([]int64, 0, len(times)-1)
		for i := 1; i < len(times); i++ {
			if d := times[i] - times[i-1]; d > 0 {
				deltas = append(deltas, d)
			}
		}
		if len(deltas) == 0 {
			continue
		}
		sorted := append([]int64(nil), deltas...)
		sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
		step := sorted[len(sorted)/2]
		if step <= 0 {
			continue
		}
		for _, d := range deltas {
			if d*2 > step*3 { // more than 1.5 nominal steps
				total++
				if d > worst {
					worst = d
				}
			}
		}
	}
	return total, float64(worst) / 1e6
}
