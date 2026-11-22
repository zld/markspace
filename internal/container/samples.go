package container

import (
	"fmt"

	mp4 "github.com/abema/go-mp4"

	"mediamerge/internal/clock"
)

// Sample is one expanded media sample on the shared rational time base.
type Sample struct {
	Index     uint32
	Timestamp clock.Rational // exact seconds from track epoch
	Offset    uint64         // byte offset in file
	Size      uint32
}

// SampleTable is the fully expanded, validated sample map of one track.
type SampleTable struct {
	TrackID    uint32
	Samples    []Sample
	TotalBytes uint64
	Duration   clock.Rational // exact, sum of stts deltas over timescale
}

// ExpandSamples flattens stts/stsc/stco/stsz into per-sample entries and
// validates every sample offset against fileSize. Expansion is capped by
// lim.MaxSamples to reject overflow-sized tables before allocating.
func ExpandSamples(t *TrackInfo, fileSize int64, lim Limits) (*SampleTable, error) {
	count, err := sampleCount(t)
	if err != nil {
		return nil, err
	}
	if count > lim.MaxSamples {
		return nil, &BoxError{TrackID: t.TrackID, BoxType: "stsz", Offset: 0,
			Msg: fmt.Sprintf("sample count %d exceeds limit %d", count, lim.MaxSamples)}
	}
	sizes, err := expandSizes(t, count)
	if err != nil {
		return nil, err
	}
	offsets, err := expandOffsets(t, sizes, count)
	if err != nil {
		return nil, err
	}
	st := &SampleTable{TrackID: t.TrackID, Samples: make([]Sample, 0, count)}
	var ticks int64
	for i := uint32(0); i < count; i++ {
		end := offsets[i] + uint64(sizes[i])
		if end > uint64(fileSize) || end < offsets[i] {
			return nil, &BoxError{TrackID: t.TrackID, BoxType: "stco",
				Offset: offsets[i],
				Msg: fmt.Sprintf("sample %d [%d,%d) beyond file size %d",
					i, offsets[i], end, fileSize)}
		}
		ts, err := clock.FromTicks(ticks, t.Timescale)
		if err != nil {
			return nil, err
		}
		st.Samples = append(st.Samples, Sample{
			Index: i, Timestamp: ts, Offset: offsets[i], Size: sizes[i],
		})
		st.TotalBytes += uint64(sizes[i])
		ticks += int64(deltaFor(t.Stts, i))
	}
	st.Duration, err = clock.FromTicks(ticks, t.Timescale)
	if err != nil {
		return nil, err
	}
	return st, nil
}

// sampleCount totals stts entries with overflow checks.
func sampleCount(t *TrackInfo) (uint32, error) {
	var total uint64
	for _, e := range t.Stts {
		total += uint64(e.SampleCount)
		if total > 1<<32-1 {
			return 0, &BoxError{TrackID: t.TrackID, BoxType: "stts", Offset: 0,
				Msg: "sample count overflows uint32"}
		}
	}
	if sz := t.SampleSize; sz == 0 && uint32(len(t.EntrySize)) != uint32(total) {
		return 0, &BoxError{TrackID: t.TrackID, BoxType: "stsz", Offset: 0,
			Msg: fmt.Sprintf("stsz entries %d != stts samples %d", len(t.EntrySize), total)}
	}
	return uint32(total), nil
}

func expandSizes(t *TrackInfo, count uint32) ([]uint32, error) {
	sizes := make([]uint32, count)
	if t.SampleSize != 0 {
		for i := range sizes {
			sizes[i] = t.SampleSize
		}
		return sizes, nil
	}
	copy(sizes, t.EntrySize)
	return sizes, nil
}

// expandOffsets maps samples to byte offsets via stsc chunk mapping.
func expandOffsets(t *TrackInfo, sizes []uint32, count uint32) ([]uint64, error) {
	if len(t.Stsc) == 0 || len(t.ChunkOffsets) == 0 {
		if count == 0 {
			return nil, nil
		}
		return nil, &BoxError{TrackID: t.TrackID, BoxType: "stsc", Offset: 0,
			Msg: "missing chunk mapping for non-empty sample table"}
	}
	offsets := make([]uint64, 0, count)
	var idx uint32
	for ci := 0; ci < len(t.ChunkOffsets) && idx < count; ci++ {
		chunk := uint32(ci) + 1 // stsc FirstChunk is 1-based
		spc := samplesPerChunk(t.Stsc, chunk)
		off := t.ChunkOffsets[ci]
		for s := uint32(0); s < spc && idx < count; s++ {
			offsets = append(offsets, off)
			off += uint64(sizes[idx])
			idx++
		}
	}
	if idx < count {
		return nil, &BoxError{TrackID: t.TrackID, BoxType: "stsc", Offset: 0,
			Msg: fmt.Sprintf("chunk mapping covers %d of %d samples", idx, count)}
	}
	return offsets, nil
}

// samplesPerChunk finds the stsc entry governing chunk (1-based).
func samplesPerChunk(stsc []mp4.StscEntry, chunk uint32) uint32 {
	spc := stsc[0].SamplesPerChunk
	for _, e := range stsc {
		if e.FirstChunk <= chunk {
			spc = e.SamplesPerChunk
		} else {
			break
		}
	}
	return spc
}

// deltaFor returns the stts sample delta for sample i.
func deltaFor(stts []mp4.SttsEntry, i uint32) uint32 {
	var run uint32
	for _, e := range stts {
		if i < run+e.SampleCount {
			return e.SampleDelta
		}
		run += e.SampleCount
	}
	return 0
}
