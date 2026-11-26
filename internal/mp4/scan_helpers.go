package mp4

import (
	gomp4 "github.com/abema/go-mp4"

	"medpipe/internal/apperr"
	"medpipe/internal/clock"
)

func boxAs[T gomp4.IBox](boxes []*gomp4.BoxInfoWithPayload, typ gomp4.BoxType) (T, gomp4.BoxInfo, bool) {
	var zero T
	for _, bp := range boxes {
		if bp.Info.Type != typ {
			continue
		}
		if p, ok := bp.Payload.(T); ok {
			return p, bp.Info, true
		}
	}
	return zero, gomp4.BoxInfo{}, false
}

func readEdits(elst *gomp4.Elst, info gomp4.BoxInfo, movie clock.TimeBase) []Edit {
	out := make([]Edit, 0, len(elst.Entries))
	payloadOffset := int64(info.Offset + info.HeaderSize)
	for i := range elst.Entries {
		duration := elst.GetSegmentDuration(i)
		mediaTime := elst.GetMediaTime(i)
		out = append(out, Edit{
			Empty:                mediaTime < 0,
			MediaTime:            mediaTime,
			SegmentDurationTicks: duration,
			Duration:             clock.NewRational(int64(duration), int64(movie.TicksPerSecond)),
			ByteOffset:           payloadOffset,
		})
	}
	return out
}

func chunkOffsets(stco *gomp4.Stco, co64 *gomp4.Co64, hasStco, hasCo64 bool) []int64 {
	switch {
	case hasStco:
		out := make([]int64, len(stco.ChunkOffset))
		for i, v := range stco.ChunkOffset {
			out[i] = int64(v)
		}
		return out
	case hasCo64:
		out := make([]int64, len(co64.ChunkOffset))
		for i, v := range co64.ChunkOffset {
			out[i] = int64(v)
		}
		return out
	default:
		return nil
	}
}

func samplesPerChunk(stsc *gomp4.Stsc, chunkNumber int) (int, *apperr.E) {
	spc := 0
	for _, e := range stsc.Entries {
		if int(e.FirstChunk) <= chunkNumber {
			spc = int(e.SamplesPerChunk)
		} else {
			break
		}
	}
	if spc <= 0 {
		return 0, apperr.New("E_MP4", "invalid stsc entry for chunk %d", chunkNumber).WithStage("scan")
	}
	return spc, nil
}

func sampleSize(stsz *gomp4.Stsz, sampleNumber int) uint32 {
	if stsz.SampleSize != 0 {
		return stsz.SampleSize
	}
	idx := sampleNumber - 1
	if idx >= 0 && idx < len(stsz.EntrySize) {
		return stsz.EntrySize[idx]
	}
	return 0
}

func nextStts(stts *gomp4.Stts, run *int, remaining *int) int64 {
	if *remaining == 0 {
		if *run >= len(stts.Entries) {
			return 0
		}
		*remaining = int(stts.Entries[*run].SampleCount)
	}
	if *run >= len(stts.Entries) || *remaining <= 0 {
		return 0
	}
	delta := int64(stts.Entries[*run].SampleDelta)
	*remaining--
	if *remaining == 0 {
		*run++
	}
	return delta
}

func nextCtts(ctts *gomp4.Ctts, run *int, remaining *int) int64 {
	if *remaining == 0 {
		if *run >= len(ctts.Entries) {
			return 0
		}
		*remaining = int(ctts.Entries[*run].SampleCount)
	}
	if *run >= len(ctts.Entries) || *remaining <= 0 {
		return 0
	}
	e := ctts.Entries[*run]
	var offset int64
	if ctts.Version == 1 {
		offset = int64(e.SampleOffsetV1)
	} else {
		offset = int64(e.SampleOffsetV0)
	}
	*remaining--
	if *remaining == 0 {
		*run++
	}
	return offset
}

func ints(in []uint32) []int {
	out := make([]int, len(in))
	for i, v := range in {
		out[i] = int(v)
	}
	return out
}
