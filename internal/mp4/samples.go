package mp4scan

import (
	"github.com/abema/go-mp4"

	"archivecheck/internal/clock"
	apperrors "archivecheck/internal/errors"
)

func buildSamples(r *ScanResult, tb clock.Timebase, stts *mp4.Stts, ctts *mp4.Ctts, stss *mp4.Stss, stsz *mp4.Stsz) *apperrors.Error {
	if stts == nil || stsz == nil {
		return apperrors.E(apperrors.KindTruncated, "sample time or size table is missing")
	}

	type timed struct{ dts, offset int64 }
	var times []timed
	var dts int64
	for _, e := range stts.Entries {
		for i := uint32(0); i < e.SampleCount; i++ {
			off := int64(0)
			if ctts != nil {
				off = cttsOffsetAt(ctts, len(times))
			}
			times = append(times, timed{dts: dts, offset: off})
			dts += int64(e.SampleDelta)
		}
	}

	var sizes []uint32
	if stsz.SampleSize != 0 {
		for i := uint32(0); i < stsz.SampleCount; i++ {
			sizes = append(sizes, stsz.SampleSize)
		}
	} else {
		sizes = append(sizes, stsz.EntrySize...)
	}
	if uint32(len(times)) != stsz.SampleCount || uint32(len(sizes)) != stsz.SampleCount {
		return apperrors.E(apperrors.KindTruncated,
			"sample tables disagree on sample count")
	}
	if len(times) == 0 {
		return apperrors.E(apperrors.KindNoKeyframe, "sample table contains no samples")
	}
	if n := len(stts.Entries); n > 0 {
		r.SampleDurationTick = clock.Tick(tb.ToShared(int64(stts.Entries[n-1].SampleDelta)))
	}

	sync := map[uint32]bool{}
	if stss != nil {
		for _, n := range stss.SampleNumber {
			sync[n] = true
			r.Keyframes = append(r.Keyframes, n)
		}
	}
	for i, tm := range times {
		n := uint32(i + 1)
		key := stss == nil || sync[n] // absent stss means every sample is sync
		r.Samples = append(r.Samples, Sample{
			Index:    n,
			DTS:      int64(tb.ToShared(tm.dts)),
			PTS:      int64(tb.ToShared(tm.dts + tm.offset)),
			Size:     sizes[i],
			Keyframe: key,
		})
		if key && stss == nil {
			r.Keyframes = append(r.Keyframes, n)
		}
	}
	return nil
}

func cttsOffsetAt(ctts *mp4.Ctts, sampleIndex int) int64 {
	remaining := uint32(sampleIndex)
	for _, e := range ctts.Entries {
		if remaining < e.SampleCount {
			return ctts.GetSampleOffset(int(indexOfEntry(ctts, e)))
		}
		remaining -= e.SampleCount
	}
	return 0
}

func indexOfEntry(ctts *mp4.Ctts, target mp4.CttsEntry) int {
	for i, e := range ctts.Entries {
		if e == target {
			return i
		}
	}
	return 0
}

func validateKeyframes(r *ScanResult) *apperrors.Error {
	for _, s := range r.Samples {
		if s.Keyframe {
			return nil
		}
	}
	return apperrors.E(apperrors.KindNoKeyframe, "track has no decodable sync sample")
}

// RecoverKeyframe returns the latest keyframe at or before requestTick. This is
// the decode preroll point; recovery then decodes forward to the requested cut.
func (r *ScanResult) RecoverKeyframe(requestTick int64) (Sample, error) {
	var found *Sample
	for i := range r.Samples {
		s := &r.Samples[i]
		if !s.Keyframe {
			continue
		}
		if s.PTS <= requestTick {
			found = s
		}
	}
	if found == nil {
		return Sample{}, apperrors.E(apperrors.KindNoKeyframe,
			"no keyframe at or before requested boundary").WithStage("recovery")
	}
	return *found, nil
}
