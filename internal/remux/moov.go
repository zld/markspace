// Package remux writes mapped events into a new MP4 file. Sample payload
// bytes are copied verbatim; only container structure is rebuilt. Every
// written event gets its output byte offset recorded for the manifest.
package remux

import (
	"github.com/abema/go-mp4"

	"mediacore/internal/clock"
	"mediacore/internal/model"
)

// buildMoov serialises the moov box. stco supplies the chunk offset per
// output track (zero on the measurement pass).
func buildMoov(outs []model.OutputTrack, samples map[string][]int, events []model.Event, chapterIdx []int, stco map[string]uint32) ([]byte, error) {
	var m memSeeker
	w := mp4.NewWriter(&m)
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeMoov()}); err != nil {
		return nil, err
	}
	marshal := func(box mp4.IImmutableBox) error {
		if _, err := w.StartBox(&mp4.BoxInfo{Type: box.GetType()}); err != nil {
			return err
		}
		if _, err := mp4.Marshal(w, box, mp4.Context{}); err != nil {
			return err
		}
		_, err := w.EndBox()
		return err
	}
	if err := marshal(&mp4.Mvhd{Timescale: 1000, NextTrackID: uint32(len(outs) + 1)}); err != nil {
		return nil, err
	}
	for ti, o := range outs {
		idxs := samples[o.ID]
		if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeTrak()}); err != nil {
			return nil, err
		}
		tkhd := &mp4.Tkhd{TrackID: uint32(ti + 1)}
		tkhd.Matrix = [9]int32{0x10000, 0, 0, 0, 0x10000, 0, 0, 0, 0x40000000}
		if err := marshal(tkhd); err != nil {
			return nil, err
		}
		// Preserve a non-zero presentation start via an edit list.
		if len(idxs) > 0 && events[idxs[0]].Ticks > 0 {
			if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeEdts()}); err != nil {
				return nil, err
			}
			var dur uint64
			for _, idx := range idxs {
				dur += uint64(max64(events[idx].Duration, 0))
			}
			elst := &mp4.Elst{EntryCount: 1, Entries: []mp4.ElstEntry{{
				SegmentDurationV0: uint32(clock.Convert(dur, clock.UnifiedTimescale, 1000)),
				MediaTimeV0:       int32(clock.Convert(uint64(events[idxs[0]].Ticks), clock.UnifiedTimescale, o.Timescale)),
				MediaRateInteger:  1,
			}}}
			if err := marshal(elst); err != nil {
				return nil, err
			}
			if _, err := w.EndBox(); err != nil { // edts
				return nil, err
			}
		}
		if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeMdia()}); err != nil {
			return nil, err
		}
		var dur uint64
		for _, idx := range idxs {
			dur += uint64(max64(events[idx].Duration, 0))
		}
		mdhd := &mp4.Mdhd{
			Timescale:  o.Timescale,
			DurationV0: uint32(clock.Convert(dur, clock.UnifiedTimescale, o.Timescale)),
			Language:   [3]byte{'u', 'n', 'd'},
		}
		if err := marshal(mdhd); err != nil {
			return nil, err
		}
		hdlr := &mp4.Hdlr{HandlerType: handlerFor(o.Kind), Name: "mediacore"}
		if err := marshal(hdlr); err != nil {
			return nil, err
		}
		if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeMinf()}); err != nil {
			return nil, err
		}
		if err := writeMediaHeader(w, o.Kind); err != nil {
			return nil, err
		}
		if err := writeStbl(w, marshal, o, idxs, events, stco[o.ID]); err != nil {
			return nil, err
		}
		for _, closeType := range []mp4.BoxType{mp4.BoxTypeMinf(), mp4.BoxTypeMdia(), mp4.BoxTypeTrak()} {
			_ = closeType
			if _, err := w.EndBox(); err != nil {
				return nil, err
			}
		}
	}
	if len(chapterIdx) > 0 {
		if err := writeChpl(w, chapterIdx, events); err != nil {
			return nil, err
		}
	}
	if _, err := w.EndBox(); err != nil { // moov
		return nil, err
	}
	return m.buf, nil
}
