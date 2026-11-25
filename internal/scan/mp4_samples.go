package scan

import (
	"os"

	"github.com/abema/go-mp4"

	"mediacore/internal/clock"
	"mediacore/internal/fault"
)

func appendSamples(f *os.File, idx *Index, path string, tr Track, trak *mp4.BoxInfo, stsz *mp4.Stsz, stts *mp4.Stts, stsc *mp4.Stsc, chunks []uint64) error {
	sampleCount := int(stsz.SampleCount)
	dts := expandStts(stts, sampleCount)
	cts := []int64{}
	if ctts, _, _ := firstPayloadWithInfo[mp4.Ctts](f, trak, mp4.BoxPath{mp4.BoxTypeMdia(), mp4.BoxTypeMinf(), mp4.BoxTypeStbl(), mp4.BoxTypeCtts()}); ctts != nil {
		cts = expandCtts(ctts, sampleCount)
	}
	keySet := map[uint32]bool{}
	if stss, _, _ := firstPayloadWithInfo[mp4.Stss](f, trak, mp4.BoxPath{mp4.BoxTypeMdia(), mp4.BoxTypeMinf(), mp4.BoxTypeStbl(), mp4.BoxTypeStss()}); stss != nil {
		for _, n := range stss.SampleNumber {
			keySet[n] = true
		}
	}
	if len(dts) != sampleCount {
		return fault.At(fault.New(fault.CorruptHeader, "stts sample count does not match stsz"), path, int64(trak.Offset))
	}

	sizes := make([]uint32, sampleCount)
	for i := 0; i < sampleCount; i++ {
		if stsz.SampleSize != 0 {
			sizes[i] = stsz.SampleSize
		} else if i < len(stsz.EntrySize) {
			sizes[i] = stsz.EntrySize[i]
		} else {
			return fault.At(fault.New(fault.CorruptHeader, "stsz entry truncated"), path, int64(trak.Offset))
		}
	}

	tb := clock.Timebase{Timescale: tr.Timescale}
	sampleIdx := 0
	for chunkNumber := uint32(1); chunkNumber <= uint32(len(chunks)); chunkNumber++ {
		entry := stsc.Entries[0]
		for _, e := range stsc.Entries {
			if e.FirstChunk <= chunkNumber {
				entry = e
			}
		}
		offset := int64(chunks[chunkNumber-1])
		for j := uint32(0); j < entry.SamplesPerChunk; j++ {
			if sampleIdx >= sampleCount {
				return fault.At(fault.New(fault.CorruptHeader, "stsc references too many samples"), path, int64(trak.Offset))
			}
			size := int64(sizes[sampleIdx])
			if offset < 0 || size <= 0 || offset+size > idx.SourceSize {
				return fault.At(fault.New(fault.TruncatedData, "sample payload outside file"), path, offset)
			}
			ptsUnits := dts[sampleIdx]
			if sampleIdx < len(cts) {
				ptsUnits += cts[sampleIdx]
			}
			dur := durationFor(stts, sampleIdx)
			idx.Samples = append(idx.Samples, SampleRef{
				TrackID:       tr.ID,
				Index:         uint32(sampleIdx),
				Offset:        offset,
				Size:          uint32(size),
				PTSUnits:      ptsUnits,
				DTSUnits:      dts[sampleIdx],
				DurationUnits: dur,
				DurationNS:    tb.ToNS(int64(dur)),
				PTSNS:         tb.ToNS(ptsUnits),
				DTSNS:         tb.ToNS(dts[sampleIdx]),
				KeyFrame:      len(keySet) == 0 || keySet[uint32(sampleIdx+1)],
			})
			offset += size
			sampleIdx++
		}
	}
	if sampleIdx != sampleCount {
		return fault.At(fault.New(fault.CorruptHeader, "not all stsz samples are covered by stsc/stco"), path, int64(trak.Offset))
	}
	return nil
}

func expandStts(stts *mp4.Stts, want int) []int64 {
	out := make([]int64, 0, want)
	var dts int64
	for _, e := range stts.Entries {
		for i := uint32(0); i < e.SampleCount && len(out) < want; i++ {
			out = append(out, dts)
			dts += int64(e.SampleDelta)
		}
	}
	return out
}

func durationFor(stts *mp4.Stts, sample int) uint32 {
	n := 0
	for _, e := range stts.Entries {
		for i := uint32(0); i < e.SampleCount; i++ {
			if n == sample {
				return e.SampleDelta
			}
			n++
		}
	}
	return 0
}

func expandCtts(ctts *mp4.Ctts, want int) []int64 {
	out := make([]int64, 0, want)
	for _, e := range ctts.Entries {
		off := int64(e.SampleOffsetV0)
		if ctts.Version == 1 {
			off = int64(e.SampleOffsetV1)
		}
		for i := uint32(0); i < e.SampleCount && len(out) < want; i++ {
			out = append(out, off)
		}
	}
	return out
}
