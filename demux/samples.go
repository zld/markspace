package demux

import (
	"fmt"

	"mediadist/timeline"
)

// expandSamples materializes the sample table of one track onto the common
// clock. The stts run-length table yields native DTS/duration values; stss
// marks keyframes and stsz coded sizes. Every count is bounded before
// allocation so corrupt tables cannot exhaust memory.
func expandSamples(t *timeline.Track, boxes []rawBox, clock timeline.Clock) error {
	stts := findBox(boxes, "stts")
	if stts == nil {
		return timeline.NewLocError(timeline.CodeTruncatedIndex, timeline.StageDemux, t.ID, -1, "stts",
			fmt.Errorf("sample timing box missing"))
	}
	sttsBox, ok := stts.box.(*mp4Stts)
	if !ok {
		return timeline.NewLocError(timeline.CodeTruncatedIndex, timeline.StageDemux, t.ID, -1, "stts",
			fmt.Errorf("payload unreadable"))
	}
	count, entries, err := flattenStts(t.ID, sttsBox)
	if err != nil {
		return err
	}
	if count > MaxSamplesPerTrack {
		return timeline.NewLocError(timeline.CodeUncheckedInput, timeline.StageDemux, t.ID, -1, "sample-count",
			fmt.Errorf("%w: %d > %d", ErrInputLimit, count, MaxSamplesPerTrack))
	}
	sizes, err := flattenStsz(t.ID, boxes, count)
	if err != nil {
		return err
	}
	keys, err := flattenStss(t.ID, boxes, count)
	if err != nil {
		return err
	}
	samples := make([]timeline.TrackSample, 0, count)
	var nativeDTS uint64
	for i := 0; i < count; i++ {
		nativeDur := uint64(entries[i])
		commonDTS := clock.ToCommon(nativeDTS)
		commonDur := clock.ToCommon(nativeDTS+nativeDur) - commonDTS
		if commonDur == 0 {
			commonDur = 1 // preserve sample presence on the integer clock
		}
		samples = append(samples, timeline.TrackSample{
			DTS:      commonDTS,
			CTS:      commonDTS,
			Dur:      commonDur,
			Size:     sizes[i],
			Keyframe: keys[i],
		})
		nativeDTS += nativeDur
	}
	t.Samples = samples
	return nil
}

// flattenStts expands the run-length stts table into one delta per sample.
// EntryCount is the number of (count, delta) runs, not the sample total.
func flattenStts(trackID uint32, b *mp4Stts) (int, []uint32, error) {
	if int(b.EntryCount) != len(b.Entries) {
		return 0, nil, timeline.NewLocError(timeline.CodeTruncatedIndex, timeline.StageDemux,
			trackID, -1, "stts-count",
			fmt.Errorf("entry_count %d != run payload %d: truncated index", b.EntryCount, len(b.Entries)))
	}
	total := 0
	for _, e := range b.Entries {
		if e.SampleCount == 0 {
			return 0, nil, timeline.NewLocError(timeline.CodeTruncatedIndex, timeline.StageDemux,
				trackID, -1, "stts-entry", fmt.Errorf("zero run count"))
		}
		if e.SampleDelta == 0 {
			return 0, nil, timeline.NewLocError(timeline.CodeTruncatedIndex, timeline.StageDemux,
				trackID, total, "stts-delta", fmt.Errorf("zero sample delta"))
		}
		total += int(e.SampleCount)
		if total > MaxSamplesPerTrack {
			return 0, nil, timeline.NewLocError(timeline.CodeUncheckedInput, timeline.StageDemux,
				trackID, -1, "stts", fmt.Errorf("%w: run total %d", ErrInputLimit, total))
		}
	}
	out := make([]uint32, 0, total)
	for _, e := range b.Entries {
		for j := uint32(0); j < e.SampleCount; j++ {
			out = append(out, e.SampleDelta)
		}
	}
	return total, out, nil
}

// flattenStsz returns one coded size per sample; a uniform-size stsz is expanded.
func flattenStsz(trackID uint32, boxes []rawBox, count int) ([]uint32, error) {
	rb := findBox(boxes, "stsz")
	if rb == nil {
		return nil, timeline.NewLocError(timeline.CodeTruncatedIndex, timeline.StageDemux,
			trackID, -1, "stsz", fmt.Errorf("sample size box missing"))
	}
	b := rb.box.(*mp4Stsz)
	if b.SampleSize != 0 {
		out := make([]uint32, count)
		for i := range out {
			out[i] = b.SampleSize
		}
		return out, nil
	}
	if int(b.SampleCount) != count {
		return nil, timeline.NewLocError(timeline.CodeTruncatedIndex, timeline.StageDemux,
			trackID, -1, "stsz-count",
			fmt.Errorf("size count %d != timing count %d: truncated index", b.SampleCount, count))
	}
	if len(b.EntrySize) < count {
		return nil, timeline.NewLocError(timeline.CodeTruncatedIndex, timeline.StageDemux,
			trackID, len(b.EntrySize), "stsz-entries",
			fmt.Errorf("payload has %d sizes, need %d", len(b.EntrySize), count))
	}
	return append([]uint32(nil), b.EntrySize[:count]...), nil
}

// flattenStss returns keyframe flags per sample. Missing stss means every
// sample is a sync sample (audio and some video); an out-of-range entry is a
// truncated/corrupt index.
func flattenStss(trackID uint32, boxes []rawBox, count int) ([]bool, error) {
	out := make([]bool, count)
	rb := findBox(boxes, "stss")
	if rb == nil {
		for i := range out {
			out[i] = true
		}
		return out, nil
	}
	b := rb.box.(*mp4Stss)
	if int(b.EntryCount) != len(b.SampleNumber) {
		return nil, timeline.NewLocError(timeline.CodeTruncatedIndex, timeline.StageDemux,
			trackID, -1, "stss-count",
			fmt.Errorf("entry_count %d != payload %d: truncated index", b.EntryCount, len(b.SampleNumber)))
	}
	for _, n := range b.SampleNumber {
		if n == 0 || int(n) > count {
			return nil, timeline.NewLocError(timeline.CodeTruncatedIndex, timeline.StageDemux,
				trackID, -1, "stss-range",
				fmt.Errorf("sync sample %d out of range [1,%d]", n, count))
		}
		out[n-1] = true
	}
	return out, nil
}
