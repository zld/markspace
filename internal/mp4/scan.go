// Package mp4 reads MP4/MOV sample tables through go-mp4.
package mp4

import (
	"io"
	"os"

	gomp4 "github.com/abema/go-mp4"

	"medpipe/internal/apperr"
	"medpipe/internal/clock"
)

// Sample is one expanded sample in media time base ticks.
type Sample struct {
	Number     int    `json:"number"`
	DTS        int64  `json:"dts_tick"`
	PTS        int64  `json:"pts_tick"`
	Duration   int64  `json:"duration_tick"`
	Size       uint32 `json:"size_bytes"`
	ByteOffset int64  `json:"byte_offset"`
	Sync       bool   `json:"sync"`
}

// Edit is one elst entry.  Empty edits are retained rather than folded away.
type Edit struct {
	Empty                bool           `json:"empty"`
	MediaTime            int64          `json:"media_time_movie_tick"`
	SegmentDurationTicks uint64         `json:"segment_duration_movie_tick"`
	Duration             clock.Rational `json:"duration_seconds"`
	ByteOffset           int64          `json:"byte_offset"`
}

// TrackScan contains compact sample-table metadata.
type TrackScan struct {
	ID                 string         `json:"id"`
	SourceFile         string         `json:"source_file"`
	FileSize           int64          `json:"file_size"`
	MovieTimeBase      clock.TimeBase `json:"movie_time_base"`
	MediaTimeBase      clock.TimeBase `json:"media_time_base"`
	MediaDurationTicks uint64         `json:"media_duration_ticks"`
	SampleCount        int            `json:"sample_count"`
	SyncSamples        []int          `json:"sync_samples,omitempty"`
	Edits              []Edit         `json:"edits,omitempty"`
}

// BatchFunc receives a batch of expanded samples.  The batch slice is reused
// after the callback returns, so callers must copy values they retain.
type BatchFunc func(batch []Sample) error

// ScanFile expands timing, size, sync and offset tables in bounded batches.
func ScanFile(id, path string, batchSize int, onBatch BatchFunc) (*TrackScan, error) {
	if batchSize <= 0 {
		batchSize = 64
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, apperr.New("E_TRACK_FILE_MISSING", "open MP4: %v", err).
			WithStage("scan").WithTrack(id, path)
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, apperr.New("E_MP4", "stat MP4: %v", err).WithStage("scan").WithTrack(id, path)
	}

	found, err := gomp4.ExtractBoxesWithPayload(f, nil, sampleTablePaths())
	if err != nil {
		return nil, apperr.New("E_MP4", "parse MP4 boxes: %v", err).WithStage("scan").WithTrack(id, path)
	}
	mdhd, _, okMd := boxAs[*gomp4.Mdhd](found, gomp4.BoxTypeMdhd())
	stts, _, okStts := boxAs[*gomp4.Stts](found, gomp4.BoxTypeStts())
	stsz, _, okStsz := boxAs[*gomp4.Stsz](found, gomp4.BoxTypeStsz())
	stsc, _, okStsc := boxAs[*gomp4.Stsc](found, gomp4.BoxTypeStsc())
	if !okMd || !okStts || !okStsz || !okStsc {
		return nil, apperr.New("E_MP4", "missing mdhd/stts/stsz/stsc sample tables").
			WithStage("scan").WithTrack(id, path)
	}

	scan := &TrackScan{
		ID:                 id,
		SourceFile:         path,
		FileSize:           st.Size(),
		MediaTimeBase:      clock.TimeBase{TicksPerSecond: mdhd.Timescale},
		MediaDurationTicks: mdhd.GetDuration(),
		SampleCount:        int(stsz.SampleCount),
	}
	if mvhd, _, ok := boxAs[*gomp4.Mvhd](found, gomp4.BoxTypeMvhd()); ok {
		scan.MovieTimeBase = clock.TimeBase{TicksPerSecond: mvhd.Timescale}
	} else {
		scan.MovieTimeBase = scan.MediaTimeBase
	}
	if stss, _, ok := boxAs[*gomp4.Stss](found, gomp4.BoxTypeStss()); ok {
		scan.SyncSamples = append(scan.SyncSamples, ints(stss.SampleNumber)...)
	}
	if elst, info, ok := boxAs[*gomp4.Elst](found, gomp4.BoxTypeElst()); ok {
		scan.Edits = readEdits(elst, info, scan.MovieTimeBase)
	}
	ctts, _, hasCtts := boxAs[*gomp4.Ctts](found, gomp4.BoxTypeCtts())
	stco, _, hasStco := boxAs[*gomp4.Stco](found, gomp4.BoxTypeStco())
	co64, _, hasCo64 := boxAs[*gomp4.Co64](found, gomp4.BoxTypeCo64())
	if !hasStco && !hasCo64 {
		return nil, apperr.New("E_MP4", "missing stco/co64 chunk offsets").WithStage("scan").WithTrack(id, path)
	}
	chunks := chunkOffsets(stco, co64, hasStco, hasCo64)

	syncSet := make(map[int]bool, len(scan.SyncSamples))
	for _, n := range scan.SyncSamples {
		syncSet[n] = true
	}
	if len(scan.SyncSamples) == 0 {
		syncSet[1] = true // no stss means at least the first sample is a resume point
		scan.SyncSamples = []int{1}
	}

	var sttsRun, sttsLeft int
	var cttsRun, cttsLeft int
	dts := int64(0)
	sampleNo := 1
	batch := make([]Sample, 0, batchSize)
	flush := func() error {
		if len(batch) == 0 || onBatch == nil {
			batch = batch[:0]
			return nil
		}
		if err := onBatch(append([]Sample(nil), batch...)); err != nil {
			return err
		}
		batch = batch[:0]
		return nil
	}

	for chunkIndex := 1; chunkIndex <= len(chunks); chunkIndex++ {
		spc, err := samplesPerChunk(stsc, chunkIndex)
		if err != nil {
			return nil, err.WithTrack(id, path)
		}
		pos := chunks[chunkIndex-1]
		for j := 0; j < spc; j++ {
			if sampleNo > scan.SampleCount {
				return nil, apperr.New("E_SAMPLE_RANGE", "stsc enumerates more samples than stsz count=%d", scan.SampleCount).
					WithStage("scan").WithTrack(id, path).WithSample(sampleNo, pos)
			}
			size := sampleSize(stsz, sampleNo)
			duration := nextStts(stts, &sttsRun, &sttsLeft)
			comp := int64(0)
			if hasCtts {
				comp = nextCtts(ctts, &cttsRun, &cttsLeft)
			}
			end := pos + int64(size)
			if pos < 0 || end > st.Size() {
				return nil, apperr.New("E_SAMPLE_RANGE",
					"sample byte range [%d,%d) exceeds file size %d", pos, end, st.Size()).
					WithStage("scan").WithTrack(id, path).WithSample(sampleNo, pos)
			}
			batch = append(batch, Sample{
				Number: sampleNo, DTS: dts, PTS: dts + comp, Duration: duration,
				Size: size, ByteOffset: pos, Sync: syncSet[sampleNo],
			})
			if len(batch) >= batchSize {
				if err := flush(); err != nil {
					return nil, err
				}
			}
			dts += duration
			pos = end
			sampleNo++
		}
	}
	if sampleNo-1 != scan.SampleCount {
		return nil, apperr.New("E_SAMPLE_RANGE", "expanded %d samples but stsz declares %d", sampleNo-1, scan.SampleCount).
			WithStage("scan").WithTrack(id, path).WithSample(sampleNo, -1)
	}
	if err := flush(); err != nil {
		return nil, err
	}
	return scan, nil
}

