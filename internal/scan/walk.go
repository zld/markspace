package scan

import (
	"io"

	mp4 "github.com/abema/go-mp4"

	"mediacore/internal/clock"
)

// buildChunks merges stsc (sample-to-chunk) with stco/co64 offsets and
// verifies that every declared payload lies inside the mdat extent.
func buildChunks(r io.ReadSeeker, stbl *mp4.BoxInfo, fileSize uint64, sampleCount uint64,
	cfg Config) ([]chunkCursor, error) {
	const op = "scan.chunk"

	stscP, err := onePayload(r, stbl, mp4.BoxTypeStsc())
	if err != nil {
		return nil, err
	}
	stsc := stscP.(*mp4.Stsc)
	if stsc.EntryCount > cfg.MaxTableEntries {
		return nil, clock.ErrRecoverablef(op, "stsc entry_count %d exceeds cap", stsc.EntryCount)
	}

	var offsets []uint64
	if coP, cerr := onePayload(r, stbl, mp4.BoxTypeCo64()); cerr == nil {
		co := coP.(*mp4.Co64)
		if co.EntryCount > cfg.MaxTableEntries {
			return nil, clock.ErrRecoverablef(op, "co64 entry_count %d exceeds cap", co.EntryCount)
		}
		offsets = append(offsets, co.ChunkOffset...)
	} else {
		coP, err2 := onePayload(r, stbl, mp4.BoxTypeStco())
		if err2 != nil {
			return nil, clock.ErrRecoverablef(op, "neither stco nor co64 present")
		}
		co := coP.(*mp4.Stco)
		if co.EntryCount > cfg.MaxTableEntries {
			return nil, clock.ErrRecoverablef(op, "stco entry_count %d exceeds cap", co.EntryCount)
		}
		offsets = make([]uint64, len(co.ChunkOffset))
		for i, o := range co.ChunkOffset {
			offsets[i] = uint64(o)
		}
	}
	chunkN := len(offsets)
	if chunkN == 0 {
		return nil, clock.ErrRecoverablef(op, "chunk offset table is empty")
	}

	chunks := make([]chunkCursor, chunkN)
	for i := range stsc.Entries {
		e := stsc.Entries[i]
		if e.SampleDescriptionIndex != 1 {
			return nil, clock.ErrRecoverablef(op, "stsc description index %d unsupported (only 1)", e.SampleDescriptionIndex)
		}
		if e.SamplesPerChunk == 0 {
			return nil, clock.ErrRecoverablef(op, "stsc zero samples-per-chunk at first_chunk %d", e.FirstChunk)
		}
		first := e.FirstChunk - 1 // stsc first_chunk is 1-based
		last := uint32(chunkN) - 1
		if i+1 < len(stsc.Entries) {
			last = stsc.Entries[i+1].FirstChunk - 2
		}
		if int(first) >= chunkN || last < first {
			return nil, clock.ErrRecoverablef(op, "stsc first_chunk %d out of range for %d chunks", e.FirstChunk, chunkN)
		}
		for c := first; c <= last; c++ {
			chunks[c].sampleCount = e.SamplesPerChunk
			chunks[c].descIndex = e.SampleDescriptionIndex
		}
	}

	var total uint64
	for i, off := range offsets {
		if off >= fileSize {
			return nil, clock.WithTrack(clock.ErrRecoverablef(op, "chunk %d offset %d beyond file %d", i, off, fileSize), 0, int64(i))
		}
		chunks[i].offset = off
		total += uint64(chunks[i].sampleCount)
	}
	if total != uint64(sampleCount) {
		return nil, clock.ErrRecoverablef(op, "stsc/stco account for %d samples but stsz declares %d", total, sampleCount)
	}
	return chunks, nil
}

