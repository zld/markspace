package ingest

import (
	"sort"

	"mediamerge/internal/model"
)

// Result is the output of fragment recovery: a unified, ordered sample
// timeline plus explicit gap and track-disposition records.
type Result struct {
	Samples         []model.Sample
	Gaps            []model.Gap
	RecoveredTracks []uint32
	IsolatedTracks  []uint32
}

type fragRef struct {
	file *File
	frag *Fragment
}

// Recover merges fragments from all parsed files into one deterministic
// timeline. It never fabricates samples: mfhd gaps become Gap records,
// tfdt rollback is repaired only when the sequence chain makes the
// expected decode time unambiguous, otherwise the track is isolated.
func Recover(files []*File, rtp map[uint32][]RTPEvent, errs *model.Errors) *Result {
	res := &Result{}
	byTrack := make(map[uint32][]fragRef)
	var trackOrder []uint32
	for _, f := range files {
		for i := range f.Frags {
			fr := &f.Frags[i]
			if _, ok := byTrack[fr.TrackID]; !ok {
				trackOrder = append(trackOrder, fr.TrackID)
			}
			byTrack[fr.TrackID] = append(byTrack[fr.TrackID], fragRef{f, fr})
		}
	}
	sort.Slice(trackOrder, func(i, j int) bool { return trackOrder[i] < trackOrder[j] })

	for _, tid := range trackOrder {
		frags := byTrack[tid]
		sort.SliceStable(frags, func(i, j int) bool {
			a, b := frags[i], frags[j]
			if a.frag.Seq != b.frag.Seq {
				return a.frag.Seq < b.frag.Seq
			}
			if a.file.Path != b.file.Path {
				return a.file.Path < b.file.Path
			}
			return a.frag.MoofOffset < b.frag.MoofOffset
		})
		frags = dedupeFragments(tid, frags, errs)
		meta := findMeta(files, tid)
		if meta == nil || meta.Timescale == 0 {
			errs.Add(model.NewError(model.LevelIsolated, "recover", "", -1, tid,
				"track has no init-segment metadata; isolated"))
			res.IsolatedTracks = append(res.IsolatedTracks, tid)
			continue
		}
		samples, gaps, ok := resolveTimeline(tid, meta, frags, rtp[tid], errs)
		if !ok {
			res.IsolatedTracks = append(res.IsolatedTracks, tid)
			continue
		}
		res.Gaps = append(res.Gaps, gaps...)
		res.Samples = append(res.Samples, samples...)
		if hasRecovery(errs, tid) {
			res.RecoveredTracks = append(res.RecoveredTracks, tid)
		}
	}

	sort.SliceStable(res.Samples, func(i, j int) bool {
		return res.Samples[i].Key() < res.Samples[j].Key()
	})
	if bad := model.CheckMonotonic(res.Samples); bad >= 0 {
		errs.Add(model.NewError(model.LevelFatal, "recover",
			res.Samples[bad].SourceFile, res.Samples[bad].ByteOffset,
			res.Samples[bad].TrackID, "timeline monotonicity violated after recovery"))
	}
	return res
}

// dedupeFragments drops exact duplicate fragments (same seq, same tfdt,
// same sample layout). Conflicting duplicates keep the first in stable
// order and are reported.
func dedupeFragments(tid uint32, frags []fragRef, errs *model.Errors) []fragRef {
	out := frags[:0]
	for i, fr := range frags {
		if i > 0 && fr.frag.Seq == frags[i-1].frag.Seq {
			prev := frags[i-1].frag
			if sameLayout(prev, fr.frag) {
				errs.Add(model.NewError(model.LevelRecovered, "recover",
					fr.file.Path, fr.frag.MoofOffset, tid,
					"duplicate fragment seq=%d dropped", fr.frag.Seq))
				continue
			}
			errs.Add(model.NewError(model.LevelRecovered, "recover",
				fr.file.Path, fr.frag.MoofOffset, tid,
				"conflicting duplicate fragment seq=%d; first kept", fr.frag.Seq))
			continue
		}
		out = append(out, fr)
	}
	return out
}

func sameLayout(a, b *Fragment) bool {
	if a.TFDT != b.TFDT || a.HasTFDT != b.HasTFDT || len(a.Samples) != len(b.Samples) {
		return false
	}
	for i := range a.Samples {
		if a.Samples[i].Size != b.Samples[i].Size ||
			a.Samples[i].DurationTicks != b.Samples[i].DurationTicks {
			return false
		}
	}
	return true
}

