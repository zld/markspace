package job

import (
	"path/filepath"

	"medpipe/internal/apperr"
	"medpipe/internal/clock"
	"medpipe/internal/manifest"
	"medpipe/internal/mp4"
	"medpipe/internal/report"
)

// TrackResult is the durable per-track processing checkpoint.
type TrackResult struct {
	Conclusion  report.TrackConclusion `json:"conclusion"`
	Scan        *mp4.TrackScan         `json:"scan"`
	Samples     []mp4.Sample           `json:"samples"`
	RTPPoints   []clock.RTPPoint       `json:"rtp_points"`
	SampleShift []int64                `json:"sample_shift_ticks"`
}

func processTrack(m *manifest.Manifest, baseDir string, t manifest.Track) (*TrackResult, error) {
	absPath := filepath.Join(baseDir, filepath.FromSlash(t.Path))
	var samples []mp4.Sample
	scan, err := mp4.ScanFile(t.ID, absPath, m.Parameters.BatchSamples, func(batch []mp4.Sample) error {
		samples = append(samples, batch...)
		return nil
	})
	if err != nil {
		if pe, ok := apperr.As(err); ok {
			pe.SourceFile = t.Path
		}
		return nil, err
	}
	scan.SourceFile = t.Path

	points := make([]clock.RTPPoint, 0, len(samples))
	for _, sample := range samples {
		payload, err := mp4.ReadPayload(absPath, sample)
		if err != nil {
			return nil, relabel(err, t.Path)
		}
		p, err := clock.ParseRTP(sample.Number, payload, sample.ByteOffset)
		if err != nil {
			return nil, relabel(err, t.Path)
		}
		points = append(points, p)
	}
	unwrapped, wraps := clock.UnwrapRTP(points)
	if len(unwrapped) > 0 {
		sourceMap := clock.NewMapper(scan.MediaTimeBase, unwrapped[0].Timestamp)
		for i := range unwrapped {
			unwrapped[i].RelativeTick = sourceMap.Relative(unwrapped[i].Timestamp)
		}
	}

	c := report.TrackConclusion{ID: t.ID, SourceFile: t.Path}
	c.Samples = scan.SampleCount
	c.SyncSamples = append([]int(nil), scan.SyncSamples...)
	c.WrapDetails = wraps
	c.WrapEvents = len(wraps)
	for _, e := range scan.Edits {
		if e.Empty {
			c.EmptyEdits++
		}
	}
	c.SampleRange = [2]int{1, scan.SampleCount}
	checkpoint, first, last := ResumeSampleRange(scan.SyncSamples, scan.SampleCount, scan.SampleCount)
	c.CheckpointSample = checkpoint
	c.ResumeRange = [2]int{first, last}

	switches := switchesForTrack(m, t.ID)
	shifts := make([]int64, scan.SampleCount)
	for _, sw := range switches {
		if sw.Sample <= 1 || sw.Sample > scan.SampleCount {
			var offset int64
			if sw.Sample >= 1 && sw.Sample <= len(samples) {
				offset = samples[sw.Sample-1].ByteOffset
			}
			return nil, apperr.New("E_SAMPLE_RANGE", "source switch sample %d outside 2..%d", sw.Sample, scan.SampleCount).
				WithStage("repair").WithTrack(t.ID, t.Path).WithSample(sw.Sample, offset)
		}
		k := sw.Sample - 1
		boundary := clock.Boundary{
			SwitchID:            sw.ID,
			Track:               t.ID,
			SourceFile:          t.Path,
			Sample:              sw.Sample,
			Timescale:           scan.MediaTimeBase.TicksPerSecond,
			PrevTick:            unwrapped[k-1].Timestamp,
			CurrentTick:         unwrapped[k].Timestamp,
			PreviousDuration:    samples[k-1].Duration,
			LastTick:            unwrapped[len(unwrapped)-1].Timestamp,
			LastDuration:        samples[len(samples)-1].Duration,
			FirstAffectedSample: sw.Sample,
			LastAffectedSample:  scan.SampleCount,
			ByteOffset:          samples[k].ByteOffset,
			LeftAnchorShift:     sw.LeftAnchorOffset,
			RightAnchorShift:    sw.RightAnchorOffset,
		}
		decision := clock.ResolveBoundary(boundary)
		switch {
		case decision.Repair != nil:
			r := decision.Repair
			r.SourceFile = t.Path
			r.Source = sw.LeftSource + " -> " + sw.RightSource
			c.Repairs = append(c.Repairs, *r)
			c.RepairCount++
			if r.ResidualTicks > c.MaxResidualTicks {
				c.MaxResidualTicks = r.ResidualTicks
			}
			c.Anomaly = &report.Interval{StartTick: r.IntervalStartTick, EndTick: r.IntervalEndTick}
			c.OriginalOffsetTick = r.OriginalOffsetTicks
			c.RepairOffsetTick = r.RepairOffsetTicks
			c.Detail = r.Basis
			for i := k; i < len(shifts); i++ {
				shifts[i] += r.RepairOffsetTicks
			}
		case decision.Isolation != nil:
			iso := decision.Isolation
			iso.SourceFile = t.Path
			c.Isolation = iso
			c.Verdict = "ISOLATED"
			c.OriginalOffsetTick = iso.JumpTicks
			c.Detail = iso.Reason
		}
	}

	if c.Verdict != "ISOLATED" {
		switch {
		case c.RepairCount > 0:
			c.Verdict = "REPAIRED"
		default:
			c.Verdict = "PASS"
		}
	}
	return &TrackResult{
		Conclusion:  c,
		Scan:        scan,
		Samples:     samples,
		RTPPoints:   unwrapped,
		SampleShift: shifts,
	}, nil
}

func switchesForTrack(m *manifest.Manifest, track string) []manifest.Switch {
	var out []manifest.Switch
	for _, sw := range m.Switches {
		if sw.Track == track {
			out = append(out, sw)
		}
	}
	return out
}

func relabel(err error, source string) error {
	if pe, ok := apperr.As(err); ok {
		pe.SourceFile = source
		return pe
	}
	return err
}
