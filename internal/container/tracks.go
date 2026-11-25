package container

import (
	"fmt"

	gomp4 "github.com/abema/go-mp4"

	"replaycheck/internal/clock"
)

func buildTrack(t *gomp4.Track, movie clock.Timebase, stblOffset int64) (TrackReport, []clock.Anomaly, error) {
	if t == nil {
		return TrackReport{}, nil, clock.NewError(clock.CodeMissingTrack, "nil track from go-mp4", -1)
	}
	tb, err := clock.NewTimebase(t.Timescale, 0, int64(t.Duration))
	if err != nil {
		ce := clock.AsError(err)
		ce.Offset = stblOffset
		return TrackReport{}, nil, ce
	}

	tr := TrackReport{
		ID: t.TrackID, Codec: codecName(t.Codec), Timebase: tb,
		SampleCount: len(t.Samples), StblOffset: stblOffset,
		Timeline: clock.Timeline{TrackID: t.TrackID, Timebase: tb, Frames: []clock.Frame{}},
	}
	anomalies := []clock.Anomaly{}

	var raw int64
	for i, sample := range t.Samples {
		if sample == nil {
			continue
		}
		startNS, err := clock.Convert(raw, t.Timescale)
		if err != nil {
			ce := clock.AsError(err)
			ce.Offset = stblOffset
			return tr, anomalies, ce
		}
		endNS, err := clock.Convert(raw+int64(sample.TimeDelta), t.Timescale)
		if err != nil {
			ce := clock.AsError(err)
			ce.Offset = stblOffset
			return tr, anomalies, ce
		}
		tr.Timeline.Frames = append(tr.Timeline.Frames, clock.Frame{
			Index: i, DTSRaw: raw, DTSNS: startNS,
			DurationRaw: sample.TimeDelta, DurationNS: endNS - startNS,
		})
		raw += int64(sample.TimeDelta)
	}

	if movie.Timescale != 0 {
		tr.DriftNS = clock.DriftNS(movie.EndNS, tb.EndNS)
		tr.DriftMS = float64(tr.DriftNS) / float64(clock.NanosecondsPerMillisecond)
		tr.DriftExceeded = clock.DriftExceeded(tr.DriftNS)
		if tr.DriftExceeded {
			anomalies = append(anomalies, clock.Anomaly{
				Kind: clock.CodeDrift, Severity: "error", Offset: stblOffset,
				StartNS: movie.EndNS, EndNS: tb.EndNS,
				ExpectedNS: movie.EndNS, ActualNS: tb.EndNS, DeviationNS: tr.DriftNS,
				Message: fmt.Sprintf("track %d drift %.3f ms exceeds 2 ms", t.TrackID, tr.DriftMS),
			})
		}
	}
	return tr, anomalies, nil
}

func codecName(c gomp4.Codec) string {
	switch c {
	case gomp4.CodecAVC1:
		return "avc1"
	case gomp4.CodecMP4A:
		return "mp4a"
	default:
		return "unknown"
	}
}

// MaxFrameBoundaryErrorNS returns the largest frame duration on a track. It
// is the upper bound for event-to-next-frame quantization error.
func MaxFrameBoundaryErrorNS(tr TrackReport) int64 {
	var max int64
	for _, f := range tr.Timeline.Frames {
		if f.DurationNS > max {
			max = f.DurationNS
		}
	}
	return max
}
