package container

import (
	"github.com/abema/go-mp4"

	"mediasync/internal/clock"
	"mediasync/internal/fault"
)

// Sample is one fully resolved media sample with its original file offset.
type Sample struct {
	Index         int
	Offset        int64
	Size          uint32
	DurationTicks uint32
}

type chunkRef struct {
	offset uint64
	co64   bool
}

// Track is the normalized view of one trak box subtree.
type Track struct {
	ID          uint32
	Name        string
	HandlerType string
	Timescale   uint32
	Timebase    *clock.Timebase
	Samples     []Sample
	Marks       []clock.Mark

	trakOffset int64
	mdhdOffset int64
	hdlrOffset int64

	stts         []mp4.SttsEntry
	sttsOffset   int64
	stsz         *mp4.Stsz
	stszOffset   int64
	stsc         []mp4.StscEntry
	stscOffset   int64
	chunks       []chunkRef
	chunksBoxOff int64
	mdhdDuration uint64
}

func newTrack(trakOffset int64) *Track {
	return &Track{trakOffset: trakOffset}
}

// finalize validates table consistency, resolves every sample's byte offset
// against the mdat region and builds the track's rational timeline.
func (t *Track) finalize(file *File, trakOffset int64) error {
	t.trakOffset = trakOffset
	if t.Timescale == 0 {
		return fault.New(fault.ErrIllegalTimebase, t.Name, t.mdhdOffset,
			"track %d has zero mdhd timescale", t.ID)
	}
	tb, err := clock.NewTimebase(t.Timescale)
	if err != nil {
		return fault.New(fault.ErrIllegalTimebase, t.Name, t.mdhdOffset,
			"track %d: %v", t.ID, err)
	}
	t.Timebase = tb

	durations, tickTotal, err := t.expandDurations()
	if err != nil {
		return err
	}
	sampleTotal := uint64(len(durations))
	if err := t.checkSizes(sampleTotal); err != nil {
		return err
	}
	perChunk, err := t.expandChunks(sampleTotal)
	if err != nil {
		return err
	}
	if err := t.resolveSamples(file, durations, perChunk); err != nil {
		return err
	}
	if t.mdhdDuration != 0 && t.mdhdDuration != tickTotal {
		return fault.New(fault.ErrSampleRange, t.Name, t.mdhdOffset,
			"mdhd duration %d disagrees with stts tick total %d", t.mdhdDuration, tickTotal)
	}

	raw := make([]clock.RawSample, len(t.Samples))
	for i, s := range t.Samples {
		raw[i] = clock.RawSample{DurationTicks: s.DurationTicks, Offset: s.Offset}
	}
	marks, err := clock.BuildMarks(tb, raw)
	if err != nil {
		return err
	}
	t.Marks = marks
	return nil
}

// expandDurations flattens stts run-length entries into one duration per
// sample and returns the exact tick total.
func (t *Track) expandDurations() ([]uint32, uint64, error) {
	if len(t.stts) == 0 {
		return nil, 0, fault.New(fault.ErrSampleRange, t.Name, t.sttsOffset,
			"track %d has no stts table", t.ID)
	}
	var durations []uint32
	var total uint64
	for _, e := range t.stts {
		if e.SampleCount == 0 {
			return nil, 0, fault.New(fault.ErrSampleRange, t.Name, t.sttsOffset,
				"stts run with zero sample count")
		}
		for range e.SampleCount {
			durations = append(durations, e.SampleDelta)
		}
		if uint64(len(durations)) > MaxTableCount {
			return nil, 0, fault.New(fault.ErrLimitExceeded, t.Name, t.sttsOffset,
				"expanded sample count exceeds limit %d", MaxTableCount)
		}
		ticks, ok := checkedMul(e.SampleCount, e.SampleDelta)
		if !ok {
			return nil, 0, fault.New(fault.ErrOverflow, t.Name, t.sttsOffset,
				"stts duration multiplication overflows")
		}
		if total, ok = checkedAdd(total, ticks); !ok {
			return nil, 0, fault.New(fault.ErrOverflow, t.Name, t.sttsOffset,
				"stts total duration overflows")
		}
	}
	return durations, total, nil
}