// ScanBatches walks the resolved tables in bounded batches. Each callback
// receives at most cfg.MaxBatchSamples fully-resolved Samples. The walk keeps
// only table cursors between calls, so peak memory is independent of the
// total sample count.
func (t *TrackScan) ScanBatches(cfg Config, fn func([]Sample) error) error {
	const op = "scan.batch"
	batch := make([]Sample, 0, cfg.MaxBatchSamples)
	sttsCur, sttsLeft := 0, uint32(0)
	cttsCur, cttsLeft := 0, uint32(0)
	var dtsTicks int64
	var chunkCursor, sampleInChunk uint32
	var offsetCursor uint64

	nonSync := map[uint32]struct{}{}
	for _, n := range t.manifest.ForceNonSync {
		nonSync[n] = struct{}{}
	}

	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		if err := fn(batch); err != nil {
			return err
		}
		batch = batch[:0]
		return nil
	}

	for i := uint32(0); i < t.Count; i++ {
		// Advance stts cursor for DTS.
		if sttsLeft == 0 {
			if sttsCur >= len(t.tables.stts) {
				return clock.WithTrack(clock.ErrRecoverablef(op, "stts exhausted before sample %d", i+1), t.TrackID, int64(i))
			}
			sttsLeft = t.tables.stts[sttsCur].SampleCount
		}
		delta := int64(t.tables.stts[sttsCur].SampleDelta)
		sttsLeft--
		if sttsLeft == 0 {
			sttsCur++
		}
		if i > 0 {
			dtsTicks += delta
		}

		// ctts composition offset (version 1 is signed).
		var cttsTicks int64
		if len(t.tables.ctts) > 0 {
			if cttsLeft == 0 {
				if cttsCur >= len(t.tables.ctts) {
					return clock.WithTrack(clock.ErrRecoverablef(op, "ctts exhausted before sample %d", i+1), t.TrackID, int64(i))
				}
				cttsLeft = t.tables.ctts[cttsCur].SampleCount
			}
			e := t.tables.ctts[cttsCur]
			if e.SampleOffsetV0 != 0 {
				cttsTicks = int64(e.SampleOffsetV0)
			} else {
				cttsTicks = int64(e.SampleOffsetV1)
			}
			cttsLeft--
			if cttsLeft == 0 {
				cttsCur++
			}
		}

		// Chunk placement.
		if sampleInChunk == t.tables.chunks[chunkCursor].sampleCount {
			chunkCursor++
			sampleInChunk, offsetCursor = 0, 0
			if chunkCursor >= uint32(len(t.tables.chunks)) {
				return clock.WithTrack(clock.ErrRecoverablef(op, "chunks exhausted before sample %d", i+1), t.TrackID, int64(i))
			}
		}
		ch := t.tables.chunks[chunkCursor]
		off := ch.offset + offsetCursor
		size := t.tables.sizes[i]
		if off+uint64(size) > t.mdatEnd || off+uint64(size) < off {
			return clock.WithTrack(clock.ErrRecoverablef(op,
				"sample %d payload [%d,%d) outside mdat ending %d (truncated)", i+1, off, off+uint64(size), t.mdatEnd),
				t.TrackID, int64(i))
		}

		dtsNanos, err := t.Basis.ToNanos(dtsTicks)
		if err != nil {
			return clock.WithTrack(err, t.TrackID, int64(i))
		}
		ctNanos, err := t.Basis.ToNanos(dtsTicks + cttsTicks)
		if err != nil {
			return clock.WithTrack(err, t.TrackID, int64(i))
		}
		presNanos, err := t.Edits.MapMediaToSegment(dtsNanos)
		if err != nil {
			return clock.WithTrack(err, t.TrackID, int64(i))
		}
		number := i + 1
		_, isSync := t.tables.sync[number]
		if _, forced := nonSync[number]; forced {
			isSync = false
		}

		batch = append(batch, Sample{
			Index: i, Number: number, Size: size,
			WireSize: t.manifest.WireSizes[i],
			Offset: off, DTS: dtsTicks,
			DTSNanos: dtsNanos, CTSNanos: ctNanos,
			Sync: isSync, ChunkIndex: chunkCursor, DescIndex: ch.descIndex,
		})
		_ = presNanos // presentation mapping validated; repair works on DTS
		offsetCursor += uint64(size)
		sampleInChunk++
		if len(batch) >= cfg.MaxBatchSamples {
			if err := flush(); err != nil {
				return err
			}
		}
	}
	return flush()
}
