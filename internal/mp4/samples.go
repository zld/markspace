package mp4

import (
	"fmt"
	"io"

	gomp4 "github.com/abema/go-mp4"

	"mediad/internal/clock"
)

func readSampleTables(r io.ReadSeeker, trak *gomp4.BoxInfo, tr *RawTrack) error {
	paths := []gomp4.BoxPath{
		p("mdia", "minf", "stbl", "stsd"),
		p("mdia", "minf", "stbl", "stts"),
		p("mdia", "minf", "stbl", "stsz"),
		p("mdia", "minf", "stbl", "stsc"),
		p("mdia", "minf", "stbl", "stco"),
		p("mdia", "minf", "stbl", "co64"),
		p("mdia", "minf", "stbl", "stss"),
	}
	bis, err := gomp4.ExtractBoxesWithPayload(r, trak, paths)
	if err != nil {
		return wrapParse(err, int64(trak.Offset), tr.TrackID)
	}
	var stts *gomp4.Stts
	var sttsOff int64
	var stsz *gomp4.Stsz
	var stsc *gomp4.Stsc
	var offsets []uint64
	var stcoOff int64
	var stss []uint32
	hasStss := false
	hasStsd := false
	for _, b := range bis {
		switch v := b.Payload.(type) {
		case *gomp4.Stsd:
			hasStsd = true
		case *gomp4.Stts:
			stts, sttsOff = v, int64(b.Info.Offset)
		case *gomp4.Stsz:
			stsz = v
		case *gomp4.Stsc:
			stsc = v
		case *gomp4.Stco:
			stcoOff = int64(b.Info.Offset)
			for _, c := range v.ChunkOffset {
				offsets = append(offsets, uint64(c))
			}
		case *gomp4.Co64:
			stcoOff = int64(b.Info.Offset)
			offsets = append(offsets, v.ChunkOffset...)
		case *gomp4.Stss:
			hasStss = true
			stss = v.SampleNumber
		}
	}
	if !hasStsd {
		return clock.New(clock.StageScan, clock.CodeBadManifest, "track %d has no stsd", tr.TrackID).
			WithOffset(int64(trak.Offset)).WithTrack(tr.TrackID)
	}
	if stts == nil || stsz == nil || stsc == nil || len(offsets) == 0 {
		return clock.New(clock.StageScan, clock.CodeTruncatedTable,
			"track %d is missing a required sample table (stts=%v stsz=%v stsc=%v chunks=%d)",
			tr.TrackID, stts != nil, stsz != nil, stsc != nil, len(offsets)).
			WithOffset(int64(trak.Offset)).WithTrack(tr.TrackID)
	}
	tr.TableBytes.Stts = sttsOff
	tr.TableBytes.Stco = stcoOff
	tr.HasSyncTable = hasStss

	sampleCount := int(stsz.SampleCount)
	if sampleCount > MaxSamplesPerTrack {
		return clock.New(clock.StageScan, clock.CodeOversizedTable,
			"track %d declares %d samples (limit %d)", tr.TrackID, sampleCount, MaxSamplesPerTrack).
			WithOffset(sttsOff).WithTrack(tr.TrackID)
	}
	// stsz entry count must match its declared count, otherwise the table was
	// truncated during collection.
	if stsz.SampleSize == 0 && len(stsz.EntrySize) != sampleCount {
		return clock.New(clock.StageScan, clock.CodeTruncatedTable,
			"track %d stsz declares %d sizes but carries %d", tr.TrackID, sampleCount, len(stsz.EntrySize)).
			WithOffset(sttsOff).WithTrack(tr.TrackID)
	}

	// DTS from stts with a byte offset per run entry.
	dts := make([]int64, sampleCount)
	cpOff := make([]int64, sampleCount)
	var t int64
	idx := 0
	runBase := sttsOff + 16 // header(8) + version/flags(4) + entry_count(4)
	for runIdx, e := range stts.Entries {
		for k := uint32(0); k < e.SampleCount; k++ {
			if idx >= sampleCount {
				break
			}
			dts[idx] = t
			cpOff[idx] = runBase + int64(runIdx)*8
			t += int64(e.SampleDelta)
			idx++
		}
	}
	if idx < sampleCount {
		return clock.New(clock.StageScan, clock.CodeTruncatedTable,
			"track %d stts covers %d of %d samples", tr.TrackID, idx, sampleCount).
			WithOffset(sttsOff).WithTrack(tr.TrackID)
	}

	// Chunk map: sample -> (offset,size) using stsc runs.
	runs := make([]chunkRun, len(stsc.Entries))
	for i, e := range stsc.Entries {
		runs[i] = chunkRun{first: e.FirstChunk, perChunk: e.SamplesPerChunk}
	}
	syncSet := make(map[uint32]bool, len(stss))
	for _, n := range stss {
		syncSet[n] = true
	}
	samples := make([]RawSample, 0, sampleCount)
	sampleNum := uint32(1)
	for ci, chunkOff := range offsets {
		chunkNum := uint32(ci + 1)
		per := samplesPerChunk(runs, chunkNum)
		if per == 0 {
			return clock.New(clock.StageScan, clock.CodeBadManifest,
				"track %d chunk %d maps to zero samples", tr.TrackID, chunkNum).
				WithOffset(stcoOff).WithTrack(tr.TrackID)
		}
		cur := chunkOff
		for k := uint32(0); k < per && sampleNum <= uint32(sampleCount); k++ {
			i := sampleNum - 1
			size := stsz.SampleSize
			if stsz.SampleSize == 0 {
				size = stsz.EntrySize[i]
			}
			if cur+uint64(size) < cur {
				return clock.New(clock.StageScan, clock.CodeTruncatedTable,
					"sample %d offset overflows", sampleNum).
					WithOffset(stcoOff).WithTrack(tr.TrackID).WithSample(sampleNum)
			}
			samples = append(samples, RawSample{
				Index:       sampleNum,
				Offset:      cur,
				Size:        size,
				DTS:         dts[i],
				PTS:         dts[i], // refined by ctts below
				Sync:        hasStss && syncSet[sampleNum],
				TableOffset: cpOff[i],
			})
			cur += uint64(size)
			sampleNum++
		}
	}
	if len(samples) != sampleCount {
		return clock.New(clock.StageScan, clock.CodeTruncatedTable,
			"track %d chunk map covers %d of %d samples", tr.TrackID, len(samples), sampleCount).
			WithOffset(stcoOff).WithTrack(tr.TrackID)
	}
	// Read composition offsets (ctts) if present to obtain true PTS.
	if err := applyCtts(r, trak, samples, tr); err != nil {
		return err
	}
	if !hasStss && tr.Kind == "video" {
		tr.Problems = append(tr.Problems, Problem{
			Code:   clock.CodeMissingSyncFlags,
			Detail: "video track has no stss; sync samples cannot be identified from flags",
			Offset: sttsOff,
		})
	}
	tr.Samples = samples
	return nil
}

