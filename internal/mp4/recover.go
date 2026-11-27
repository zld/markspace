package mp4

import (
	"context"
	"errors"
	"fmt"

	"mediapipe/internal/model"
)

// mdatRange is one media-data area of the input.
type mdatRange struct {
	start int64
	end   int64
}

func (s *Stream) mediaRanges() ([]mdatRange, error) {
	var out []mdatRange
	err := s.Walk(context.Background(), func(b Box) error {
		if b.Type == "mdat" {
			out = append(out, mdatRange{start: b.Offset + b.HeaderSize, end: b.PayloadEnd()})
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// recoverSamples reconstructs locatable samples when the stbl index is missing
// or internally inconsistent. It only trusts data it can bound: sample sizes
// must come from a parseable stsz, and every reconstructed sample must lie
// inside an mdat. Anything beyond the available media data becomes a reported
// gap, never an invented sample.
func recoverSamples(s *Stream, trak Box, stbl Box, t *stblTables) ([]Sample, []Gap, string, error) {
	if len(t.stsz) == 0 {
		return nil, nil, "", s.corrupt("recover", stbl.Offset,
			errors.New("cannot recover: sample size table missing or unusable"))
	}
	if len(t.stts) == 0 {
		return nil, nil, "", s.corrupt("recover", stbl.Offset,
			errors.New("cannot recover: time-to-sample table missing or unusable"))
	}
	ranges, err := s.mediaRanges()
	if err != nil {
		return nil, nil, "", err
	}
	if len(ranges) == 0 {
		return nil, nil, "", s.corrupt("recover", trak.Offset, errors.New("cannot recover: no mdat"))
	}
	note := "recovered from sample table and media data"
	if t.err != nil {
		note = "recovered after table error: " + t.err.Error()
	}

	// Decode timeline from stts, independent of physical locality.
	dts := make([]int64, len(t.stsz))
	durs := make([]int64, len(t.stsz))
	var cur int64
	si := 0
	for _, e := range t.stts {
		for n := uint32(0); n < e.count && si < len(dts); n++ {
			dts[si] = cur
			durs[si] = int64(e.delta)
			cur += int64(e.delta)
			si++
		}
		if si >= len(dts) {
			break
		}
	}
	if si < len(dts) {
		note += fmt.Sprintf("; %d samples have no decode duration", len(dts)-si)
	}

	chunks := t.stco
	perChunk := t.stsc
	if len(perChunk) == 0 {
		perChunk = []stscEntry{{firstChunk: 1, samplesPerChunk: 1}}
		note += "; stsc missing, assuming one sample per chunk"
	}
	if len(chunks) == 0 {
		// Pack the whole media area as a single chunk starting at the first
		// mdat payload; this is the only placement that can be bounded.
		chunks = []uint64{uint64(ranges[0].start)}
		perChunk = []stscEntry{{firstChunk: 1, samplesPerChunk: uint32(len(t.stsz))}}
		note += "; chunk offsets missing, packing from first mdat"
	}

	inMedia := func(off, size int64) int {
		for i, r := range ranges {
			if off >= r.start && off+size <= r.end {
				return i
			}
		}
		return -1
	}

	out := make([]Sample, 0, len(t.stsz))
	used := make([]bool, len(ranges))
	var sampleNum uint32 = 1
	for chunk := 0; chunk < len(chunks) && int(sampleNum) <= len(t.stsz); chunk++ {
		per := samplesInChunk(perChunk, chunk+1)
		off := int64(chunks[chunk])
		for j := uint32(0); j < per && int(sampleNum) <= len(t.stsz); j++ {
			size := int64(t.stsz[sampleNum-1])
			ri := inMedia(off, size)
			if ri < 0 {
				return nil, nil, "", s.corrupt("recover", int64(chunks[chunk]), fmt.Errorf(
					"%w: recovered sample %d at %d size %d outside mdat", ErrOutOfBounds, sampleNum, off, size))
			}
			used[ri] = true
			sync := true
			if t.hasStss {
				sync = t.stss[sampleNum]
			}
			out = append(out, Sample{
				Index:     int(sampleNum),
				Offset:    off,
				Size:      size,
				DecodeTks: dts[sampleNum-1],
				Duration:  durs[sampleNum-1],
				Sync:      sync,
			})
			off += size
			sampleNum++
		}
	}

	// Any media area not referenced by the recovered samples is an
	// unlocatable region and must be surfaced as a gap.
	var gaps []Gap
	for i, r := range ranges {
		if used[i] {
			continue
		}
		gaps = append(gaps, Gap{Duration: -1, Bytes: r.end - r.start})
	}
	if len(out) == 0 {
		return nil, nil, "", s.corrupt("recover", stbl.Offset, errors.New("recovery produced no samples"))
	}
	return out, gaps, note, nil
}

// Validate ensures sample payloads stay inside the input; used by callers
// before trusting a fully parsed index.
func (idx *Index) Validate(size int64, asset string) error {
	for _, t := range idx.Tracks {
		var prevEnd int64 = -1
		for _, sm := range t.Samples {
			if sm.Size < 0 || sm.Offset < 0 {
				return model.New(model.KindCorrupt, model.StageIndex, asset, "validate",
					errors.New("negative sample offset/size")).WithTrack(fmt.Sprint(t.TrackID)).
					WithOffset(sm.Offset)
			}
			if sm.End() > size {
				return model.New(model.KindCorrupt, model.StageIndex, asset, "validate",
					fmt.Errorf("%w: sample %d ends at %d beyond input %d",
						ErrOutOfBounds, sm.Index, sm.End(), size)).
					WithTrack(fmt.Sprint(t.TrackID)).WithOffset(sm.Offset)
			}
			if sm.Offset < prevEnd {
				return model.New(model.KindCorrupt, model.StageIndex, asset, "validate",
					errors.New("sample offsets overlap")).WithTrack(fmt.Sprint(t.TrackID)).
					WithOffset(sm.Offset)
			}
			prevEnd = sm.End()
		}
	}
	return nil
}
