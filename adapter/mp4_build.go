package adapter

import (
	"math"

	mp4 "github.com/abema/go-mp4"

	"mediacore/media"
)

// expandTables turns stts/stsc/stsz/stco into one Sample per sample,
// preserving presentation timing and flagging every out-of-range offset.
func (p *mp4Parser) expandTables(st *mp4TrackState) ([]Sample, int64, error) {
	if !st.hasStsd {
		return nil, 0, media.NewError(media.CodeMissingTable, st.trackID, nil,
			st.byteStart, "stsd box missing")
	}
	if len(st.stts) == 0 || st.stsz == nil || len(st.stsc) == 0 || len(st.stco) == 0 {
		return nil, 0, media.NewError(media.CodeMissingTable, st.trackID, nil,
			st.byteStart, "one of stts/stsc/stsz/stco is missing")
	}
	scale := media.TimeScale(st.timescale)
	count := st.stsz.SampleCount
	total, err := sumSTTS(st)
	if err != nil {
		return nil, 0, media.Errorf(media.CodeOverflow, st.trackID, nil, st.byteStart,
			"stts duration overflow: %v", err)
	}
	if total == 0 || count == 0 {
		return nil, 0, media.NewError(media.CodeMalformed, st.trackID, nil,
			st.byteStart, "empty sample table")
	}
	sizes, err := expandSizes(st.stsz)
	if err != nil {
		return nil, 0, media.Errorf(media.CodeMalformed, st.trackID, nil, st.byteStart,
			"stsz: %v", err)
	}
	if uint32(len(sizes)) != count {
		return nil, 0, media.Errorf(media.CodeMalformed, st.trackID, nil, st.byteStart,
			"stsz size list %d != sample_count %d", len(sizes), count)
	}
	durs, err := expandDeltas(st.stts, count)
	if err != nil {
		return nil, 0, media.Errorf(media.CodeMalformed, st.trackID, nil, st.byteStart,
			"stts: %v", err)
	}
	chunks, err := expandChunks(st.stsc, count, uint32(len(st.stco)))
	if err != nil {
		return nil, 0, media.Errorf(media.CodeMalformed, st.trackID, nil, st.byteStart,
			"stsc: %v", err)
	}

	samples := make([]Sample, 0, count)
	var tickCursor uint64
	for i := uint32(0); i < count; i++ {
		chunk := chunks[i]
		if chunk >= uint32(len(st.stco)) {
			return nil, 0, media.Errorf(media.CodeOffsetOutOfRange, st.trackID, nil,
				st.byteStart, "sample %d references chunk %d of %d",
				i, chunk, len(st.stco))
		}
		// stco carries absolute file offsets for every one-sample chunk.
		offset := int64(st.stco[chunk])
		if offset < 0 || offset >= p.fileSize || offset+int64(sizes[i]) > p.fileSize {
			return nil, 0, media.Errorf(media.CodeOffsetOutOfRange, st.trackID,
				&media.Interval{Start: int64(tickCursor)}, offset,
				"sample %d offset/size outside file", i)
		}
		pts, err := media.ToNanos(int64(tickCursor), scale)
		if err != nil {
			return nil, 0, media.Errorf(media.CodeOverflow, st.trackID, nil, offset,
				"pts conversion: %v", err)
		}
		dur, err := media.ToNanos(int64(durs[i]), scale)
		if err != nil {
			return nil, 0, media.Errorf(media.CodeOverflow, st.trackID, nil, offset,
				"duration conversion: %v", err)
		}
		_, key := st.stss[i+1]
		samples = append(samples, Sample{
			Index: i, ByteOffset: offset, Size: sizes[i],
			PTS: pts, Duration: dur, IsKeyframe: key || len(st.stss) == 0,
		})
		tickCursor += uint64(durs[i])
	}
	durNanos, err := media.ToNanos(int64(total), scale)
	if err != nil {
		return nil, 0, err
	}
	return samples, durNanos, nil
}

func sumSTTS(st *mp4TrackState) (uint64, error) {
	var total uint64
	for _, e := range st.stts {
		add := uint64(e.SampleCount) * uint64(e.SampleDelta)
		if add > math.MaxUint64-total {
			return 0, media.NewError(media.CodeOverflow, st.trackID, nil, -1, "stts sum")
		}
		total += add
	}
	return total, nil
}

func expandSizes(sz *mp4.Stsz) ([]uint32, error) {
	if sz.SampleSize != 0 {
		out := make([]uint32, sz.SampleCount)
		for i := range out {
			out[i] = sz.SampleSize
		}
		return out, nil
	}
	if uint32(len(sz.EntrySize)) != sz.SampleCount {
		return nil, media.NewError(media.CodeMalformed, 0, nil, -1, "entry size count mismatch")
	}
	return append([]uint32(nil), sz.EntrySize...), nil
}

func expandDeltas(entries []mp4.SttsEntry, count uint32) ([]uint32, error) {
	out := make([]uint32, 0, count)
	for _, e := range entries {
		if e.SampleCount == 0 || e.SampleDelta == 0 {
			return nil, media.NewError(media.CodeMalformed, 0, nil, -1, "zero stts run/delta")
		}
		if uint64(len(out))+uint64(e.SampleCount) > uint64(count) {
			return nil, media.NewError(media.CodeMalformed, 0, nil, -1, "stts covers too many samples")
		}
		for k := uint32(0); k < e.SampleCount; k++ {
			out = append(out, e.SampleDelta)
		}
	}
	if uint32(len(out)) != count {
		return nil, media.NewError(media.CodeMalformed, 0, nil, -1, "stts sample count mismatch")
	}
	return out, nil
}

// expandChunks resolves the chunk index for every sample from stsc runs.
func expandChunks(entries []mp4.StscEntry, count, chunkCount uint32) ([]uint32, error) {
	if entries[0].FirstChunk != 1 || entries[0].SamplesPerChunk != 1 {
		return nil, media.NewError(media.CodeMalformed, 0, nil, -1,
			"only one-sample chunks are supported")
	}
	out := make([]uint32, count)
	for i := range out {
		if uint32(i) >= chunkCount {
			return nil, media.NewError(media.CodeOffsetOutOfRange, 0, nil, -1,
				"more samples than chunk offsets")
		}
		out[i] = uint32(i)
	}
	return out, nil
}
