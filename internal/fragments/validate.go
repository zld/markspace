// Package fragments validates the timing and ordering contract for media
// fragments: boundaries on valid instants, cross-track deviation under 1 ms,
// monotonic and non-duplicated sequence numbers, and interleaved blocks kept in
// arrival order.
package fragments

import (
	"fmt"
	"sort"

	"example.com/assetcheck/internal/media"
	"example.com/assetcheck/internal/timeline"
)

// ElementBlock is the Matroska SimpleBlock ID. Fragment problems point here.
const ElementBlock uint32 = 0xA3

// Problem is one fragment-level failure with the earliest locatable offset.
type Problem struct {
	Category  timeline.Category
	ElementID uint32
	Offset    int64
	Tracks    []uint64
	Reason    string
	Cluster   int
}

func (p Problem) toError() *timeline.Error {
	return &timeline.Error{
		Stage:     timeline.StageValidate,
		Category:  p.Category,
		ElementID: p.ElementID,
		Offset:    p.Offset,
		Tracks:    p.Tracks,
		Reason:    p.Reason,
	}
}

// Report is the quantitative result of fragment validation.
type Report struct {
	FragmentCount int
	ClusterCount  int
	MaxDeviation  timeline.Nanosecond
	DeviationAt   int64
	Problems      []Problem
}

// OK reports whether no fragment-level problem was found.
func (r Report) OK() bool { return len(r.Problems) == 0 }

// Err returns the first problem as the shared error type, or nil.
func (r Report) Err() error {
	if len(r.Problems) == 0 {
		return nil
	}
	return r.Problems[0].toError()
}

// Validate checks fragment timing, ordering, interleaving and — when the asset
// carries transport sequence numbers — sequence continuity.
func Validate(a *media.Asset) Report {
	rep := Report{FragmentCount: len(a.Fragments)}
	rep.Problems = append(rep.Problems, validateStarts(a)...)
	rep.Problems = append(rep.Problems, validateTracks(a)...)
	rep.Problems = append(rep.Problems, validateSequences(a)...)
	rep.Problems = append(rep.Problems, validateInterleave(a)...)

	dev, at := maxClusterDeviation(a.Fragments)
	rep.MaxDeviation = dev
	rep.DeviationAt = at
	if dev > timeline.MaxTolerance {
		rep.Problems = append(rep.Problems, Problem{
			Category:  timeline.CatFragmentConflict,
			ElementID: ElementBlock,
			Offset:    at,
			Tracks:    a.TrackNumbers(),
			Reason: fmt.Sprintf("cross-track deviation %s ns exceeds %d ns tolerance",
				timeline.FormatNS(dev), timeline.MaxTolerance),
		})
	}
	sort.SliceStable(rep.Problems, func(i, j int) bool {
		return rep.Problems[i].Offset < rep.Problems[j].Offset
	})
	rep.ClusterCount = clusterCount(a.Fragments)
	return rep
}

func clusterCount(fs []media.Fragment) int {
	seen := map[int]bool{}
	for _, f := range fs {
		seen[f.Cluster] = true
	}
	return len(seen)
}

func validateStarts(a *media.Asset) []Problem {
	var out []Problem
	for _, f := range a.Fragments {
		if f.Start < 0 {
			out = append(out, Problem{
				Category:  timeline.CatFragmentConflict,
				ElementID: ElementBlock,
				Offset:    f.Offset,
				Tracks:    []uint64{f.TrackNumber},
				Reason:    fmt.Sprintf("track %d fragment starts at negative time", f.TrackNumber),
			})
		}
		if a.Duration > 0 && f.Start > a.Duration {
			out = append(out, Problem{
				Category:  timeline.CatFragmentConflict,
				ElementID: ElementBlock,
				Offset:    f.Offset,
				Tracks:    []uint64{f.TrackNumber},
				Reason: fmt.Sprintf("track %d fragment starts at %s past duration %s",
					f.TrackNumber, timeline.FormatNS(f.Start), timeline.FormatNS(a.Duration)),
			})
		}
	}
	return out
}

// clusterDeviation compares each track's earliest block in a cluster against the
// cluster reference, returning the largest gap. It is the cross-track alignment
// metric reported by the CLI.
func maxClusterDeviation(fs []media.Fragment) (timeline.Nanosecond, int64) {
	type key struct {
		cluster int
		track   uint64
	}
	first := map[key]media.Fragment{}
	for _, f := range fs {
		k := key{f.Cluster, f.TrackNumber}
		if prev, ok := first[k]; !ok || f.Order < prev.Order {
			first[k] = f
		}
	}
	var maxDev timeline.Nanosecond
	var at int64 = -1
	keys := make([]key, 0, len(first))
	for k := range first {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].cluster != keys[j].cluster {
			return keys[i].cluster < keys[j].cluster
		}
		return keys[i].track < keys[j].track
	})
	for _, k := range keys {
		f := first[k]
		if dev := timeline.DeltaNS(f.Start, f.Reference); dev > maxDev {
			maxDev = dev
			at = f.Offset
		}
	}
	return maxDev, at
}

