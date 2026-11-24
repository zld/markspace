package mp4

import (
	"errors"
	"fmt"
	"io"

	mp4 "github.com/abema/go-mp4"

	"mediasync/internal/model"
)

type moovMeta struct {
	timescale uint32
	duration  uint64
}

// inspectMoov expands moov one level to find mvhd and each trak, then expands
// each track to read tkhd, mdhd, hdlr, edts/elst and stbl/stss.
func inspectMoov(r io.ReadSeeker, path string, moov *mp4.BoxInfo, info *Info) error {
	_, err := mp4.ReadBoxStructureFromInternal(r, moov, func(h *mp4.ReadHandle) (interface{}, error) {
		b := h.BoxInfo
		switch b.Type {
		case mp4.BoxTypeMvhd():
			box, _, err := h.ReadPayload()
			if err != nil {
				return nil, err
			}
			mvhd, ok := box.(*mp4.Mvhd)
			if !ok {
				return nil, fmt.Errorf("unexpected mvhd payload %T", box)
			}
			info.Timescale = mvhd.Timescale
			info.DurationTicks = mvhd.GetDuration()
			if d, err := model.ToNanos(int64(info.DurationTicks), info.Timescale); err == nil {
				info.Duration = d
			} else {
				return nil, err
			}
		case mp4.BoxTypeTrak():
			if _, err := h.Expand(); err != nil {
				return nil, err
			}
		}
		return nil, nil
	})
	return err
}

// trackMeta collects fields found while walking a track, then assembles the
// TrackInfo after the walk because edts may precede or follow mdia.
type trackMeta struct {
	tkhd  *mp4.Tkhd
	mdhd  *mp4.Mdhd
	hdlr  *mp4.Hdlr
	edts  []*mp4.Elst
	stss  *mp4.Stss
	depth int
}

// InspectTrack expands a single trak, used by tests and by the fixture
// generator to round-trip files written through go-mp4.
func InspectTrack(r io.ReadSeeker, path string, trak *mp4.BoxInfo) (TrackInfo, model.ErrorList, error) {
	var errs model.ErrorList
	tm := &trackMeta{}
	_, err := mp4.ReadBoxStructureFromInternal(r, trak, trackHandler(path, trak.Offset, tm, &errs))
	if err != nil {
		return TrackInfo{}, errs, err
	}
	ti, terr := buildTrackInfo(path, trak.Offset, tm)
	if terr != nil {
		errs.Add(terr)
		return TrackInfo{}, errs, nil
	}
	return ti, errs, nil
}

func trackHandler(path string, base uint64, tm *trackMeta, errs *model.ErrorList) mp4.ReadHandler {
	return func(h *mp4.ReadHandle) (interface{}, error) {
		b := h.BoxInfo
		loc := model.Loc{File: path, HasByteOffset: true, ByteOffset: int64(b.Offset)}
		switch b.Type {
		case mp4.BoxTypeTkhd():
			box, _, err := h.ReadPayload()
			if err != nil {
				errs.Add(model.NewError(model.CodeMP4Structure, loc, "cannot decode tkhd").Wrap(err))
				return nil, nil
			}
			tm.tkhd = box.(*mp4.Tkhd)
		case mp4.BoxTypeMdhd():
			box, _, err := h.ReadPayload()
			if err != nil {
				errs.Add(model.NewError(model.CodeMP4Structure, loc, "cannot decode mdhd").Wrap(err))
				return nil, nil
			}
			tm.mdhd = box.(*mp4.Mdhd)
		case mp4.BoxTypeHdlr():
			box, _, err := h.ReadPayload()
			if err != nil {
				errs.Add(model.NewError(model.CodeMP4Structure, loc, "cannot decode hdlr").Wrap(err))
				return nil, nil
			}
			tm.hdlr = box.(*mp4.Hdlr)
		case mp4.BoxTypeElst():
			box, _, err := h.ReadPayload()
			if err != nil {
				errs.Add(model.NewError(model.CodeMP4Structure, loc, "cannot decode elst").Wrap(err))
				return nil, nil
			}
			tm.edts = append(tm.edts, box.(*mp4.Elst))
		case mp4.BoxTypeStss():
			box, _, err := h.ReadPayload()
			if err != nil {
				errs.Add(model.NewError(model.CodeMP4Structure, loc, "cannot decode stss").Wrap(err))
				return nil, nil
			}
			tm.stss = box.(*mp4.Stss)
		case mp4.BoxTypeMdia(), mp4.BoxTypeMinf(), mp4.BoxTypeStbl(), mp4.BoxTypeEdts():
			if _, err := h.Expand(); err != nil {
				return nil, err
			}
		}
		return nil, nil
	}
}