// resolveTimeline repairs tfdt rollback and converts to nanoseconds.
// Recovery requires an unbroken mfhd sequence chain so the expected decode
// time follows from continuous sample durations; otherwise the track is
// isolated. An available RTP capture must confirm monotone arrival order.
func resolveTimeline(tid uint32, meta *TrackMeta, frags []fragRef,
	rtpEvents []RTPEvent, errs *model.Errors) ([]model.Sample, []model.Gap, bool) {

	if len(rtpEvents) > 0 && !Monotonic(rtpEvents) {
		errs.Add(model.NewError(model.LevelIsolated, "recover", "", -1, tid,
			"RTP capture contradicts fragment order; track isolated"))
		return nil, nil, false
	}

	ts := model.Timescale{TicksPerSecond: uint64(meta.Timescale)}
	var samples []model.Sample
	var gaps []model.Gap
	var prevEndTicks uint64

	for i, fr := range frags {
		f := fr.frag
		var fragDur uint64
		for _, s := range f.Samples {
			fragDur += uint64(s.DurationTicks)
		}
		tfdt := f.TFDT
		if i > 0 {
			prev := frags[i-1].frag
			expected := prevEndTicks
			seqContinuous := f.Seq == prev.Seq+1
			switch {
			case !f.HasTFDT:
				if !seqContinuous {
					errs.Add(model.NewError(model.LevelIsolated, "recover",
						fr.file.Path, f.MoofOffset, tid,
						"missing tfdt across sequence gap; cannot locate fragment"))
					return nil, nil, false
				}
				tfdt = expected
				errs.Add(model.NewError(model.LevelRecovered, "recover",
					fr.file.Path, f.MoofOffset, tid,
					"missing tfdt reconstructed from continuous durations"))
			case tfdt < expected:
				if !seqContinuous {
					errs.Add(model.NewError(model.LevelIsolated, "recover",
						fr.file.Path, f.MoofOffset, tid,
						"tfdt rollback (%d < expected %d) across sequence gap; unrecoverable",
						tfdt, expected))
					return nil, nil, false
				}
				errs.Add(model.NewError(model.LevelRecovered, "recover",
					fr.file.Path, f.MoofOffset, tid,
					"tfdt rollback %d -> %d repaired from continuous durations",
					tfdt, expected))
				tfdt = expected
			}
			if f.Seq > prev.Seq+1 {
				startNs, _ := ts.ToNs(prevEndTicks)
				gaps = append(gaps, model.Gap{
					TrackID:    tid,
					AfterSeq:   prev.Seq,
					BeforeSeq:  f.Seq,
					Time:       model.Interval{Start: startNs, End: prevEndNsOf(startNs, frags, i, ts, tfdt)},
					EstSamples: int(f.Seq-prev.Seq-1) * medianSamples(frags),
				})
			}
		}
		ticks := tfdt
		for _, s := range f.Samples {
			start, _ := ts.ToNs(ticks)
			dur, _ := ts.ToNs(uint64(s.DurationTicks))
			samples = append(samples, model.Sample{
				TrackID:    tid,
				SeqNum:     f.Seq,
				SourceFile: fr.file.Path,
				ByteOffset: s.Offset,
				Size:       s.Size,
				Start:      start,
				Dur:        model.NsDuration(dur),
				Index:      s.Index,
			})
			ticks += uint64(s.DurationTicks)
		}
		prevEndTicks = ticks
	}
	return samples, gaps, true
}

// prevEndNsOf computes the gap end: the start time of the next fragment.
func prevEndNsOf(start model.NsTime, frags []fragRef, i int, ts model.Timescale, tfdt uint64) model.NsTime {
	end, _ := ts.ToNs(tfdt)
	if end < start {
		return start
	}
	return end
}

func medianSamples(frags []fragRef) int {
	if len(frags) == 0 {
		return 0
	}
	counts := make([]int, 0, len(frags))
	for _, fr := range frags {
		counts = append(counts, len(fr.frag.Samples))
	}
	sort.Ints(counts)
	return counts[len(counts)/2]
}

func findMeta(files []*File, tid uint32) *TrackMeta {
	for _, f := range files {
		if m := f.Meta(tid); m != nil {
			return m
		}
	}
	return nil
}

// hasRecovery reports whether a recovered-level diagnostic was recorded
// for the track during this run.
func hasRecovery(errs *model.Errors, tid uint32) bool {
	for _, e := range errs.All() {
		if e.TrackID == tid && e.Level == model.LevelRecovered && e.Op == "recover" {
			return true
		}
	}
	return false
}
