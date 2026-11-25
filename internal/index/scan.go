package index

import (
	"errors"
	"io/fs"
	"os"

	"mediacore/internal/clock"
	"mediacore/internal/diagnostic"
	"mediacore/internal/manifest"
)

// ScanOptions controls local safety and read sizing.
type ScanOptions struct {
	WorkRoot     string
	ChunkBytes   int
	MaxFileBytes int64
}

func DefaultScanOptions() ScanOptions {
	return ScanOptions{ChunkBytes: 64 * 1024, MaxFileBytes: 1 << 40}
}

// ScanEntry hashes and parses one media entry, then builds its random-access index.
// A malformed sample returns a located error and no EntryIndex is persisted by callers.
func ScanEntry(e manifest.Entry, opts ScanOptions) (*EntryIndex, error) {
	if opts.ChunkBytes <= 0 {
		opts.ChunkBytes = 64 * 1024
	}
	loc := e.Location("index")

	mediaHash, mediaSize, err := HashFile(e.MediaPath)
	if err != nil {
		loc.ByteOffset = 0
		return nil, diagnostic.Wrap(errKindForRead(err), loc, err)
	}
	if opts.MaxFileBytes > 0 && mediaSize > opts.MaxFileBytes {
		return nil, diagnostic.New(diagnostic.ErrIllegalSample, loc, "media file exceeds configured limit")
	}

	initHash := ""
	var initTimescale int64
	if e.InitPath != "" {
		h, _, err := HashFile(e.InitPath)
		if err != nil {
			loc.Field = "initialization"
			return nil, diagnostic.Wrap(errKindForRead(err), loc, err)
		}
		initHash = h
		pm, err := parseMP4(e.InitPath)
		if err != nil {
			loc.Field = "initialization"
			loc.ByteOffset = -1
			return nil, diagnostic.Wrap(diagnostic.ErrIllegalSample, loc, err)
		}
		initTimescale = pm.timescale
	}

	pm, err := parseMP4(e.MediaPath)
	if err != nil {
		// Box-level truncation/malformation. Keep the source entry in the location.
		loc.ByteOffset = -1
		return nil, diagnostic.Wrap(diagnostic.ErrTruncated, loc, err)
	}
	timescale := pm.timescale
	if timescale == 0 {
		timescale = initTimescale
	}
	if timescale == 0 {
		timescale = e.SourceTimescale
	}
	if timescale <= 0 {
		return nil, diagnostic.New(diagnostic.ErrZeroTimescale, loc, "media timescale is zero")
	}

	idx := &EntryIndex{
		StableKey:        e.StableKey,
		Manifest:         e.Manifest,
		Representation:   e.Representation,
		EntryIndex:       e.EntryIndex,
		OriginalSequence: e.OriginalSequence,
		InputPath:        e.MediaPath,
		InputSHA256:      mediaHash,
		InitSHA256:       initHash,
		MappingVersion:   MappingRuleVersion,
		ReferenceStartU:  e.ReferenceStartU,
		DeviationU:       e.DeviationU,
		Aligned:          e.Aligned,
		Timescale:        timescale,
	}

	if len(pm.samples) == 0 {
		loc.ByteOffset = 0
		return nil, diagnostic.New(diagnostic.ErrIllegalSample, loc, "media contains no sample table entries")
	}

	var sourceCursor int64
	for i, ps := range pm.samples {
		sampleLoc := loc
		sampleLoc.ByteOffset = ps.offset
		if ps.size == 0 {
			return nil, diagnostic.New(diagnostic.ErrIllegalSample, sampleLoc, "sample length is zero")
		}
		if ps.offset < 0 {
			return nil, diagnostic.New(diagnostic.ErrIllegalSample, sampleLoc, "sample offset is negative")
		}
		end, addErr := clock.Add(ps.offset, int64(ps.size))
		if addErr != nil {
			return nil, diagnostic.Wrap(diagnostic.ErrIllegalSample, sampleLoc, addErr)
		}
		if ps.offset > mediaSize {
			sampleLoc.ByteOffset = mediaSize
			return nil, diagnostic.New(diagnostic.ErrTruncated, sampleLoc, "sample starts after end of shard")
		}
		if end > mediaSize {
			sampleLoc.ByteOffset = mediaSize
			return nil, diagnostic.New(diagnostic.ErrTruncated, sampleLoc, "sample payload extends beyond truncated shard")
		}

		deltaU, err := clock.DurationToUnified(int64(ps.duration), timescale)
		if err != nil {
			return nil, diagnostic.Wrap(err, sampleLoc, err)
		}
		startU, err := clock.Add(e.ProgramStartU, sourceCursor)
		if err != nil {
			return nil, diagnostic.Wrap(err, sampleLoc, err)
		}
		endU, err := clock.Add(startU, deltaU)
		if err != nil {
			return nil, diagnostic.Wrap(err, sampleLoc, err)
		}
		sourceStart := e.SourceStart + sourceCursor
		if pm.fragment && pm.baseDecodeTime != 0 && i == 0 {
			sourceStart = int64(pm.baseDecodeTime)
		}
		idx.Samples = append(idx.Samples, Sample{
			Index:            i,
			StableKey:        e.StableKey + "#s" + itoaIndex(i),
			SourceEntryKey:   e.StableKey,
			Manifest:         e.Manifest,
			Representation:   e.Representation,
			OriginalSequence: e.OriginalSequence,
			FilePath:         e.MediaPath,
			ByteOffset:       ps.offset,
			ByteSize:         int64(ps.size),
			StartU:           startU,
			EndU:             endU,
			SourceTimescale:  timescale,
			SourceStart:      sourceStart,
			SourceDuration:   int64(ps.duration),
			RandomAccess:     ps.sync,
		})
		sourceCursor, err = clock.Add(sourceCursor, deltaU)
		if err != nil {
			return nil, diagnostic.Wrap(err, sampleLoc, err)
		}
	}
	idx.Digest = DigestSamples(idx)
	return idx, nil
}

func errKindForRead(err error) error {
	if errors.Is(err, fs.ErrNotExist) {
		return diagnostic.ErrTruncated
	}
	return nil
}

func itoaIndex(i int) string {
	if i == 0 {
		return "0"
	}
	neg := i < 0
	if neg {
		i = -i
	}
	var b [20]byte
	pos := len(b)
	for i > 0 {
		pos--
		b[pos] = byte('0' + i%10)
		i /= 10
	}
	if neg {
		pos--
		b[pos] = '-'
	}
	return string(b[pos:])
}

// ReadSampleRangeChunks reads a byte range in bounded chunks. It exists to keep
// one large sample/shard from being materialized entirely in memory.
func ReadSampleRangeChunks(path string, offset, size int64, chunk int, consume func([]byte) error) error {
	if chunk <= 0 {
		chunk = 64 * 1024
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := f.Seek(offset, 0); err != nil {
		return err
	}
	remaining := size
	buf := make([]byte, chunk)
	for remaining > 0 {
		n := int64(chunk)
		if n > remaining {
			n = remaining
		}
		rn, err := f.Read(buf[:n])
		if rn > 0 {
			if cerr := consume(buf[:rn]); cerr != nil {
				return cerr
			}
			remaining -= int64(rn)
		}
		if err != nil {
			return err
		}
		if rn == 0 {
			return diagnostic.ErrTruncated
		}
	}
	return nil
}
