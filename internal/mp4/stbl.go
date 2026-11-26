package mp4

import (
	go_mp4 "github.com/abema/go-mp4"

	"regress/internal/media"
)

// fillSamples resolves sample sizes, chunk offsets and per-sample durations.
func fillSamples(r readSeeker, source string, stbl *boxHeader, ts *TrackState) error {
	sizes, err := readStsz(r, source, stbl)
	if err != nil {
		return err
	}
	offsets, err := readOffsets(r, source, stbl)
	if err != nil {
		return err
	}
	durations, err := readStts(r, source, stbl, len(sizes))
	if err != nil {
		return err
	}
	if len(offsets) != len(sizes) {
		// stsc expansion should already have produced one offset per sample;
		// a mismatch means the sample tables disagree.
		return media.CorruptBox(source, int64(stbl.offset),
			"sample table size/offset count mismatch")
	}
	pay := media.NewDigester(media.KindPayload)
	ts.Samples = make([]Sample, len(sizes))
	for i := range sizes {
		ts.Samples[i] = Sample{
			Index:    i,
			Size:     sizes[i],
			Offset:   offsets[i],
			Duration: durations[i],
		}
		b, err := bytesAt(r, offsets[i], int(sizes[i]))
		if err != nil {
			return media.Truncated(source, "sample bytes unreadable")
		}
		pay.Write(b)
	}
	ts.SampleCount = len(ts.Samples)
	ts.PayloadDigest = pay.Sum()
	return nil
}

// readStsz reads uniform or per-sample sizes from the stsz box.
func readStsz(r readSeeker, source string, stbl *boxHeader) ([]uint32, error) {
	box, err := findChild(r, stbl, go_mp4.BoxTypeStsz())
	if err != nil {
		return nil, asTrackErr(source, err)
	}
	if box == nil {
		return nil, media.CorruptBox(source, int64(stbl.offset), "stbl has no stsz")
	}
	var stsz go_mp4.Stsz
	if err := unmarshalBox(r, box, &stsz); err != nil {
		// The declared length cannot contain the advertised entries: point at
		// the exact size field offset of the stsz box.
		return nil, media.CorruptBox(source, int64(box.offset),
			"stsz length field corrupt: "+err.Error())
	}
	if stsz.SampleSize != 0 {
		out := make([]uint32, stsz.SampleCount)
		for i := range out {
			out[i] = stsz.SampleSize
		}
		return out, nil
	}
	if uint32(len(stsz.EntrySize)) != stsz.SampleCount {
		return nil, media.CorruptBox(source, int64(box.offset),
			"stsz entry count disagrees with declared sample count")
	}
	return append([]uint32(nil), stsz.EntrySize...), nil
}

// readOffsets expands stco/co64 chunk offsets with stsc sample-to-chunk maps.
func readOffsets(r readSeeker, source string, stbl *boxHeader) ([]uint64, error) {
	chunks, err := readChunkOffsets(r, source, stbl)
	if err != nil {
		return nil, err
	}
	perChunk, err := readStsc(r, source, stbl, len(chunks))
	if err != nil {
		return nil, err
	}
	out := make([]uint64, 0, len(chunks))
	// Samples are contiguous within a chunk; size is filled in fillSamples.
	for ci, base := range chunks {
		for s := 0; s < perChunk[ci]; s++ {
			out = append(out, base)
		}
	}
	return out, nil
}

func readChunkOffsets(r readSeeker, source string, stbl *boxHeader) ([]uint64, error) {
	if box, _ := findChild(r, stbl, go_mp4.BoxTypeCo64()); box != nil {
		var co64 go_mp4.Co64
		if err := unmarshalBox(r, box, &co64); err != nil {
			return nil, media.CorruptBox(source, int64(box.offset), "co64 corrupt: "+err.Error())
		}
		return append([]uint64(nil), co64.ChunkOffset...), nil
	}
	box, err := findChild(r, stbl, go_mp4.BoxTypeStco())
	if err != nil {
		return nil, asTrackErr(source, err)
	}
	if box == nil {
		return nil, media.CorruptBox(source, int64(stbl.offset), "stbl has no stco/co64")
	}
	var stco go_mp4.Stco
	if err := unmarshalBox(r, box, &stco); err != nil {
		return nil, media.CorruptBox(source, int64(box.offset), "stco corrupt: "+err.Error())
	}
	out := make([]uint64, len(stco.ChunkOffset))
	for i, v := range stco.ChunkOffset {
		out[i] = uint64(v)
	}
	return out, nil
}

// readStsc expands the sample-to-chunk table into samples-per-chunk counts.
func readStsc(r readSeeker, source string, stbl *boxHeader, chunkCount int) ([]int, error) {
	box, err := findChild(r, stbl, go_mp4.BoxTypeStsc())
	if err != nil {
		return nil, asTrackErr(source, err)
	}
	if box == nil {
		// Default: one sample per chunk.
		out := make([]int, chunkCount)
		for i := range out {
			out[i] = 1
		}
		return out, nil
	}
	var stsc go_mp4.Stsc
	if err := unmarshalBox(r, box, &stsc); err != nil {
		return nil, media.CorruptBox(source, int64(box.offset), "stsc corrupt: "+err.Error())
	}
	perChunk := make([]int, chunkCount)
	entry := 0
	for chunk := 0; chunk < chunkCount; chunk++ {
		for entry+1 < len(stsc.Entries) &&
			uint32(chunk+1) >= stsc.Entries[entry+1].FirstChunk {
			entry++
		}
		perChunk[chunk] = int(stsc.Entries[entry].SamplesPerChunk)
	}
	return perChunk, nil
}

// readStts expands the decode time-to-sample table into per-sample durations.
func readStts(r readSeeker, source string, stbl *boxHeader, sampleCount int) ([]uint32, error) {
	box, err := findChild(r, stbl, go_mp4.BoxTypeStts())
	if err != nil {
		return nil, asTrackErr(source, err)
	}
	out := make([]uint32, sampleCount)
	if box == nil {
		return out, nil // no timing table: zero durations, boundaries still built
	}
	var stts go_mp4.Stts
	if err := unmarshalBox(r, box, &stts); err != nil {
		return nil, media.CorruptBox(source, int64(box.offset), "stts corrupt: "+err.Error())
	}
	i := 0
	for _, e := range stts.Entries {
		for k := uint32(0); k < e.SampleCount && i < sampleCount; k++ {
			out[i] = e.SampleDelta
			i++
		}
	}
	return out, nil
}