type chunkRun struct {
	first, perChunk uint32
}

// samplesPerChunk resolves stsc runs for a 1-based chunk number.
func samplesPerChunk(runs []chunkRun, chunk uint32) uint32 {
	per := uint32(0)
	for _, r := range runs {
		if chunk >= r.first {
			per = r.perChunk
		}
	}
	return per
}

func applyCtts(r io.ReadSeeker, trak *gomp4.BoxInfo, samples []RawSample, tr *RawTrack) error {
	bis, err := gomp4.ExtractBoxesWithPayload(r, trak, []gomp4.BoxPath{p("mdia", "minf", "stbl", "ctts")})
	if err != nil {
		return wrapParse(err, int64(trak.Offset), tr.TrackID)
	}
	if len(bis) == 0 {
		return nil
	}
	b := bis[0]
	ctts := b.Payload.(*gomp4.Ctts)
	pos := 0
	for _, e := range ctts.Entries {
		for k := uint32(0); k < e.SampleCount; k++ {
			if pos >= len(samples) {
				return clock.New(clock.StageScan, clock.CodeTruncatedTable,
					"ctts covers more samples than stsz").WithOffset(int64(b.Info.Offset)).WithTrack(tr.TrackID)
			}
			var off int64
			if ctts.Version == 1 {
				off = int64(e.SampleOffsetV1)
			} else {
				off = int64(e.SampleOffsetV0)
			}
			samples[pos].PTS = samples[pos].DTS + off
			pos++
		}
	}
	if pos < len(samples) {
		// Missing ctts coverage means PTS equals DTS for the tail; this is
		// legal only when the remaining offsets are zero, which we cannot
		// verify, so record the truncation.
		tr.Problems = append(tr.Problems, Problem{
			Code:   clock.CodeTruncatedTable,
			Detail: fmt.Sprintf("ctts covers %d of %d samples", pos, len(samples)),
			Offset: int64(b.Info.Offset),
		})
	}
	return nil
}
