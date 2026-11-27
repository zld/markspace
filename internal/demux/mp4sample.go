package demux

import (
	"io"

	"mediapack/internal/model"
)

// readSamples expands the stsc/stco/stsz/stts tables into per-sample records,
// validates every offset against the mdat extent and the file size, then reads
// each compressed payload verbatim via ReaderAt.
func (t *mp4Track) readSamples(c *Container, ra io.ReaderAt,
	mdatPayload, mdatEnd, fileSize int64, path string,
) ([]model.Sample, error) {
	tid := t.tkhd.TrackID
	if err := t.checkTables(path); err != nil {
		return nil, err
	}
	sizes := t.expandSizes(path, tid)
	chunkOffsets, err := t.chunkOffsets(path, tid)
	if err != nil {
		return nil, err
	}
	chunks, err := t.expandChunks(len(chunkOffsets), len(sizes), path, tid)
	if err != nil {
		return nil, err
	}
	dtsList, durList := t.expandTimes(len(sizes))

	out := make([]model.Sample, 0, len(sizes))
	sampleIdx := 0
	for chunkIdx, nInChunk := range chunks {
		base := chunkOffsets[chunkIdx]
		cursor := int64(base)
		if base < mdatPayload || base >= mdatEnd {
			return nil, model.NewError(model.CodeOutOfRangeSample,
				"chunk offset outside mdat").
				WithFile(path).WithTrack(tid).WithOffset(int64(base))
		}
		for k := 0; k < nInChunk; k++ {
			size := sizes[sampleIdx]
			end := cursor + int64(size)
			if end > mdatEnd || end > fileSize {
				return nil, model.NewError(model.CodeOutOfRangeSample,
					"sample extends past mdat/file").
					WithFile(path).WithTrack(tid).WithOffset(cursor)
			}
			buf := make([]byte, size)
			if _, err := ra.ReadAt(buf, cursor); err != nil {
				return nil, model.NewError(model.CodeTruncatedBox,
					"cannot read sample bytes").
					WithFile(path).WithTrack(tid).WithOffset(cursor)
			}
			_, dtsTick := model.ScaleToMaster(dtsList[sampleIdx], t.mdhd.Timescale)
			out = append(out, model.Sample{
				Source:      path,
				SourceTrack: tid,
				Index:       uint32(sampleIdx),
				DTS:         dtsTick,
				Duration:    model.DeltaMaster(uint64(durList[sampleIdx]), t.mdhd.Timescale),
				SrcOffset:   cursor,
				Size:        size,
				Payload:     buf,
			})
			cursor = end
			sampleIdx++
		}
	}
	if sampleIdx != len(sizes) {
		return nil, model.NewError(model.CodeMalformedContainer,
			"stsc/stsz sample count disagreement").
			WithFile(path).WithTrack(tid)
	}
	return out, nil
}

func (t *mp4Track) checkTables(path string) error {
	tid := safeTrackID(t)
	if t.stts == nil || t.stsc == nil || t.stsz == nil {
		return model.NewError(model.CodeMalformedContainer,
			"trak missing stts/stsc/stsz").WithFile(path).WithTrack(tid)
	}
	if t.stco == nil && t.co64 == nil {
		return model.NewError(model.CodeMalformedContainer,
			"trak has neither stco nor co64").WithFile(path).WithTrack(tid)
	}
	if t.stco != nil && t.co64 != nil {
		return model.NewError(model.CodeMalformedContainer,
			"trak has both stco and co64").WithFile(path).WithTrack(tid)
	}
	return nil
}

// expandSizes resolves stsz into one size per sample.
func (t *mp4Track) expandSizes(path string, tid uint32) []uint32 {
	if t.stsz.SampleSize != 0 {
		out := make([]uint32, t.stsz.SampleCount)
		for i := range out {
			out[i] = t.stsz.SampleSize
		}
		return out
	}
	return append([]uint32(nil), t.stsz.EntrySize...)
}

func (t *mp4Track) chunkOffsets(path string, tid uint32) ([]int64, error) {
	if t.stco != nil {
		if int(t.stco.EntryCount) != len(t.stco.ChunkOffset) {
			return nil, model.NewError(model.CodeMalformedContainer,
				"stco entry count mismatch").WithFile(path).WithTrack(tid)
		}
		out := make([]int64, len(t.stco.ChunkOffset))
		for i, v := range t.stco.ChunkOffset {
			out[i] = int64(v)
		}
		return out, nil
	}
	if int(t.co64.EntryCount) != len(t.co64.ChunkOffset) {
		return nil, model.NewError(model.CodeMalformedContainer,
			"co64 entry count mismatch").WithFile(path).WithTrack(tid)
	}
	out := make([]int64, len(t.co64.ChunkOffset))
	for i, v := range t.co64.ChunkOffset {
		out[i] = int64(v)
	}
	return out, nil
}

// expandChunks turns stsc entries into the sample count for each chunk index.
func (t *mp4Track) expandChunks(chunkCount, sampleCount int, path string,
	tid uint32,
) ([]int, error) {
	perChunk := make([]int, chunkCount)
	if len(t.stsc.Entries) == 0 {
		return nil, model.NewError(model.CodeMalformedContainer,
			"empty stsc").WithFile(path).WithTrack(tid)
	}
	for i, e := range t.stsc.Entries {
		first := e.FirstChunk // 1-based
		var last uint32
		if i+1 < len(t.stsc.Entries) {
			last = t.stsc.Entries[i+1].FirstChunk // exclusive boundary
		} else {
			last = uint32(chunkCount) + 1
		}
		for ch := first; ch < last; ch++ {
			if int(ch-1) >= chunkCount {
				return nil, model.NewError(model.CodeOutOfRangeSample,
					"stsc first-chunk beyond chunk count").
					WithFile(path).WithTrack(tid)
			}
			perChunk[ch-1] = int(e.SamplesPerChunk)
		}
	}
	total := 0
	for _, n := range perChunk {
		total += n
	}
	if total != sampleCount {
		return nil, model.NewError(model.CodeMalformedContainer,
			"stsc sample total disagrees with stsz").
			WithFile(path).WithTrack(tid)
	}
	return perChunk, nil
}

// expandTimes materializes one DTS and duration per sample from stts runs.
func (t *mp4Track) expandTimes(sampleCount int) (dts, dur []int64) {
	dts = make([]int64, sampleCount)
	dur = make([]int64, sampleCount)
	idx := 0
	var clock int64
	for _, run := range t.stts.Entries {
		for k := uint32(0); k < run.SampleCount && idx < sampleCount; k++ {
			dts[idx] = clock
			dur[idx] = int64(run.SampleDelta)
			clock += int64(run.SampleDelta)
			idx++
		}
	}
	return dts, dur
}
