package clock

import (
	"mediarc/internal/model"
)

// FanoutConfig controls per-representation recovery. Buffering is bounded by
// MaxSamplesPerTrack so a runaway representation cannot exhaust memory.
type FanoutConfig struct {
	MaxSamplesPerTrack    int
	DriftThresholdMsPerSec int64
}

// DefaultFanoutConfig returns conservative limits.
func DefaultFanoutConfig() FanoutConfig {
	return FanoutConfig{MaxSamplesPerTrack: 100000, DriftThresholdMsPerSec: 5}
}

// RecoverRepresentation recovers every track of one representation in
// isolation. A failure on one track or representation is captured in its own
// Errors slice and never aborts the others. Original sample timestamps are
// copied into RawTimes and are never modified.
func RecoverRepresentation(rep model.Representation, tracks []model.Track, cfg FanoutConfig) model.RecoveredRepresentation {
	out := model.RecoveredRepresentation{
		ID:     rep.ID,
		Format: rep.Format,
		OK:     true,
	}
	if len(rep.ManifestErrors) > 0 {
		out.Errors = append(out.Errors, rep.ManifestErrors...)
		out.OK = false
	}
	for ti := range tracks {
		rt := recoverTrack(rep.ID, tracks[ti], cfg)
		// Informational events (e.g. wraparound expansion) do not make a
		// representation unhealthy; only hard errors do.
		for _, e := range rt.Errors {
			if e.Kind != model.KindTimestampWrap {
				out.OK = false
				break
			}
		}
		out.Errors = append(out.Errors, rt.Errors...)
		out.Tracks = append(out.Tracks, rt)
	}
	return out
}

func errFor(kind model.ErrorKind, repID, msg string) *model.MediaError {
	return model.New(kind, repID, msg).ForItem(repID)
}

func recoverTrack(repID string, tr model.Track, cfg FanoutConfig) model.RecoveredTrack {
	rt := model.RecoveredTrack{Track: tr, Trusted: true}
	n := len(tr.Samples)
	if n == 0 {
		rt.Trusted = false
		rt.Errors = append(rt.Errors, model.AtOffset(model.KindSegmentCorrupt, "", -1, repID, "track "+itoa32(tr.ID)+" has no samples"))
		return rt
	}
	if cfg.MaxSamplesPerTrack > 0 && n > cfg.MaxSamplesPerTrack {
		rt.Trusted = false
		rt.Errors = append(rt.Errors, errFor(model.KindSegmentCorrupt, repID,
			"track "+itoa32(tr.ID)+" sample count "+itoa(n)+" exceeds buffer budget "+itoa(cfg.MaxSamplesPerTrack)))
	}

	// Prefer the RTP clock packets (pion/rtp semantics); fall back to the
	// raw container timestamps expanded through the same 32-bit carrier.
	var unwrapped []int64
	var raw []uint32
	if len(tr.ClockPackets) == n {
		up, rv, err := ExtendRTP(tr.ClockPackets)
		if err != nil {
			rt.Trusted = false
			rt.Errors = append(rt.Errors, errFor(model.KindSegmentCorrupt, repID, err.Error()))
		} else {
			unwrapped, raw = up, rv
		}
	}
	if unwrapped == nil {
		raw = make([]uint32, n)
		unwrapped = make([]int64, n)
		u32 := NewUnwrapper32()
		for i, s := range tr.Samples {
			raw[i] = uint32(s.Dts)
			unwrapped[i] = u32.Push(raw[i])
		}
		rt.WrapCount = u32.WrapCount()
	}
	// Container DTS values are full-width integers: a raw value drop below
	// half the 32-bit carrier range is an unambiguous carrier wrap even when
	// the modular delta (e.g. 0xFFFFFFF8 -> 0x00000002) stays positive.
	for i := 1; i < n; i++ {
		if tr.Samples[i].Dts+halfRange < tr.Samples[i-1].Dts && tr.Samples[i].Dts < halfRange {
			rt.WrapCount++
		}
	}
	if rt.WrapCount > 0 {
		rt.Errors = append(rt.Errors, errFor(model.KindTimestampWrap, repID,
			"expanded "+itoa(rt.WrapCount)+" timestamp wraparound(s) on track "+itoa32(tr.ID)))
	}

	// Raw timestamps are retained verbatim alongside recovery output.
	rt.RawTimes = make([]uint64, n)
	durations := make([]uint32, n)
	for i, s := range tr.Samples {
		rt.RawTimes[i] = s.Dts
		durations[i] = s.Duration
	}
	rt.Unwrapped = unwrapped

	drift := EstimateDrift(unwrapped, durations)
	rt.DriftPPM = drift.DriftPPM
	rt.VFR = drift.VFR
	if DriftExceeded(drift.DriftPPM, cfg.DriftThresholdMsPerSec) {
		rt.Trusted = false
		rt.Errors = append(rt.Errors, errFor(model.KindDriftExceeded, repID,
			"drift "+itoa64(drift.DriftPPM)+" PPM exceeds "+itoa64(cfg.DriftThresholdMsPerSec)+" ms/s on track "+itoa32(tr.ID)))
	}

	rt.Boundaries = buildBoundaries(repID, tr.Timescale, unwrapped, durations, &rt)
	return rt
}

func buildBoundaries(repID string, timescale uint32, unwrapped []int64, durations []uint32, rt *model.RecoveredTrack) []model.Boundary {
	if timescale == 0 {
		rt.Trusted = false
		rt.Errors = append(rt.Errors, errFor(model.KindTimescaleIncompat, repID, "zero timescale cannot anchor a timeline"))
		return nil
	}
	// Boundaries follow distinct sample DTS discontinuities: each time the
	// actual gap differs from the previous sample duration, a new segment
	// boundary begins. This is where fragmented DASH/HLS segments join.
	bounds := []model.Boundary{{SegmentIndex: 0, StartMs: model.DurationMillis(unwrapped[0], timescale)}}
	bidx := 0
	for i := 1; i < len(unwrapped); i++ {
		// A segment boundary sits where the inter-sample gap stops matching
		// the declared duration of the sample that precedes the gap.
		gap := unwrapped[i] - unwrapped[i-1]
		if gap != int64(durations[i-1]) {
			bounds[bidx].EndMs = model.DurationMillis(unwrapped[i], timescale)
			bounds = append(bounds, model.Boundary{
				SegmentIndex: bidx + 1,
				StartMs:      model.DurationMillis(unwrapped[i], timescale),
			})
			bidx++
		}
	}
	end := unwrapped[len(unwrapped)-1] + int64(durations[len(durations)-1])
	bounds[bidx].EndMs = model.DurationMillis(end, timescale)
	return bounds
}

func itoa32(v uint32) string { return itoa(int(v)) }
func itoa64(v int64) string {
	neg := v < 0
	if neg {
		v = -v
	}
	s := itoa(int(v))
	if neg {
		return "-" + s
	}
	return s
}
