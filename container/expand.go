package container

import (
	"fmt"

	mp4 "github.com/abema/go-mp4"

	"mediarc/model"
	"mediarc/timebase"
)

// expandTrack turns a builder into a Track with validated samples on the shared
// timeline. Any chunk whose extent leaves the file is dropped and reported.
func expandTrack(b *trackBuilder, size int64, options ScanOptions) (Track, []*model.Error) {
	track := Track{ID: b.id, Kind: b.kind, Timescale: b.timescale}
	issues := make([]*model.Error, 0, 2)
	if b.timescale == 0 {
		issues = append(issues, model.New(model.CodeIndexIncomplete, stream,
			fmt.Sprintf("track %d has no timescale", b.id)))
		return track, issues
	}
	if ns, err := timebase.NanosFromScale(int64(b.duration), b.timescale); err == nil {
		track.Duration = ns
	}
	if !b.hasStbl || !b.hasStsz || !b.hasStts || !b.hasStsc || !b.hasStco {
		var missing string
		switch {
		case !b.hasStbl:
			missing = "stbl"
		case !b.hasStsz:
			missing = "stsz"
		case !b.hasStts:
			missing = "stts"
		case !b.hasStsc:
			missing = "stsc"
		default:
			missing = "stco/co64"
		}
		issues = append(issues, model.New(model.CodeIndexIncomplete, stream,
			fmt.Sprintf("track %d is missing %s", b.id, missing)))
		return track, issues
	}
	total := int(b.stszSampleCount())
	if total > options.MaxSamplesPerTrack {
		issues = append(issues, model.New(model.CodeIndexIncomplete, stream,
			fmt.Sprintf("track %d declares %d samples, over the %d cap", b.id, total, options.MaxSamplesPerTrack)))
		total = options.MaxSamplesPerTrack
	}
	syncSet := make(map[uint32]struct{}, len(b.syncSamples))
	for _, number := range b.syncSamples {
		syncSet[number] = struct{}{}
	}

	var number uint32
	var clockTicks int64
	chunkSamples := b.samplesPerChunk()
	for chunkIndex, chunkOffset := range b.chunkOffsets {
		if int(number) >= total {
			break
		}
		perChunk := chunkSamples.perChunk(uint32(chunkIndex + 1))
		offset := int64(chunkOffset)
		// A dropped chunk must still advance the sample counter so that the
		// remaining sample durations line up with their own slots.
		skipped := uint32(0)
		for i := uint32(0); i < perChunk; i++ {
			number++
			if int(number) > total {
				break
			}
			sampleSize := b.constSize
			if sampleSize == 0 {
				if int(number-1) >= len(b.sampleSizes) {
					issues = append(issues, model.New(model.CodeIndexIncomplete, stream,
						fmt.Sprintf("track %d stsz lacks sample %d", b.id, number)))
					return track, issues
				}
				sampleSize = b.sampleSizes[number-1]
			}
			if offset < 0 || offset+int64(sampleSize) > size {
				issues = append(issues, model.New(model.CodeIndexIncomplete, stream,
					fmt.Sprintf("track %d chunk %d sample %d lies outside the file", b.id, chunkIndex+1, number)).
					WithOffset(offset))
				// Drop only the unreadable chunk. Subsequent chunks carry
				// their own offsets, so valid later fragments are still
				// recovered without reading past the input.
				skipped = perChunk - i
				break
			}
			var delta uint32
			if int(number-1) < len(b.sampleDeltas) {
				delta = b.sampleDeltas[number-1]
			}
			startNS, err := timebase.NanosFromScale(clockTicks, b.timescale)
			if err != nil {
				issues = append(issues, model.New(model.CodeTimestampOverflow, stream,
					"sample start is not representable").WithOffset(offset))
				return track, issues
			}
			durationNS, err := timebase.NanosFromScale(int64(delta), b.timescale)
			if err != nil {
				issues = append(issues, model.New(model.CodeTimestampOverflow, stream,
					"sample duration is not representable").WithOffset(offset))
				return track, issues
			}
			sync := b.syncAll
			if b.hasSyncBox {
				_, sync = syncSet[number]
			}
			track.Samples = append(track.Samples, Sample{
				TrackID:    b.id,
				Number:     number,
				Offset:     offset,
				Size:       sampleSize,
				StartNS:    startNS,
				DurationNS: durationNS,
				Sync:       sync,
			})
			clockTicks += int64(delta)
			offset += int64(sampleSize)
		}
		for i := uint32(0); i < skipped; i++ {
			if int(number-1) < len(b.sampleDeltas) {
				clockTicks += int64(b.sampleDeltas[number-1])
			}
			number++
		}
	}
	if len(track.Samples) == 0 {
		issues = append(issues, model.New(model.CodeIndexIncomplete, stream,
			fmt.Sprintf("track %d yields no samples", b.id)))
	}
	if len(b.sampleDeltas) < total {
		issues = append(issues, model.New(model.CodeIndexIncomplete, stream,
			fmt.Sprintf("track %d stts covers %d of %d samples", b.id, len(b.sampleDeltas), total)))
	}
	return track, issues
}

// stszSampleCount is the sample count the sample-size table declares. A
// constant-size stsz carries only the count; a per-sample table carries the
// entries themselves.
func (b *trackBuilder) stszSampleCount() uint64 {
	if b.constSize != 0 {
		return uint64(b.sampleCount)
	}
	return uint64(len(b.sampleSizes))
}

// chunkMapping answers "how many samples does chunk N carry?".
type chunkMapping struct {
	entries []mp4.StscEntry
}

func (b *trackBuilder) samplesPerChunk() chunkMapping {
	return chunkMapping{entries: b.stsc}
}

// perChunk returns the samples stored in a 1-based chunk number.
func (m chunkMapping) perChunk(chunk uint32) uint32 {
	if len(m.entries) == 0 {
		return 0
	}
	per := m.entries[0].SamplesPerChunk
	for _, entry := range m.entries {
		if entry.FirstChunk > chunk {
			break
		}
		per = entry.SamplesPerChunk
	}
	return per
}
