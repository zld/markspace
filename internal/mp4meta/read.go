package mp4meta

import (
	"bytes"
	"errors"
	"fmt"

	"github.com/abema/go-mp4"
)

// Decode reads a metadata document back and returns one entry per track in
// file order. Structurally inconsistent boxes are errors: the metadata is an
// audit artifact, so a silent omission would be worse than a hard failure.
func Decode(data []byte) ([]LayerMeta, error) {
	type acc struct {
		meta   LayerMeta
		deltas []mp4.SttsEntry
		sync   []uint32
		sizes  []uint32
	}
	var tracks []*acc
	var cur *acc

	finalize := func() error {
		if cur == nil {
			return nil
		}
		m := cur.meta
		if m.Timescale == 0 {
			return fmt.Errorf("mp4meta: track %d has no timescale", m.TrackID)
		}
		// stts holds one entry per sample; sample i starts at the sum of the
		// first i deltas.
		times := make([]int64, 0, 64)
		var t int64
		for _, d := range cur.deltas {
			for n := uint32(0); n < d.SampleCount; n++ {
				times = append(times, t)
				t += int64(d.SampleDelta)
			}
		}
		m.SampleCount = len(times)
		for _, s := range cur.sync {
			if s == 0 || int(s) > len(times) {
				return fmt.Errorf("mp4meta: sync sample %d outside %d samples", s, len(times))
			}
			m.KeyframeTimes = append(m.KeyframeTimes, times[s-1])
			m.KeyframeFrames = append(m.KeyframeFrames, s-1)
		}
		for _, s := range cur.sizes {
			m.TotalBytes += uint64(s)
		}
		cur.meta = m
		tracks = append(tracks, cur)
		cur = nil
		return nil
	}

	_, err := mp4.ReadBoxStructure(bytes.NewReader(data), func(h *mp4.ReadHandle) (interface{}, error) {
		switch h.BoxInfo.Type {
		case mp4.BoxTypeMoov(), mp4.BoxTypeMdia(), mp4.BoxTypeMinf(), mp4.BoxTypeStbl():
			_, err := h.Expand()
			return nil, err
		case mp4.BoxTypeTrak():
			if err := finalize(); err != nil {
				return nil, err
			}
			cur = &acc{}
			_, err := h.Expand()
			return nil, err
		case mp4.BoxTypeTkhd():
			box, _, err := h.ReadPayload()
			if err != nil || cur == nil {
				return nil, err
			}
			cur.meta.TrackID = box.(*mp4.Tkhd).TrackID
		case mp4.BoxTypeMdhd():
			box, _, err := h.ReadPayload()
			if err != nil || cur == nil {
				return nil, err
			}
			mdhd := box.(*mp4.Mdhd)
			cur.meta.Timescale = mdhd.Timescale
			cur.meta.Duration = uint64(mdhd.DurationV0)
		case mp4.BoxTypeHdlr():
			box, _, err := h.ReadPayload()
			if err != nil || cur == nil {
				return nil, err
			}
			cur.meta.Handler = box.(*mp4.Hdlr).HandlerType
		case mp4.BoxTypeStts():
			box, _, err := h.ReadPayload()
			if err != nil || cur == nil {
				return nil, err
			}
			cur.deltas = box.(*mp4.Stts).Entries
		case mp4.BoxTypeStss():
			box, _, err := h.ReadPayload()
			if err != nil || cur == nil {
				return nil, err
			}
			cur.sync = box.(*mp4.Stss).SampleNumber
		case mp4.BoxTypeStsz():
			box, _, err := h.ReadPayload()
			if err != nil || cur == nil {
				return nil, err
			}
			cur.sizes = box.(*mp4.Stsz).EntrySize
		}
		return nil, nil
	})
	if err != nil {
		return nil, err
	}
	if err := finalize(); err != nil {
		return nil, err
	}
	if len(tracks) == 0 {
		return nil, errors.New("mp4meta: document has no tracks")
	}
	out := make([]LayerMeta, 0, len(tracks))
	for _, t := range tracks {
		out = append(out, t.meta)
	}
	return out, nil
}
