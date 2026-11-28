package mp4

import (
	"fmt"
	"io"

	gomp4 "github.com/abema/go-mp4"

	"medcheck/internal/model"
)

// trackAcc accumulates the sample-table boxes of one trak.
type trackAcc struct {
	id        uint32
	timescale uint32
	chunks    []int64
	sizes     []uint32
	deltas    []uint32
	stsc      []gomp4.StscEntry
	keySet    map[uint32]struct{}
}

func readTracks(r io.ReadSeeker, stream string) ([]*TrackScan, error) {
	var accs []*trackAcc
	_, err := gomp4.ReadBoxStructure(r, func(h *gomp4.ReadHandle) (interface{}, error) {
		switch h.BoxInfo.Type {
		case gomp4.BoxTypeMoov(), gomp4.BoxTypeTrak(),
			gomp4.BoxTypeMdia(), gomp4.BoxTypeMinf(), gomp4.BoxTypeStbl():
			return h.Expand()
		case gomp4.BoxTypeTkhd():
			box, _, err := h.ReadPayload()
			if err != nil {
				return nil, truncErr(h, stream, err)
			}
			tk := box.(*gomp4.Tkhd)
			accs = append(accs, &trackAcc{id: tk.TrackID, keySet: map[uint32]struct{}{}})
			return nil, nil
		case gomp4.BoxTypeMdhd():
			box, _, err := h.ReadPayload()
			if err != nil {
				return nil, truncErr(h, stream, err)
			}
			accs[len(accs)-1].timescale = box.(*gomp4.Mdhd).Timescale
			return nil, nil
		case gomp4.BoxTypeStco():
			box, _, err := h.ReadPayload()
			if err != nil {
				return nil, truncErr(h, stream, err)
			}
			for _, o := range box.(*gomp4.Stco).ChunkOffset {
				accs[len(accs)-1].chunks = append(accs[len(accs)-1].chunks, int64(o))
			}
			return nil, nil
		case gomp4.BoxTypeCo64():
			box, _, err := h.ReadPayload()
			if err != nil {
				return nil, truncErr(h, stream, err)
			}
			for _, o := range box.(*gomp4.Co64).ChunkOffset {
				accs[len(accs)-1].chunks = append(accs[len(accs)-1].chunks, int64(o))
			}
			return nil, nil
		case gomp4.BoxTypeStsz():
			box, _, err := h.ReadPayload()
			if err != nil {
				return nil, truncErr(h, stream, err)
			}
			st := box.(*gomp4.Stsz)
			acc := accs[len(accs)-1]
			if st.SampleSize != 0 {
				acc.sizes = make([]uint32, st.SampleCount)
				for i := range acc.sizes {
					acc.sizes[i] = st.SampleSize
				}
			} else {
				acc.sizes = append(acc.sizes, st.EntrySize...)
			}
			return nil, nil
		case gomp4.BoxTypeStts():
			box, _, err := h.ReadPayload()
			if err != nil {
				return nil, truncErr(h, stream, err)
			}
			for _, e := range box.(*gomp4.Stts).Entries {
				for i := uint32(0); i < e.SampleCount; i++ {
					accs[len(accs)-1].deltas = append(accs[len(accs)-1].deltas, e.SampleDelta)
				}
			}
			return nil, nil
		case gomp4.BoxTypeStsc():
			box, _, err := h.ReadPayload()
			if err != nil {
				return nil, truncErr(h, stream, err)
			}
			accs[len(accs)-1].stsc = append(accs[len(accs)-1].stsc, box.(*gomp4.Stsc).Entries...)
			return nil, nil
		case gomp4.BoxTypeStss():
			box, _, err := h.ReadPayload()
			if err != nil {
				return nil, truncErr(h, stream, err)
			}
			acc := accs[len(accs)-1]
			for _, n := range box.(*gomp4.Stss).SampleNumber {
				acc.keySet[n-1] = struct{}{} // stss is 1-based
			}
			return nil, nil
		default:
			return nil, nil // skip unrelated boxes
		}
	})
	if err != nil {
		if ce := model.AsCodeError(err); ce != nil {
			return nil, ce
		}
		return nil, err
	}
	out := make([]*TrackScan, 0, len(accs))
	for _, acc := range accs {
		out = append(out, acc.build())
	}
	return out, nil
}

func truncErr(h *gomp4.ReadHandle, stream string, cause error) *model.CodeError {
	return &model.CodeError{
		Kind:     model.KindTruncatedBox,
		Offset:   model.RawOffset{Stream: stream, Start: int64(h.BoxInfo.Offset), End: int64(h.BoxInfo.Offset + h.BoxInfo.Size)},
		FirstPos: int64(h.BoxInfo.Offset),
		Message:  fmt.Sprintf("unmarshal %s: %v", h.BoxInfo.Type.String(), cause),
	}
}

// samplesPerChunk resolves the stsc run table for chunk index (0-based).
func samplesPerChunk(entries []gomp4.StscEntry, chunkIdx uint32) uint32 {
	run := entries[0].SamplesPerChunk
	for _, e := range entries {
		if e.FirstChunk-1 <= chunkIdx { // FirstChunk is 1-based
			run = e.SamplesPerChunk
		} else {
			break
		}
	}
	return run
}

func (a *trackAcc) build() *TrackScan {
	ts := &TrackScan{
		TrackID:   a.id,
		Timescale: model.Rational{Num: uint64(a.timescale), Den: 1},
		KeyFrames: nil,
	}
	var sampleIdx, tickAcc uint32
	var cursor int64
	for chunkIdx, chunkOff := range a.chunks {
		cursor = chunkOff
		n := samplesPerChunk(a.stsc, uint32(chunkIdx))
		for j := uint32(0); j < n; j++ {
			if int(sampleIdx) >= len(a.sizes) {
				return ts
			}
			size := a.sizes[sampleIdx]
			var ticks int64
			if int(sampleIdx) < len(a.deltas) {
				ticks = int64(tickAcc)
				tickAcc += a.deltas[sampleIdx]
			}
			isKey := false
			if len(a.keySet) == 0 {
				isKey = sampleIdx == 0 // no stss: only the first sample is key
			} else {
				_, isKey = a.keySet[sampleIdx]
			}
			ts.Samples = append(ts.Samples, SampleInfo{
				Index:      sampleIdx,
				Offset:     cursor,
				Size:       size,
				Ticks:      ticks,
				IsKeyFrame: isKey,
			})
			if isKey {
				ts.KeyFrames = append(ts.KeyFrames, sampleIdx)
			}
			cursor += int64(size)
			sampleIdx++
		}
	}
	return ts
}
