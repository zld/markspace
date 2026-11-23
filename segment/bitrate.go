package segment

import (
	"fmt"

	"mediadist/timeline"
)

// SegmentStat captures one segment's target and measured bitrate.
type SegmentStat struct {
	Index            int
	StartTicks       uint64
	DurationTicks    uint64
	Bytes            uint64
	TargetBitrateBps uint64
	ActualBitrateBps uint64
	// ErrorBasisPts is |actual-target|/target in basis points (per ten thousand).
	ErrorBasisPts uint64
}

// BitrateReport aggregates per-segment statistics for one representation.
type BitrateReport struct {
	RepresentationID  string
	Segments          []SegmentStat
	MeanErrorBasisPts uint64
	MaxErrorBasisPts  uint64
	Conformant        bool
}

// bytesBetween sums coded sample sizes whose DTS lies in [start, end).
func bytesBetween(t *timeline.Track, start, end uint64) uint64 {
	var total uint64
	for _, s := range t.Samples {
		if s.DTS >= start && s.DTS < end {
			total += uint64(s.Size)
		}
	}
	return total
}

// MeasureBitrate computes per-segment actual bitrate and the deviation from
// target. Actual bitrate uses exact integer scaling (bytes*8*timescale/dur);
// the deviation is basis points so no float enters persisted artifacts.
func MeasureBitrate(r Representation, plan *Plan, cfg Config) (*BitrateReport, error) {
	if r.Video == nil || len(plan.Boundaries) < 2 {
		return nil, timeline.NewLocError(timeline.CodeBadArgument, timeline.StageSegment, 0, -1,
			"bitrate", fmt.Errorf("representation %s lacks samples or boundaries", r.ID))
	}
	rep := &BitrateReport{RepresentationID: r.ID, Conformant: true}
	var errSum uint64
	for i := 0; i < len(plan.Boundaries)-1; i++ {
		start := plan.Boundaries[i].At
		end := plan.Boundaries[i+1].At
		dur := end - start
		if dur == 0 {
			return nil, timeline.NewLocError(timeline.CodeAlignmentDrift, timeline.StageSegment,
				r.Video.ID, i, "bitrate", fmt.Errorf("zero-duration segment"))
		}
		// Combine video and audio coded bytes when an audio track is present.
		n := bytesBetween(r.Video, start, end)
		if r.Audio != nil {
			n += bytesBetween(r.Audio, start, end)
		}
		// bps = bytes*8 * CommonTimescale / durationTicks
		actual := n * 8 * uint64(timeline.CommonTimescale) / dur
		var errBp uint64
		if r.TargetBitrateBps > 0 {
			var diff uint64
			if actual > r.TargetBitrateBps {
				diff = actual - r.TargetBitrateBps
			} else {
				diff = r.TargetBitrateBps - actual
			}
			errBp = diff * 10000 / r.TargetBitrateBps
		}
		st := SegmentStat{
			Index: i, StartTicks: start, DurationTicks: dur, Bytes: n,
			TargetBitrateBps: r.TargetBitrateBps, ActualBitrateBps: actual,
			ErrorBasisPts: errBp,
		}
		rep.Segments = append(rep.Segments, st)
		errSum += errBp
		if errBp > rep.MaxErrorBasisPts {
			rep.MaxErrorBasisPts = errBp
		}
		if errBp > cfg.MaxBitrateErrorBasisPts {
			rep.Conformant = false
		}
	}
	rep.MeanErrorBasisPts = errSum / uint64(len(rep.Segments))
	return rep, nil
}

// PSNR is computed from the deterministic synthetic sample fill. Because the
// fixtures are procedural rather than coded pictures, PSNR here models encoder
// fidelity as a bitrate-dependent exact rational saturation curve: higher
// target bitrate yields higher PSNR, bounded above. The value is expressed in
// hundredths of a dB (integer), keeping manifests free of floating point.
type PSNR struct {
	HundredthsDB uint64
}

// EstimatePSNR maps target bitrate to a deterministic quality figure. The
// curve saturates near 48 dB; identical inputs always produce identical output.
func EstimatePSNR(targetBitrateBps uint64) PSNR {
	const (
		floorHundredths   = 2800 // 28.00 dB
		ceilingHundredths = 4800
		referenceBps      = 3_000_000
	)
	_ = ceilingHundredths
	if targetBitrateBps == 0 {
		return PSNR{HundredthsDB: floorHundredths}
	}
	// q = 2000 * bps / (bps + reference): monotone, saturates at 2000.
	q := 2000 * targetBitrateBps / (targetBitrateBps + referenceBps)
	return PSNR{HundredthsDB: floorHundredths + q}
}

// String renders the integer PSNR as a fixed 2-decimal dB string.
func (p PSNR) String() string {
	return fmt.Sprintf("%d.%02d", p.HundredthsDB/100, p.HundredthsDB%100)
}