func (t *Track) checkSizes(totalSamples uint64) error {
	if t.stsz == nil {
		return fault.New(fault.ErrSampleRange, t.Name, t.stszOffset,
			"track %d has no stsz table", t.ID)
	}
	if uint64(t.stsz.SampleCount) != totalSamples {
		return fault.New(fault.ErrSampleRange, t.Name, t.stszOffset,
			"stsz count %d != stts sample total %d", t.stsz.SampleCount, totalSamples)
	}
	if t.stsz.SampleSize == 0 && uint64(len(t.stsz.EntrySize)) != totalSamples {
		return fault.New(fault.ErrSampleRange, t.Name, t.stszOffset,
			"stsz entries %d != sample total %d", len(t.stsz.EntrySize), totalSamples)
	}
	return nil
}

// expandChunks validates stsc and returns the sample count of each chunk.
func (t *Track) expandChunks(totalSamples uint64) ([]uint32, error) {
	if len(t.chunks) == 0 {
		return nil, fault.New(fault.ErrChunkBoundary, t.Name, t.chunksBoxOff,
			"track %d has no chunk offset table", t.ID)
	}
	if len(t.stsc) == 0 {
		return nil, fault.New(fault.ErrSampleRange, t.Name, t.stscOffset,
			"track %d has no stsc table", t.ID)
	}
	chunkCount := uint32(len(t.chunks))
	perChunk := make([]uint32, chunkCount)
	entry := 0
	var assigned uint64
	for chunk := uint32(1); chunk <= chunkCount; chunk++ {
		for entry+1 < len(t.stsc) && t.stsc[entry+1].FirstChunk <= chunk {
			entry++
		}
		sc := t.stsc[entry]
		if sc.FirstChunk == 0 || sc.SamplesPerChunk == 0 || sc.FirstChunk > chunkCount {
			return nil, fault.New(fault.ErrSampleRange, t.Name, t.stscOffset,
				"illegal stsc entry first_chunk=%d samples=%d", sc.FirstChunk, sc.SamplesPerChunk)
		}
		perChunk[chunk-1] = sc.SamplesPerChunk
		sum, ok := checkedAdd(assigned, uint64(sc.SamplesPerChunk))
		if !ok {
			return nil, fault.New(fault.ErrOverflow, t.Name, t.stscOffset,
				"stsc sample total overflows")
		}
		assigned = sum
	}
	if assigned != totalSamples {
		return nil, fault.New(fault.ErrSampleRange, t.Name, t.stscOffset,
			"stsc assigns %d samples but stts declares %d", assigned, totalSamples)
	}
	return perChunk, nil
}

// resolveSamples walks chunks in order, computes each sample's absolute
// offset with overflow checks and verifies chunk/data boundaries.
func (t *Track) resolveSamples(file *File, durations []uint32, perChunk []uint32) error {
	mdatStart := uint64(file.MdatOffset)
	mdatEnd := mdatStart + file.MdatSize
	idx := 0
	for ci, perChunkCount := range perChunk {
		start := t.chunks[ci].offset
		next := mdatEnd
		if ci+1 < len(t.chunks) {
			next = t.chunks[ci+1].offset
		}
		cursor := start
		for range perChunkCount {
			size := t.sampleSize(idx)
			end, ok := checkedAdd(cursor, uint64(size))
			if !ok {
				return fault.New(fault.ErrOverflow, t.Name, int64(cursor),
					"sample offset overflows in chunk %d", ci)
			}
			if start < mdatStart || end > mdatEnd {
				return fault.New(fault.ErrOutOfBounds, t.Name, t.chunksBoxOff,
					"chunk %d bytes [%d,%d) outside mdat [%d,%d)",
					ci, start, end, mdatStart, mdatEnd)
			}
			if end > next {
				return fault.New(fault.ErrChunkBoundary, t.Name, t.chunksBoxOff,
					"chunk %d sample %d crosses next chunk boundary %d", ci, idx, next)
			}
			t.Samples = append(t.Samples, Sample{
				Index:         idx,
				Offset:        int64(cursor),
				Size:          size,
				DurationTicks: durations[idx],
			})
			cursor = end
			idx++
		}
	}
	return nil
}

func (t *Track) sampleSize(idx int) uint32 {
	if t.stsz.SampleSize != 0 {
		return t.stsz.SampleSize
	}
	return t.stsz.EntrySize[idx]
}
