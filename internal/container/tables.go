package container

import (
	"github.com/abema/go-mp4"

	"mediatool/internal/clock"
	merr "mediatool/internal/errors"
)

func (t *Track) applySizes(b *mp4.Stsz, off int64) error {
	n := len(t.Samples)
	if b.SampleSize != 0 {
		for i := range t.Samples {
			t.Samples[i].Size = b.SampleSize
		}
		return nil
	}
	if int(b.SampleCount) != n {
		return merr.At(merr.CodeInvalidBox, off,
			"stsz declares %d samples, stts produced %d", b.SampleCount, n)
	}
	if len(b.EntrySize) != n {
		return merr.At(merr.CodeTruncated, off,
			"stsz entry table truncated: have %d sizes, need %d", len(b.EntrySize), n)
	}
	for i := range t.Samples {
		t.Samples[i].Size = b.EntrySize[i]
	}
	return nil
}

func (t *Track) applyOffsets32(offs []uint32, off, fileSize int64) error {
	u64 := make([]uint64, len(offs))
	for i, v := range offs {
		u64[i] = uint64(v)
	}
	return t.applyOffsets(u64, off, fileSize)
}

func (t *Track) applyOffsets64(offs []uint64, off, fileSize int64) error {
	return t.applyOffsets(offs, off, fileSize)
}

// applyOffsets expands stsc/stco into per-sample file offsets and verifies
// every sample stays inside the file.
func (t *Track) applyOffsets(offs []uint64, off, fileSize int64) error {
	n := len(t.Samples)
	if n == 0 {
		return merr.At(merr.CodeInvalidBox, off, "offsets before any samples")
	}
	// samplesPerChunk per chunk index. stsc FirstChunk is 1-based.
	spc := make([]uint32, len(offs))
	run := 0
	for ci := range offs {
		oneBased := uint32(ci) + 1
		for run+1 < len(t.chunks) && t.chunks[run+1].FirstChunk <= oneBased {
			run++
		}
		if run >= len(t.chunks) {
			return merr.At(merr.CodeInvalidBox, off, "stsc does not cover chunk %d", ci)
		}
		spc[ci] = t.chunks[run].SamplesPerChunk
	}
	sampleIdx := 0
	for ci, chunkStart := range offs {
		if int64(chunkStart) >= fileSize {
			return merr.At(merr.CodeBoxOutOfRange, int64(off),
				"chunk %d offset %d beyond file size %d", ci, chunkStart, fileSize)
		}
		cursor := int64(chunkStart)
		for k := uint32(0); k < spc[ci]; k++ {
			if sampleIdx >= n {
				return merr.At(merr.CodeInvalidBox, off,
					"chunk %d references sample %d beyond stts count %d", ci, sampleIdx, n)
			}
			t.Samples[sampleIdx].FileOffset = cursor
			cursor += int64(t.Samples[sampleIdx].Size)
			if cursor > fileSize {
				return merr.At(merr.CodeTruncated, cursor,
					"sample %d ends at %d beyond EOF %d", sampleIdx, cursor, fileSize)
			}
			sampleIdx++
		}
	}
	if sampleIdx != n {
		return merr.At(merr.CodeSampleMissing, off,
			"only %d of %d samples reached via chunk table", sampleIdx, n)
	}
	return nil
}

func (t *Track) applyKeyframes(b *mp4.Stss) error {
	key := make(map[int]bool, len(b.SampleNumber))
	for _, sn := range b.SampleNumber {
		if sn == 0 || int(sn-1) >= len(t.Samples) {
			return merr.New(merr.CodeInvalidBox,
				"stss sample number %d out of range", sn)
		}
		key[int(sn)-1] = true
	}
	for i := range t.Samples {
		t.Samples[i].Key = key[i]
	}
	return nil
}

// Timeline validates monotonicity and returns the track's timeline. VFR
// tracks keep their actual PTS intervals; any backwards step is surfaced.
func (t *Track) Timeline() (*clock.Timeline, error) {
	tl := clock.NewTimeline()
	for _, s := range t.Samples {
		if err := tl.Append(s); err != nil {
			return nil, err
		}
	}
	return tl, nil
}

// AudioTrack returns the first audio track or nil.
func (f *File) AudioTrack() *Track {
	for _, tr := range f.Tracks {
		if tr.Kind == KindAudio {
			return tr
		}
	}
	return nil
}