// ReadPayload reads exactly one sample payload.  Callers should keep only one
// payload or one bounded batch in memory.
func ReadPayload(path string, s Sample) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, apperr.New("E_MP4", "open sample payload: %v", err).WithStage("scan")
	}
	defer f.Close()
	if _, err := f.Seek(s.ByteOffset, io.SeekStart); err != nil {
		return nil, apperr.New("E_SAMPLE_RANGE", "seek sample %d: %v", s.Number, err).
			WithStage("scan").WithSample(s.Number, s.ByteOffset)
	}
	buf := make([]byte, s.Size)
	if _, err := io.ReadFull(f, buf); err != nil {
		return nil, apperr.New("E_SAMPLE_RANGE", "read sample %d: %v", s.Number, err).
			WithStage("scan").WithSample(s.Number, s.ByteOffset)
	}
	return buf, nil
}

func sampleTablePaths() []gomp4.BoxPath {
	return []gomp4.BoxPath{
		{gomp4.BoxTypeMoov(), gomp4.BoxTypeMvhd()},
		{gomp4.BoxTypeMoov(), gomp4.BoxTypeTrak(), gomp4.BoxTypeMdia(), gomp4.BoxTypeMdhd()},
		{gomp4.BoxTypeMoov(), gomp4.BoxTypeTrak(), gomp4.BoxTypeEdts(), gomp4.BoxTypeElst()},
		{gomp4.BoxTypeMoov(), gomp4.BoxTypeTrak(), gomp4.BoxTypeMdia(), gomp4.BoxTypeMinf(), gomp4.BoxTypeStbl(), gomp4.BoxTypeStts()},
		{gomp4.BoxTypeMoov(), gomp4.BoxTypeTrak(), gomp4.BoxTypeMdia(), gomp4.BoxTypeMinf(), gomp4.BoxTypeStbl(), gomp4.BoxTypeCtts()},
		{gomp4.BoxTypeMoov(), gomp4.BoxTypeTrak(), gomp4.BoxTypeMdia(), gomp4.BoxTypeMinf(), gomp4.BoxTypeStbl(), gomp4.BoxTypeStss()},
		{gomp4.BoxTypeMoov(), gomp4.BoxTypeTrak(), gomp4.BoxTypeMdia(), gomp4.BoxTypeMinf(), gomp4.BoxTypeStbl(), gomp4.BoxTypeStsc()},
		{gomp4.BoxTypeMoov(), gomp4.BoxTypeTrak(), gomp4.BoxTypeMdia(), gomp4.BoxTypeMinf(), gomp4.BoxTypeStbl(), gomp4.BoxTypeStsz()},
		{gomp4.BoxTypeMoov(), gomp4.BoxTypeTrak(), gomp4.BoxTypeMdia(), gomp4.BoxTypeMinf(), gomp4.BoxTypeStbl(), gomp4.BoxTypeStco()},
		{gomp4.BoxTypeMoov(), gomp4.BoxTypeTrak(), gomp4.BoxTypeMdia(), gomp4.BoxTypeMinf(), gomp4.BoxTypeStbl(), gomp4.BoxTypeCo64()},
	}
}
