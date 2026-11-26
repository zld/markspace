package index

import (
	"fmt"

	"medtrace/internal/core"
	"medtrace/internal/manifest"
)

// Build scans every segment of a playlist in manifest order, producing an
// immutable index, the time-jump anomalies found, and a fatal fault if media
// could not be read at all. Per-segment damage is recorded on the Segment and
// returned as an anomaly, not as a fatal error.
func Build(root string, pl *manifest.Playlist) (*core.Index, []core.Anomaly, error) {
	type repInfo struct {
		init  *initInfo
		scale uint32
		kind  core.TrackKind
	}
	reps := make(map[string]repInfo, len(pl.Representations))
	for _, r := range pl.Representations {
		info, f := readInit(root, r.Init)
		if f != nil {
			return nil, nil, f
		}
		scale := r.Timescale
		if info != nil && info.timescale != 0 {
			scale = info.timescale
		}
		if scale == 0 {
			scale = 90000
		}
		reps[r.ID] = repInfo{init: info, scale: scale, kind: r.Track}
	}

	tracker := core.NewEpochTracker("ticks", 1<<32)

	var (
		segs      []*core.Segment
		anomalies []core.Anomaly
		prevRep   string
		prevRaw   int64
		haveRaw   bool
		lastEnd   core.Nanos
	)

	for _, ms := range pl.Segments {
		ri, known := reps[ms.Representation]
		if !known && ms.Representation != "" {
			return nil, nil, core.NewFault(core.KindManifest,
				core.Pos{File: pl.File, Line: ms.Line, Byte: -1}, "build",
				fmt.Sprintf("segment references unknown representation %q", ms.Representation))
		}
		kind := ri.kind
		scale := ri.scale
		if scale == 0 {
			scale = 90000
		}

		sc, digest, size, pf := scanFragment(root, ms.URI)
		seg := &core.Segment{
			URI:            ms.URI,
			Representation: ms.Representation,
			SHA256:         digest,
			Size:           size,
		}
		if sc != nil && sc.trackID != 0 {
			seg.Track.ID = sc.trackID
		}
		seg.Track.Kind = kind
		seg.Track.Representation = ms.Representation

		if pf != nil {
			if !core.Recorded(pf.Kind) {
				return nil, nil, pf
			}
			seg.Truncated = pf.Kind == core.KindSegmentTruncated
			anomalies = append(anomalies, core.Anomaly{
				Category:   categoryOf(pf.Kind),
				Pos:        core.Pos{File: ms.URI, Line: ms.Line, Byte: pf.Pos.Byte},
				Track:      seg.Track,
				Segment:    ms.URI,
				ByteOffset: pf.Pos.Byte,
				Detail:     pf.Detail,
			})
		}
		if sc != nil {
			if vf := verify(sc, size); vf != nil {
				seg.Truncated = seg.Truncated || vf.Kind == core.KindSegmentTruncated
				anomalies = append(anomalies, core.Anomaly{
					Category:   categoryOf(vf.Kind),
					Pos:        core.Pos{File: ms.URI, Line: ms.Line, Byte: vf.Pos.Byte},
					Track:      seg.Track,
					Segment:    ms.URI,
					ByteOffset: vf.Pos.Byte,
					Detail:     vf.Detail,
				})
			}
		}

		// Assign epochs and canonical positions for the samples that parsed.
		if sc != nil && len(sc.samples) > 0 {
			reason := core.EpochReason("")
			if !tracker.HasEpoch() {
				reason = core.EpochStart
			} else if ms.Discontinuity {
				// An explicit HLS discontinuity is the strongest signal: it
				// marks a packaging boundary even when the representation ID
				// also changes.
				reason = core.EpochDiscontinuity
			} else if ms.Representation != prevRep {
				reason = core.EpochRepresentation
			} else if haveRaw {
				reason = tracker.Jump(prevRaw, sc.samples[0].dts)
				if reason == "" && sc.samples[0].dts < prevRaw {
					reason = core.EpochBackwards
				}
			}
			first := sc.samples[0]
			rawNanos, err := core.FromTicks(first.dts, scale)
			if err != nil {
				return nil, nil, core.NewFault(core.KindBoxParse,
					core.Pos{File: ms.URI, Byte: first.offset}, "build", err.Error())
			}
			if reason == core.EpochStart {
				tracker.OpenStart(ms.URI, first.dts, rawNanos)
			} else if reason != "" {
				tracker.OpenAfter(reason, ms.URI, first.dts, rawNanos, lastEnd)
				anomalies = append(anomalies, core.Anomaly{
					Category:       core.CatTimeJump,
					Reason:         string(reason),
					Pos:            core.Pos{File: pl.File, Line: ms.Line, Byte: -1},
					Track:          seg.Track,
					Segment:        ms.URI,
					ByteOffset:     first.offset,
					RawTimestamp:   first.dts,
					Timescale:      scale,
					Canonical:      tracker.Current().CanonicalBase,
					BoundaryOffset: tracker.Current().BoundaryOffset,
					Detail:         fmt.Sprintf("opened epoch %d (%s)", tracker.Current().Number, reason),
				})
			}

			epochNum := tracker.Current().Number
			for si, rs := range sc.samples {
				rn, err := core.FromTicks(rs.dts, scale)
				if err != nil {
					return nil, nil, core.NewFault(core.KindBoxParse,
						core.Pos{File: ms.URI, Byte: rs.offset}, "build", err.Error())
				}
				// A backwards step inside one fragment (rare, but a damaged or
				// remuxed file can do it) opens its own epoch at the sample.
				if si > 0 {
					if why := tracker.Jump(sc.samples[si-1].dts, rs.dts); why != "" {
						tracker.OpenAfter(why, ms.URI, rs.dts, rn, tracker.Last())
						anomalies = append(anomalies, core.Anomaly{
							Category:       core.CatTimeJump,
							Reason:         string(why),
							Pos:            core.Pos{File: ms.URI, Byte: rs.offset},
							Track:          seg.Track,
							Segment:        ms.URI,
							ByteOffset:     rs.offset,
							RawTimestamp:   rs.dts,
							Timescale:      scale,
							Canonical:      tracker.Current().CanonicalBase,
							BoundaryOffset: tracker.Current().BoundaryOffset,
						})
						epochNum = tracker.Current().Number
					}
				}
				canon, _ := tracker.Map(rn)
				dur, err := core.FromTicks(rs.dur, scale)
				if err != nil {
					return nil, nil, core.NewFault(core.KindBoxParse,
						core.Pos{File: ms.URI, Byte: rs.offset}, "build", err.Error())
				}
				smp := core.Sample{
					Track:      seg.Track,
					Segment:    ms.URI,
					ByteOffset: rs.offset,
					Size:       rs.size,
					RawTicks:   rs.dts,
					DTSTicks:   rs.dts,
					CTSTicks:   rs.cts,
					Timescale:  scale,
					Canonical:  canon,
					Duration:   dur,
					Epoch:      epochNum,
					Keyframe:   rs.key,
				}
				if rs.key {
					seg.Keyframes = append(seg.Keyframes, len(seg.Samples))
				}
				seg.Samples = append(seg.Samples, smp)
				lastEnd = canon + dur
			}
			seg.Start = seg.Samples[0].Canonical
			seg.End = lastEnd
			prevRaw = sc.samples[len(sc.samples)-1].dts
			haveRaw = true
		}
		prevRep = ms.Representation
		// A segment with no parseable samples still occupies the canonical
		// timeline for its manifest duration so later epochs stay ordered.
		if len(seg.Samples) == 0 && ms.Duration > 0 {
			seg.Start = lastEnd
			seg.End = lastEnd + ms.Duration
			lastEnd = seg.End
		}
		segs = append(segs, seg)
	}
	idx := core.NewIndex(segs)
	idx.Epochs = tracker.Epochs()
	core.SortAnomalies(anomalies)
	return idx, anomalies, nil
}

func categoryOf(k core.Kind) core.Category {
	switch k {
	case core.KindSegmentTruncated:
		return core.CatTruncated
	case core.KindHashMismatch:
		return core.CatHashMismatch
	case core.KindNoKeyframe:
		return core.CatNoKeyframe
	case core.KindTimeJump:
		return core.CatTimeJump
	default:
		return core.CatBoxParse
	}
}
