package manifest

import (
	"strconv"

	"archivecheck/internal/model"
)

// buildHLSTimeline converts parsed segments onto the shared timeline.
func buildHLSTimeline(pl *hlsPlaylist, opts Options, v *Validator) (*Plan, error) {
	base := opts.base(DefaultTimebase)
	plan := NewPlan(FormatHLS, pl.file)
	plan.Timebase = base
	plan.MediaSequence = pl.mediaSequence
	plan.Faults = &v.faults

	if pl.hasTarget {
		t, err := model.SecondsToTime(pl.targetDuration.Num, pl.targetDuration.Den, base)
		if err != nil || !t.Rem.IsZero() {
			// A target duration that is not a whole number of plan ticks would
			// silently round the advertised cadence, so it is a hard error.
			v.At(1, 1)
			v.fail(model.CodeManifestTimebase,
				"EXT-X-TARGETDURATION %s does not map to whole %d tps ticks (remainder %s)",
				pl.targetDuration, base, t.Rem)
			return nil, v.Err()
		}
		plan.TargetDuration = t.Ticks
	} else {
		v.warn(model.CodeManifestMissingField, "EXT-X-TARGETDURATION missing; EXTINF durations remain authoritative")
	}

	rep := Representation{ID: "hls-media", Timescale: base}
	acc := NewAccumulator(base)
	rangeCursor := map[string]int64{}
	for i := range pl.segments {
		raw := &pl.segments[i]
		v.At(raw.uriLine, 1)
		d, err := model.SecondsToTime(raw.duration.Num, raw.duration.Den, base)
		if err != nil {
			v.At(raw.line, 1)
			v.fail(model.CodeManifestTimebase, "segment %d duration %s does not fit timebase %d: %v", i, raw.duration, base, err)
			continue
		}
		if !opts.AllowInexact && !d.Rem.IsZero() {
			v.At(raw.line, 1)
			v.fail(model.CodeManifestTimebase,
				"segment %d duration %s is not an integral number of %d tps ticks (remainder %s); use --allow-inexact to carry it",
				i, raw.duration, base, d.Rem)
			continue
		}
		seg := Segment{
			Index:          i,
			ID:             strconv.FormatInt(pl.mediaSequence+int64(i), 10),
			URI:            raw.uri,
			Discontinuity:  raw.discontinuity,
			Representation: rep.ID,
		}
		if raw.gap {
			seg.URI = ""
		}
		if raw.byteRange != nil {
			br := *raw.byteRange
			if !raw.rangeExplicit {
				prev, ok := rangeCursor[raw.uri]
				if !ok {
					v.At(raw.uriLine, 1)
					v.fail(model.CodeManifestBadRange,
						"segment %d byte range for %q omits its offset but no earlier range exists", i, raw.uri)
					continue
				}
				total, ok := addRange(prev, br.Len())
				if !ok {
					v.At(raw.uriLine, 1)
					v.fail(model.CodeManifestBadRange, "segment %d byte range overflows int64", i)
					continue
				}
				br.Start, br.End = prev, total
			}
			if !br.Valid() {
				v.At(raw.uriLine, 1)
				v.fail(model.CodeManifestBadRange, "segment %d has invalid byte range [%d,%d)", i, br.Start, br.End)
				continue
			}
			rangeCursor[raw.uri] = br.End
			seg.Range = &br
			seg.ByteOffset = br.Start
		}
		start := acc.Position()
		integral, startRem, endRem, err := acc.Next(d)
		if err != nil {
			v.At(raw.uriLine, 1)
			v.fail(model.CodeManifestTimebase, "segment %d timeline overflow: %v", i, err)
			continue
		}
		seg.Start = start.Ticks
		seg.Duration = integral
		seg.Remainder = startRem
		seg.EndRemainder = endRem
		rep.Segments = append(rep.Segments, seg)
	}
	if limit := opts.maxSegments(); limit > 0 && len(rep.Segments) > limit {
		v.fail(model.CodeManifestBoundary, "playlist expands to %d segments, limit is %d", len(rep.Segments), limit)
	}
	plan.Representations = []Representation{rep}
	pos := acc.Position()
	plan.Duration = pos.Ticks
	plan.Remainder = pos.Rem
	if v.faults.Len() > 0 {
		return nil, v.Err()
	}
	return plan, nil
}
