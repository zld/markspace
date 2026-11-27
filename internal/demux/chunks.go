package demux

import (
	"fmt"
	"io"
	"strings"

	"github.com/abema/go-mp4"
	"github.com/zld/continuity/internal/clock"
)

// expandStsz resolves one size per sample. A constant SampleSize means every
// row shares it; otherwise the entry array must cover the whole stts range.
func expandStsz(byType map[mp4.BoxType]*mp4.BoxInfoWithPayload, count int, trackID uint32) ([]uint32, error) {
	box, ok := byType[mp4.BoxTypeStsz()]
	if !ok {
		return nil, clock.NewError(clock.StageDemux, "stsz-missing",
			fmt.Errorf("%w: no stsz box", clock.ErrCorrupt)).WithTrack(trackID)
	}
	stsz := box.Payload.(*mp4.Stsz)
	sizes := make([]uint32, count)
	switch {
	case stsz.SampleSize != 0:
		for i := range sizes {
			sizes[i] = stsz.SampleSize
		}
	case len(stsz.EntrySize) < count:
		return nil, clock.NewError(clock.StageDemux, "stsz-range",
			fmt.Errorf("%w: stsz has %d entries for %d samples",
				clock.ErrCorrupt, len(stsz.EntrySize), count)).
			WithTrack(trackID).WithSample(uint32(len(stsz.EntrySize)))
	default:
		copy(sizes, stsz.EntrySize[:count])
	}
	for i, sz := range sizes {
		if sz == 0 {
			return nil, clock.NewError(clock.StageDemux, "stsz-zero",
				fmt.Errorf("%w: sample %d has zero size", clock.ErrCorrupt, i)).
				WithTrack(trackID).WithSample(uint32(i))
		}
	}
	return sizes, nil
}

// expandOffsets walks stsc/stco|co64 to produce a contiguous per-sample file
// offset. It rejects truncated chunk tables and sample counts that do not match
// the stts/stsz declarations.
func expandOffsets(rs io.ReadSeeker, trak *mp4.BoxInfo,
	byType map[mp4.BoxType]*mp4.BoxInfoWithPayload, sizes []uint32, trackID uint32) ([]uint64, error) {
	stscBox, ok := byType[mp4.BoxTypeStsc()]
	if !ok {
		return nil, clock.NewError(clock.StageDemux, "stsc-missing",
			fmt.Errorf("%w: no stsc box", clock.ErrCorrupt)).WithTrack(trackID)
	}
	stsc := stscBox.Payload.(*mp4.Stsc)

	var chunkOff []uint64
	if co, ok := byType[mp4.BoxTypeStco()]; ok {
		for _, v := range co.Payload.(*mp4.Stco).ChunkOffset {
			chunkOff = append(chunkOff, uint64(v))
		}
	} else if c64, ok := byType[mp4.BoxTypeCo64()]; ok {
		chunkOff = append(chunkOff, c64.Payload.(*mp4.Co64).ChunkOffset...)
	} else {
		return nil, clock.NewError(clock.StageDemux, "stco-missing",
			fmt.Errorf("%w: no stco/co64 box", clock.ErrCorrupt)).WithTrack(trackID)
	}

	// Expand stsc into samples-per-chunk for every chunk index.
	perChunk, err := samplesPerChunk(stsc.Entries, uint32(len(chunkOff)), trackID)
	if err != nil {
		return nil, err
	}
	var total uint32
	for _, n := range perChunk {
		total += n
	}
	if int(total) != len(sizes) {
		return nil, clock.NewError(clock.StageDemux, "sample-count",
			fmt.Errorf("%w: chunks hold %d samples but stts/stsz declare %d",
				clock.ErrCorrupt, total, len(sizes))).
			WithTrack(trackID).WithSample(minU32(total, uint32(len(sizes))))
	}

	offsets := make([]uint64, 0, len(sizes))
	sampleIdx := uint32(0)
	for chunk, spc := range perChunk {
		off := chunkOff[chunk]
		for k := uint32(0); k < spc; k++ {
			offsets = append(offsets, off)
			off += uint64(sizes[sampleIdx])
			sampleIdx++
		}
	}
	return offsets, nil
}

// samplesPerChunk expands the run-length stsc table into a flat array indexed
// by chunk. FirstChunk values are one-based.
func samplesPerChunk(entries []mp4.StscEntry, chunkCount, trackID uint32) ([]uint32, error) {
	if len(entries) == 0 {
		return nil, clock.NewError(clock.StageDemux, "stsc-empty",
			fmt.Errorf("%w: stsc has no entries", clock.ErrCorrupt)).WithTrack(trackID)
	}
	out := make([]uint32, chunkCount)
	for i := range entries {
		start := entries[i].FirstChunk
		if start == 0 {
			return nil, clock.NewError(clock.StageDemux, "stsc-first-chunk",
				fmt.Errorf("%w: FirstChunk must be >= 1", clock.ErrCorrupt)).WithTrack(trackID)
		}
		var end uint32 = chunkCount + 1 // stsc chunks are 1-based, exclusive here
		if i+1 < len(entries) {
			end = entries[i+1].FirstChunk
		}
		if start > chunkCount || end > chunkCount+1 || end <= start {
			return nil, clock.NewError(clock.StageDemux, "stsc-range",
				fmt.Errorf("%w: stsc run %d..%d invalid for %d chunks",
					clock.ErrCorrupt, start, end-1, chunkCount)).WithTrack(trackID)
		}
		for c := start - 1; c < end-1; c++ {
			out[c] = entries[i].SamplesPerChunk
		}
	}
	for c, n := range out {
		if n == 0 {
			return nil, clock.NewError(clock.StageDemux, "stsc-coverage",
				fmt.Errorf("%w: chunk %d uncovered by stsc", clock.ErrCorrupt, c)).
				WithTrack(trackID)
		}
	}
	return out, nil
}

func minU32(a, b uint32) uint32 {
	if a < b {
		return a
	}
	return b
}

// mapReadErr turns go-mp4's short-read and truncated-array failures into the
// shared corrupt-input sentinel so the command layer can pick the documented
// exit code. Structural faults (bad entry counts, cut boxes) all surface here.
func mapReadErr(err error) error {
	if err == nil {
		return nil
	}
	if isUnexpectedEOF(err) || strings.Contains(err.Error(), "failed to read array") {
		return fmt.Errorf("%w: %v", clock.ErrCorrupt, err)
	}
	return err
}

func isUnexpectedEOF(err error) bool {
	return err == io.ErrUnexpectedEOF || err == io.EOF
}