func validateTracks(a *media.Asset) []Problem {
	var out []Problem
	known := map[uint64]bool{}
	for _, t := range a.Tracks {
		known[t.Number] = true
	}
	reported := map[uint64]bool{}
	for _, f := range a.Fragments {
		if !known[f.TrackNumber] && !reported[f.TrackNumber] {
			reported[f.TrackNumber] = true
			out = append(out, Problem{
				Category:  timeline.CatFragmentConflict,
				ElementID: ElementBlock,
				Offset:    f.Offset,
				Tracks:    []uint64{f.TrackNumber},
				Reason:    fmt.Sprintf("block references undeclared track %d", f.TrackNumber),
			})
		}
	}
	return out
}

func validateSequences(a *media.Asset) []Problem {
	byTrack := map[uint64][]media.Fragment{}
	for _, f := range a.Fragments {
		if f.Seq >= 0 {
			byTrack[f.TrackNumber] = append(byTrack[f.TrackNumber], f)
		}
	}
	if len(byTrack) == 0 {
		return nil
	}
	tracks := make([]uint64, 0, len(byTrack))
	for t := range byTrack {
		tracks = append(tracks, t)
	}
	sort.Slice(tracks, func(i, j int) bool { return tracks[i] < tracks[j] })

	var out []Problem
	for _, tn := range tracks {
		fs := byTrack[tn]
		// Validate in document order, not sorted by sequence: a delivery that
		// stores fragments out of sequence order is itself the defect.
		sort.Slice(fs, func(i, j int) bool { return fs[i].Order < fs[j].Order })
		for i := 1; i < len(fs); i++ {
			switch {
			case fs[i].Seq == fs[i-1].Seq:
				out = append(out, Problem{
					Category: timeline.CatDuplicate,
					Offset:   fs[i].Offset,
					Tracks:   []uint64{tn},
					Reason: fmt.Sprintf("track %d sequence %d repeated (previous at offset %d)",
						tn, fs[i].Seq, fs[i-1].Offset),
				})
			case fs[i].Seq < fs[i-1].Seq:
				out = append(out, Problem{
					Category: timeline.CatOrder,
					Offset:   fs[i].Offset,
					Tracks:   []uint64{tn},
					Reason: fmt.Sprintf("track %d sequence went backwards: %d after %d",
						tn, fs[i].Seq, fs[i-1].Seq),
				})
			case fs[i].Seq != fs[i-1].Seq+1:
				out = append(out, Problem{
					Category: timeline.CatMissingElement,
					Offset:   fs[i].Offset,
					Tracks:   []uint64{tn},
					Reason: fmt.Sprintf("track %d sequence gap: %d follows %d",
						tn, fs[i].Seq, fs[i-1].Seq),
				})
			}
		}
		if a.SequenceBase >= 0 && fs[0].Seq != a.SequenceBase {
			out = append(out, Problem{
				Category: timeline.CatFragmentConflict,
				Offset:   fs[0].Offset,
				Tracks:   []uint64{tn},
				Reason: fmt.Sprintf("track %d starts at sequence %d, manifest expects %d",
					tn, fs[0].Seq, a.SequenceBase),
			})
		}
	}
	return out
}

// validateInterleave checks that blocks keep arrival order within a cluster and
// that a track's time never jumps backwards across clusters.
func validateInterleave(a *media.Asset) []Problem {
	var out []Problem
	byCluster := map[int][]media.Fragment{}
	for _, f := range a.Fragments {
		byCluster[f.Cluster] = append(byCluster[f.Cluster], f)
	}
	clusters := make([]int, 0, len(byCluster))
	for c := range byCluster {
		clusters = append(clusters, c)
	}
	sort.Ints(clusters)

	lastStart := map[uint64]timeline.Nanosecond{}
	lastOffset := map[uint64]int64{}
	for _, c := range clusters {
		fs := byCluster[c]
		for i := 1; i < len(fs); i++ {
			if fs[i].Order <= fs[i-1].Order {
				out = append(out, Problem{
					Category: timeline.CatOrder,
					Offset:   fs[i].Offset,
					Tracks:   []uint64{fs[i].TrackNumber},
					Reason: fmt.Sprintf("block order %d after %d inside cluster %d",
						fs[i].Order, fs[i-1].Order, c),
				})
			}
		}
		for _, f := range fs {
			if prev, ok := lastStart[f.TrackNumber]; ok && f.Start < prev {
				out = append(out, Problem{
					Category:  timeline.CatOrder,
					ElementID: ElementBlock,
					Offset:    f.Offset,
					Tracks:    []uint64{f.TrackNumber},
					Reason: fmt.Sprintf("track %d time went backwards from %s to %s (previous block at %d)",
						f.TrackNumber, timeline.FormatNS(prev), timeline.FormatNS(f.Start),
						lastOffset[f.TrackNumber]),
				})
			}
			lastStart[f.TrackNumber] = f.Start
			lastOffset[f.TrackNumber] = f.Offset
		}
	}
	return out
}
