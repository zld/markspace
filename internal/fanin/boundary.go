// Package fanin merges the representation layers back onto the shared clock
// and validates switch boundaries. This file performs the boundary
// cross-check itself.
package fanin

import (
	"sort"

	"mediad/internal/media"
	"mediad/internal/merr"
)

// applySwitch cross-checks a boundary and, when it passes, moves the track
// onto the new representation. A rejected switch leaves the active
// representation unchanged.
func applySwitch(res *Result, streams map[string][]media.Block, active map[media.TrackID]string,
	tid media.TrackID, sw Switch, atNs int64, opts Options) {
	prev := active[tid]
	res.SwitchCount++
	from := blockAt(streams[prev], tid, atNs)
	to := blockAt(streams[sw.ToRep], tid, atNs)
	if from == nil || to == nil {
		dev := opts.ToleranceNs + 1
		if from != nil {
			dev = abs64(from.TimeNs - atNs)
		}
		res.Mismatches = append(res.Mismatches, Mismatch{
			Track: tid, FromRep: prev, ToRep: sw.ToRep, TimeNs: atNs,
			DeviationNs: dev, Reason: "missing",
		})
		return
	}
	dev := abs64(from.TimeNs - to.TimeNs)
	if dev > opts.ToleranceNs {
		res.Mismatches = append(res.Mismatches, Mismatch{
			Track: tid, FromRep: prev, ToRep: sw.ToRep, TimeNs: atNs,
			DeviationNs: dev, Reason: "deviation",
		})
		return
	}
	if dev > res.MaxBoundaryDeviationNs {
		res.MaxBoundaryDeviationNs = dev
	}
	res.Boundaries = append(res.Boundaries, Boundary{
		Track: tid, TimeNs: atNs, FromRep: prev, ToRep: sw.ToRep,
		FromTime: from.TimeNs, ToTime: to.TimeNs, DeviationNs: dev,
	})
	active[tid] = sw.ToRep
}

// blockAt finds the representation's block nearest a switch instant.
//
// After repair both representations carry the same shared-clock timestamps,
// so a coherent boundary has matching blocks on both sides. The nearest block
// is the one the representation could actually hand over at that instant;
// comparing against a distant neighbour is what surfaces a missing or
// mis-timed block. Streams are sorted by (track, time), so a binary search
// locates the insertion point and only its neighbours need checking.
func blockAt(stream []media.Block, track media.TrackID, timeNs int64) *media.Block {
	lo, hi := 0, len(stream)
	for lo < hi {
		mid := (lo + hi) / 2
		b := stream[mid]
		if b.Track < track || (b.Track == track && b.TimeNs < timeNs) {
			lo = mid + 1
		} else {
			hi = mid
		}
	}
	var best *media.Block
	var bestDev int64
	consider := func(i int) {
		if i < 0 || i >= len(stream) || stream[i].Track != track {
			return
		}
		b := &stream[i]
		dev := abs64(b.TimeNs - timeNs)
		if best == nil || dev < bestDev {
			best, bestDev = b, dev
		}
	}
	consider(lo)
	consider(lo - 1)
	return best
}

// checkOrdered verifies a stream is sorted by (track, time) and strictly
// increasing within each track.
func checkOrdered(stream []media.Block) error {
	var lastTrack media.TrackID
	var lastTime int64
	have := false
	for i, b := range stream {
		if have && b.Track == lastTrack && b.TimeNs <= lastTime {
			return merr.New("fanin.ordered", merr.CodeBadInput, int64(i),
				"track %d time %d not after %d", b.Track, b.TimeNs, lastTime)
		}
		lastTrack, lastTime, have = b.Track, b.TimeNs, true
	}
	return nil
}

func abs64(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}

// SortStream returns a stream sorted by (track, time), stable on input order.
func SortStream(blocks []media.Block) []media.Block {
	out := append([]media.Block(nil), blocks...)
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Track != out[j].Track {
			return out[i].Track < out[j].Track
		}
		return out[i].TimeNs < out[j].TimeNs
	})
	return out
}
