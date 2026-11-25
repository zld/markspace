package index

import (
	"context"

	"mediacore/internal/clock"
	"mediacore/internal/diagnostic"
)

// QueryIndex returns samples overlapping the half-open [startU,endU) interval.
// Unaligned representations are rejected rather than silently mixed into results.
func QueryIndex(idx *EntryIndex, startU, endU int64) (*RangeReport, error) {
	if idx == nil {
		return nil, diagnostic.New(diagnostic.ErrOutOfRange, diagnostic.Location{Stage: "query", ByteOffset: -1}, "nil index")
	}
	loc := diagnostic.Location{
		Stage:          "query",
		ManifestPath:   idx.Manifest,
		Representation: idx.Representation,
		EntryKey:       idx.StableKey,
		EntryIndex:     idx.EntryIndex,
		Sequence:       idx.OriginalSequence,
		ByteOffset:     -1,
	}
	if !idx.Aligned {
		loc.Field = "alignment"
		return nil, diagnostic.New(diagnostic.ErrMisaligned, loc,
			"representation deviates by more than 20ms; refusing interval mix")
	}
	if endU < startU {
		loc.Field = "interval"
		return nil, diagnostic.New(diagnostic.ErrTimeRegression, loc, "query interval end precedes start")
	}

	report := &RangeReport{RequestedStartU: startU, RequestedEndU: endU}
	var selected []Sample
	for _, s := range idx.Samples {
		// Half-open overlap: [s.StartU,s.EndU) ∩ [startU,endU).
		if s.StartU < endU && s.EndU > startU {
			selected = append(selected, s)
		}
	}
	if len(selected) == 0 {
		return nil, diagnostic.New(diagnostic.ErrOutOfRange, loc, "interval contains no samples")
	}
	report.Samples = selected
	report.SampleCount = len(selected)
	first := selected[0]
	last := selected[len(selected)-1]
	report.FirstTick = first.StartU
	report.LastTick = last.EndU
	report.FirstByteOffset = first.ByteOffset
	report.LastByteEnd = last.ByteOffset + last.ByteSize

	var err error
	if report.FirstTickDeviation, err = clock.Diff(first.StartU, startU); err != nil {
		return nil, diagnostic.Wrap(err, loc, err)
	}
	if report.LastTickDeviation, err = clock.Diff(last.EndU, endU); err != nil {
		return nil, diagnostic.Wrap(err, loc, err)
	}

	// Leading byte gap relative to the preceding indexed sample.
	if first.Index > 0 && first.Index < len(idx.Samples) {
		prev := idx.Samples[first.Index-1]
		expected := prev.ByteOffset + prev.ByteSize
		report.FirstByteDeviation = first.ByteOffset - expected
	} else {
		report.FirstByteDeviation = 0
	}

	// Trailing drift from a contiguous run beginning at the first selected offset.
	var sumSizes int64
	for _, s := range selected {
		sumSizes += s.ByteSize
	}
	expectedEnd := first.ByteOffset + sumSizes
	report.LastByteDeviation, err = clock.Diff(report.LastByteEnd, expectedEnd)
	if err != nil {
		return nil, diagnostic.Wrap(err, loc, err)
	}
	return report, nil
}

// ReadRangeBytes reads every selected sample in bounded chunks. Cancellation is
// checked between chunks and samples.
func ReadRangeBytes(ctx context.Context, r *RangeReport, chunk int, consume func([]byte) error) (int64, error) {
	if r == nil {
		return 0, diagnostic.New(diagnostic.ErrOutOfRange, diagnostic.Location{Stage: "query", ByteOffset: -1}, "nil report")
	}
	var total int64
	for _, s := range r.Samples {
		if err := ctx.Err(); err != nil {
			return total, diagnostic.Wrap(diagnostic.ErrCancelled, diagnostic.Location{
				Stage: "query", EntryKey: s.StableKey, ByteOffset: s.ByteOffset,
			}, err)
		}
		var sampleBytes int64
		err := ReadSampleRangeChunks(s.FilePath, s.ByteOffset, s.ByteSize, chunk, func(p []byte) error {
			if err := ctx.Err(); err != nil {
				return diagnostic.Wrap(diagnostic.ErrCancelled, diagnostic.Location{
					Stage: "query", EntryKey: s.StableKey, ByteOffset: s.ByteOffset + sampleBytes,
				}, err)
			}
			sampleBytes += int64(len(p))
			total += int64(len(p))
			if consume != nil {
				return consume(p)
			}
			return nil
		})
		if err != nil {
			return total, err
		}
	}
	return total, nil
}
