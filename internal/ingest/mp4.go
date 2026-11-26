package ingest

import (
	"fmt"
	"math"
	"os"

	"github.com/abema/go-mp4"

	"mediasync/internal/media"
)

type AACSample struct {
	Index         int    `json:"index"`
	Offset        int64  `json:"byte_offset"`
	Size          uint32 `json:"size_bytes"`
	DurationUnits uint32 `json:"duration_units"`
	DurationTicks int64  `json:"duration_ticks"`
	StartTicks    int64  `json:"start_ticks"`
}

type AACResult struct {
	TrackID   string      `json:"track_id"`
	TimeScale uint32      `json:"timescale"`
	Samples   []AACSample `json:"samples"`
}

// ReadAACTrack validates sample-table durations and data offsets with
// go-mp4. It checks container samples only; AAC payloads are not decoded.
func ReadAACTrack(spec TrackSpec, inputDir string) (*AACResult, error) {
	path, err := TrackPath(inputDir, spec.Path)
	if err != nil {
		return nil, err
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, media.InputError("ingest.mp4", spec.ID, err)
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, media.InputError("ingest.mp4", spec.ID, err)
	}

	info, err := mp4.Probe(f)
	if err != nil {
		return nil, media.InputError("ingest.mp4", spec.ID,
			fmt.Errorf("parse MP4 sample tables: %w", err)).WithOffset(0)
	}
	if len(info.Tracks) == 0 {
		return nil, media.InputError("ingest.mp4", spec.ID, fmt.Errorf("no MP4 tracks")).WithOffset(0)
	}
	track := info.Tracks[0]
	if track.Timescale == 0 {
		track.Timescale = 44100
	}
	if len(track.Samples) == 0 {
		return nil, media.InputError("ingest.mp4", spec.ID, fmt.Errorf("sample table is empty")).WithOffset(0)
	}

	out := &AACResult{TrackID: spec.ID, TimeScale: track.Timescale, Samples: make([]AACSample, 0, len(track.Samples))}
	var (
		sampleIndex int
		cursorTicks int64
	)
	fileSize := uint64(st.Size())

	for chunkIndex, chunk := range track.Chunks {
		if chunk.DataOffset > math.MaxInt64 {
			return nil, mp4Fault(spec.ID, sampleIndex, 0, fmt.Errorf("chunk %d offset overflows int64", chunkIndex))
		}
		offset := int64(chunk.DataOffset)
		for inChunk := uint32(0); inChunk < chunk.SamplesPerChunk; inChunk++ {
			if sampleIndex >= len(track.Samples) {
				return nil, mp4Fault(spec.ID, sampleIndex, offset,
					fmt.Errorf("stsc lists more samples than stts/stsz"))
			}
			probeSample := track.Samples[sampleIndex]
			size := probeSample.Size
			if size == 0 {
				return nil, mp4Fault(spec.ID, sampleIndex, offset,
					fmt.Errorf("sample has zero size or missing stsz entry"))
			}
			end := uint64(offset) + uint64(size)
			if end < uint64(offset) || end > fileSize {
				return nil, mp4Fault(spec.ID, sampleIndex, offset,
					fmt.Errorf("sample range [%d,%d) exceeds file size %d", offset, end, fileSize))
			}
			durationTicks, err := media.FromRate(int64(probeSample.TimeDelta), int64(track.Timescale))
			if err != nil {
				return nil, media.OverflowError("ingest.mp4", spec.ID, err).
					WithRecord(sampleIndex).WithOffset(offset)
			}
			startTicks, err := media.CheckedAdd(spec.OriginTicks, cursorTicks)
			if err != nil {
				return nil, media.OverflowError("ingest.mp4", spec.ID, err).
					WithRecord(sampleIndex).WithOffset(offset)
			}
			out.Samples = append(out.Samples, AACSample{
				Index: sampleIndex, Offset: offset, Size: size,
				DurationUnits: probeSample.TimeDelta, DurationTicks: durationTicks,
				StartTicks: startTicks,
			})
			var nextCursor int64
			nextCursor, err = media.CheckedAdd(cursorTicks, durationTicks)
			if err != nil {
				return nil, media.OverflowError("ingest.mp4", spec.ID, err).
					WithRecord(sampleIndex).WithOffset(offset)
			}
			cursorTicks = nextCursor
			offset += int64(size)
			sampleIndex++
		}
	}

	if sampleIndex != len(track.Samples) {
		offset := int64(0)
		if len(track.Chunks) > 0 {
			offset = int64(track.Chunks[len(track.Chunks)-1].DataOffset)
		}
		return nil, mp4Fault(spec.ID, sampleIndex, offset,
			fmt.Errorf("sample table count %d does not match stsc expansion %d", len(track.Samples), sampleIndex))
	}
	return out, nil
}

func mp4Fault(track string, index int, offset int64, err error) *media.Error {
	return media.InputError("ingest.mp4", track, err).WithRecord(index).WithOffset(offset)
}