func buildTrackInfo(path string, base uint64, tm *trackMeta) (TrackInfo, *model.Error) {
	var ti TrackInfo
	if tm.tkhd != nil {
		ti.ID = tm.tkhd.TrackID
	}
	if tm.hdlr != nil {
		ti.Handler = string(tm.hdlr.HandlerType[:])
	}
	if tm.mdhd == nil {
		return ti, model.NewError(model.CodeMP4Structure,
			model.Loc{File: path, TrackID: ti.ID, HasByteOffset: true, ByteOffset: int64(base)},
			"track %d has no mdhd box", ti.ID)
	}
	ti.Timescale = tm.mdhd.Timescale
	ti.DurationTicks = tm.mdhd.GetDuration()
	if ti.Timescale == 0 {
		return ti, model.NewError(model.CodeMP4Structure,
			model.Loc{File: path, TrackID: ti.ID, Field: "mdhd.timescale", HasByteOffset: true, ByteOffset: int64(base)},
			"track %d has zero timescale", ti.ID)
	}
	d, err := model.ToNanos(int64(ti.DurationTicks), ti.Timescale)
	if err != nil {
		return ti, model.NewError(model.CodeMP4Structure,
			model.Loc{File: path, TrackID: ti.ID, Field: "mdhd.duration", HasByteOffset: true, ByteOffset: int64(base)}, "track %d duration").Wrap(err)
	}
	ti.Duration = d

	// Edit lists: validate each segment against the media duration. A negative
	// media_time is an empty edit (a gap) in ISO BMFF and is represented as a
	// zero-length interval rather than an error.
	var totalEdit uint64
	for _, elst := range tm.edts {
		ti.RawEdits = append(ti.RawEdits, elst.Entries...)
		for i := range elst.Entries {
			dur := elst.GetSegmentDuration(i)
			mt := elst.GetMediaTime(i)
			if mt >= 0 {
				if uint64(mt) > ti.DurationTicks {
					return ti, model.NewError(model.CodeMP4ElstRange,
						model.Loc{File: path, TrackID: ti.ID, Field: fmt.Sprintf("elst[%d].media_time", i), HasByteOffset: true, ByteOffset: int64(base)},
						"edit media_time %d lies beyond media duration %d", mt, ti.DurationTicks)
				}
			}
			ns, err := model.ToNanos(mt, ti.Timescale)
			if err != nil {
				return ti, model.NewError(model.CodeMP4ElstRange,
					model.Loc{File: path, TrackID: ti.ID, Field: fmt.Sprintf("elst[%d].media_time", i), HasByteOffset: true, ByteOffset: int64(base)}, "edit media_time").Wrap(err)
			}
			dn, err := model.ToNanos(int64(dur), ti.Timescale)
			if err != nil {
				return ti, model.NewError(model.CodeMP4ElstRange,
					model.Loc{File: path, TrackID: ti.ID, Field: fmt.Sprintf("elst[%d].segment_duration", i), HasByteOffset: true, ByteOffset: int64(base)}, "edit duration").Wrap(err)
			}
			if ns < 0 {
				ns = 0
			}
			ti.Edit = append(ti.Edit, model.Interval{Start: ns, End: ns + dn})
			if i == 0 && mt >= 0 {
				ti.MediaTime = mt
			}
			totalEdit += dur
		}
	}
	if totalEdit > ti.DurationTicks && totalEdit-ti.DurationTicks > 0 {
		return ti, model.NewError(model.CodeMP4ElstRange,
			model.Loc{File: path, TrackID: ti.ID, Field: "elst.segment_duration", HasByteOffset: true, ByteOffset: int64(base)},
			"edit list covers %d ticks but media lasts %d ticks", totalEdit, ti.DurationTicks)
	}
	if len(ti.Edit) > 0 {
		ti.StartOffset = ti.Edit[0].Start
	}

	if tm.stss != nil {
		for _, n := range tm.stss.SampleNumber {
			ti.Keyframes = append(ti.Keyframes, uint64(n))
		}
	}
	return ti, nil
}

// ReadSyncSamples is a small helper that re-reads the stss of a track, kept
// separate for tests that need to confirm sparse keyframe preservation.
func ReadSyncSamples(f io.ReadSeeker, trak *mp4.BoxInfo) ([]uint64, error) {
	var out []uint64
	_, err := mp4.ReadBoxStructureFromInternal(f, trak, func(h *mp4.ReadHandle) (interface{}, error) {
		if h.BoxInfo.Type != mp4.BoxTypeStss() {
			if h.BoxInfo.Type == mp4.BoxTypeMdia() || h.BoxInfo.Type == mp4.BoxTypeMinf() || h.BoxInfo.Type == mp4.BoxTypeStbl() {
				if _, err := h.Expand(); err != nil {
					return nil, err
				}
			}
			return nil, nil
		}
		box, _, err := h.ReadPayload()
		if err != nil {
			return nil, err
		}
		stss, ok := box.(*mp4.Stss)
		if !ok {
			return nil, errors.New("unexpected stss payload")
		}
		for _, n := range stss.SampleNumber {
			out = append(out, uint64(n))
		}
		return nil, nil
	})
	return out, err
}
