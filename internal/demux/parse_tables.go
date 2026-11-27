package demux

import (
	"errors"
	"fmt"
	"io"

	"github.com/abema/go-mp4"
	"github.com/zld/continuity/internal/clock"
)

var (
	errTrackNotFound  = errors.New("track not found")
	errSampleOutOfRange = errors.New("sample index out of range")
	errNoMoov         = errors.New("missing moov box")
	errNoTracks       = errors.New("moov contains no trak")
)

// parse walks the moov box tree once and materializes every track's table.
func parse(rs io.ReadSeeker) (*File, error) {
	trakInfos, err := mp4.ExtractBox(rs, nil, boxPath(mp4.BoxTypeMoov(), mp4.BoxTypeTrak()))
	if err != nil {
		return nil, clock.Wrap(clock.StageDemux, "extract-trak", mapReadErr(err))
	}
	if len(trakInfos) == 0 {
		// Distinguish a structurally truncated file from a merely empty one.
		if _, perr := mp4.ExtractBox(rs, nil, boxPath(mp4.BoxTypeMoov())); perr != nil {
			return nil, clock.Wrap(clock.StageDemux, "extract-moov", mapReadErr(perr))
		}
		return nil, clock.NewError(clock.StageDemux, "parse", errNoTracks)
	}

	out := &File{}
	for ti, trak := range trakInfos {
		tr, err := parseTrack(rs, trak, uint32(ti))
		if err != nil {
			return nil, err
		}
		out.Tracks = append(out.Tracks, tr)
	}
	return out, nil
}

func parseTrack(rs io.ReadSeeker, trak *mp4.BoxInfo, trackID uint32) (*Track, error) {
	// When reading from the trak box as parent, the handler path starts at
	// trak's children, so paths begin with mdia/tkhd rather than trak.
	mdhds, err := mp4.ExtractBoxesWithPayload(rs, trak, []mp4.BoxPath{
		boxPath(mp4.BoxTypeMdia(), mp4.BoxTypeMdhd()),
		boxPath(mp4.BoxTypeMdia(), mp4.BoxTypeHdlr()),
		boxPath(mp4.BoxTypeMdia(), mp4.BoxTypeMinf(), mp4.BoxTypeStbl(), mp4.BoxTypeStts()),
		boxPath(mp4.BoxTypeMdia(), mp4.BoxTypeMinf(), mp4.BoxTypeStbl(), mp4.BoxTypeStsc()),
		boxPath(mp4.BoxTypeMdia(), mp4.BoxTypeMinf(), mp4.BoxTypeStbl(), mp4.BoxTypeStsz()),
		boxPath(mp4.BoxTypeMdia(), mp4.BoxTypeMinf(), mp4.BoxTypeStbl(), mp4.BoxTypeStco()),
		boxPath(mp4.BoxTypeMdia(), mp4.BoxTypeMinf(), mp4.BoxTypeStbl(), mp4.BoxTypeCo64()),
		boxPath(mp4.BoxTypeMdia(), mp4.BoxTypeMinf(), mp4.BoxTypeStbl(), mp4.BoxTypeStss()),
		boxPath(mp4.BoxTypeTkhd()),
	})
	if err != nil {
		return nil, clock.NewError(clock.StageDemux, "extract-stbl", mapReadErr(err)).WithTrack(trackID)
	}
	byType := map[mp4.BoxType]*mp4.BoxInfoWithPayload{}
	for _, item := range mdhds {
		byType[item.Info.Type] = item
	}

	tr := &Track{ID: trackID + 1}
	if md, ok := byType[mp4.BoxTypeMdhd()]; ok {
		mdhd := md.Payload.(*mp4.Mdhd)
		tr.Timescale = mdhd.Timescale
	}
	if tr.Timescale == 0 {
		return nil, clock.NewError(clock.StageDemux, "mdhd-timescale",
			fmt.Errorf("%w: track %d has no usable time base", clock.ErrCorrupt, trackID)).WithTrack(trackID)
	}
	if h, ok := byType[mp4.BoxTypeHdlr()]; ok {
		tr.Video = string(h.Payload.(*mp4.Hdlr).HandlerType[:]) == "vide"
	}
	if tk, ok := byType[mp4.BoxTypeTkhd()]; ok {
		tr.ID = tk.Payload.(*mp4.Tkhd).TrackID
	}

	dts, durations, present, err := expandStts(byType, tr.Timescale, trackID)
	if err != nil {
		return nil, err
	}
	sizes, err := expandStsz(byType, len(dts), trackID)
	if err != nil {
		return nil, err
	}
	offsets, err := expandOffsets(rs, trak, byType, sizes, trackID)
	if err != nil {
		return nil, err
	}
	if stss, ok := byType[mp4.BoxTypeStss()]; ok {
		tr.SyncRows = stss.Payload.(*mp4.Stss).SampleNumber
	}
	syncSet := map[uint32]bool{}
	for _, row := range tr.SyncRows {
		if row == 0 || int(row-1) >= len(dts) {
			return nil, clock.NewError(clock.StageDemux, "stss-range",
				fmt.Errorf("%w: sync sample %d outside 1..%d", clock.ErrCorrupt, row, len(dts))).
				WithTrack(trackID).WithSample(row - 1)
		}
		syncSet[row-1] = true
	}

	tr.Samples = make([]Sample, len(dts))
	for i := range dts {
		tr.Samples[i] = Sample{
			Index:      uint32(i),
			DTS:        dts[i],
			Duration:   durations[i],
			FileOffset: offsets[i],
			Size:       sizes[i],
			Sync:       syncSet[uint32(i)],
			Present:    present[i],
		}
	}
	return tr, nil
}

// expandStts turns run-length stts entries into one DTS/duration row per sample.
func expandStts(byType map[mp4.BoxType]*mp4.BoxInfoWithPayload, ts, trackID uint32) (
	[]clock.Rational, []clock.Rational, []bool, error) {
	box, ok := byType[mp4.BoxTypeStts()]
	if !ok {
		return nil, nil, nil, clock.NewError(clock.StageDemux, "stts-missing",
			fmt.Errorf("%w: no stts box", clock.ErrCorrupt)).WithTrack(trackID)
	}
	stts := box.Payload.(*mp4.Stts)
	var dts, dur []clock.Rational
	var present []bool
	var ticks uint64
	for _, e := range stts.Entries {
		if e.SampleCount == 0 {
			continue
		}
		step, err := clock.FromTicks(uint64(e.SampleDelta), ts)
		if err != nil {
			return nil, nil, nil, clock.NewError(clock.StageDemux, "stts-delta", err).WithTrack(trackID)
		}
		for k := uint32(0); k < e.SampleCount; k++ {
			t, err := clock.FromTicks(ticks, ts)
			if err != nil {
				return nil, nil, nil, clock.NewError(clock.StageDemux, "stts-dts", err).
					WithTrack(trackID).WithSample(uint32(len(dts)))
			}
			dts = append(dts, t)
			dur = append(dur, step)
			present = append(present, e.SampleDelta > 0)
			ticks += uint64(e.SampleDelta)
		}
	}
	if len(dts) == 0 {
		return nil, nil, nil, clock.NewError(clock.StageDemux, "stts-empty",
			fmt.Errorf("%w: stts declares no samples", clock.ErrCorrupt)).WithTrack(trackID)
	}
	return dts, dur, present, nil
}
