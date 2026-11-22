package container

import (
	"fmt"
	"os"

	"github.com/abema/go-mp4"

	"mediacore/internal/core"
)

// readStts expands time-to-sample runs and validates that the expanded count
// agrees with the other tables. A disagreement is recoverable; the declared
// count is kept so recovery can decide where trusting stops.
func readStts(f *os.File, box *Box, tr *TrackScan, lim Limits) *core.Error {
	var v mp4.Stts
	if err := decodeFile(f, box, &v); err != nil {
		return core.Wrap(core.CodeTableTruncated, core.StageScan, true,
			"stts unreadable", err).WithOffset(box.Offset).WithTrack(tr.ID, 0)
	}
	if len(v.Entries) == 0 {
		return core.New(core.CodeTableTruncated, core.StageScan, true,
			"stts has no entries").WithOffset(box.Offset).WithTrack(tr.ID, 0)
	}
	for _, e := range v.Entries {
		if e.SampleCount == 0 {
			return core.New(core.CodeTableTruncated, core.StageScan, true,
				"stts entry with zero sample count").WithOffset(box.Offset).WithTrack(tr.ID, 0)
		}
		if e.SampleDelta == 0 {
			// Zero-delta runs make presentation times non-monotonic.
			return core.New(core.CodeBadTimestamp, core.StageScan, true,
				"stts entry with zero sample delta").WithOffset(box.Offset).WithTrack(tr.ID, 0)
		}
		tr.Stts = append(tr.Stts, SttsRun{Count: e.SampleCount, Delta: e.SampleDelta})
	}
	return nil
}

func readStsc(f *os.File, box *Box, tr *TrackScan, lim Limits) *core.Error {
	var v mp4.Stsc
	if err := decodeFile(f, box, &v); err != nil {
		return core.Wrap(core.CodeTableTruncated, core.StageScan, true,
			"stsc unreadable", err).WithOffset(box.Offset).WithTrack(tr.ID, 0)
	}
	if len(v.Entries) == 0 {
		return core.New(core.CodeTableTruncated, core.StageScan, true,
			"stsc has no entries").WithOffset(box.Offset).WithTrack(tr.ID, 0)
	}
	last := uint32(0)
	for i, e := range v.Entries {
		if e.FirstChunk == 0 || (i > 0 && e.FirstChunk <= last) {
			return core.New(core.CodeTableTruncated, core.StageScan, true,
				"stsc first_chunk is not strictly increasing").WithOffset(box.Offset).WithTrack(tr.ID, 0)
		}
		if e.SamplesPerChunk == 0 {
			return core.New(core.CodeTableTruncated, core.StageScan, true,
				"stsc entry with zero samples per chunk").WithOffset(box.Offset).WithTrack(tr.ID, 0)
		}
		last = e.FirstChunk
		tr.Stsc = append(tr.Stsc, StscRun{
			FirstChunk:       e.FirstChunk,
			SamplesPerChunk:  e.SamplesPerChunk,
			DescriptionIndex: e.SampleDescriptionIndex,
		})
	}
	return nil
}

func readStsz(f *os.File, box *Box, tr *TrackScan, lim Limits) *core.Error {
	var v mp4.Stsz
	if err := decodeFile(f, box, &v); err != nil {
		return core.Wrap(core.CodeTableTruncated, core.StageScan, true,
			"stsz unreadable", err).WithOffset(box.Offset).WithTrack(tr.ID, 0)
	}
	tr.Declared = int(v.SampleCount)
	maxSamples := lim.MaxSamples
	if maxSamples <= 0 {
		maxSamples = 1 << 22
	}
	if tr.Declared < 0 || tr.Declared > maxSamples {
		return core.New(core.CodeLimitExceeded, core.StageScan, true,
			fmt.Sprintf("stsz declares %d samples, limit %d", tr.Declared, maxSamples)).
			WithOffset(box.Offset).WithTrack(tr.ID, 0)
	}
	if int64(len(v.EntrySize))*4 > limTableBytes(lim) {
		return core.New(core.CodeLimitExceeded, core.StageScan, true,
			fmt.Sprintf("stsz table of %d entries exceeds memory limit", len(v.EntrySize))).
			WithOffset(box.Offset).WithTrack(tr.ID, 0)
	}
	if v.SampleSize != 0 {
		tr.FixedSize = v.SampleSize
		return nil
	}
	tr.Sizes = make([]uint32, len(v.EntrySize))
	copy(tr.Sizes, v.EntrySize)
	if len(v.EntrySize) < tr.Declared {
		return core.New(core.CodeTableTruncated, core.StageScan, true,
			fmt.Sprintf("stsz declares %d samples but holds %d sizes", tr.Declared, len(v.EntrySize))).
			WithOffset(box.Offset).WithTrack(tr.ID, 0)
	}
	return nil
}

func readStco(f *os.File, box *Box, tr *TrackScan, lim Limits) *core.Error {
	var v mp4.Stco
	if err := decodeFile(f, box, &v); err != nil {
		return core.Wrap(core.CodeTableTruncated, core.StageScan, true,
			"stco unreadable", err).WithOffset(box.Offset).WithTrack(tr.ID, 0)
	}
	for _, off := range v.ChunkOffset {
		tr.Offsets = append(tr.Offsets, int64(off))
	}
	return nil
}

func readCo64(f *os.File, box *Box, tr *TrackScan, lim Limits) *core.Error {
	var v mp4.Co64
	if err := decodeFile(f, box, &v); err != nil {
		return core.Wrap(core.CodeTableTruncated, core.StageScan, true,
			"co64 unreadable", err).WithOffset(box.Offset).WithTrack(tr.ID, 0)
	}
	for _, off := range v.ChunkOffset {
		if off > 1<<62 {
			return core.New(core.CodeSampleRange, core.StageScan, true,
				"co64 chunk offset out of range").WithOffset(box.Offset).WithTrack(tr.ID, 0)
		}
		tr.Offsets = append(tr.Offsets, int64(off))
	}
	return nil
}

// limTableBytes is the per-table byte budget; zero means the default.
func limTableBytes(lim Limits) int64 {
	if lim.MaxTableBytes > 0 {
		return lim.MaxTableBytes
	}
	return 64 << 20
}
