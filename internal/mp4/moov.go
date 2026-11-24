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
// each track to read tkhd, mdhd, hdlr, edts/elst and stbl/stss. Expanding from
// the root box is handled by the walker, which visits moov itself first.
func inspectMoov(r io.ReadSeeker, path string, moov *mp4.BoxInfo, info *Info) error {
	_, err := mp4.ReadBoxStructureFromInternal(r, moov, func(h *mp4.ReadHandle) (interface{}, error) {
		switch h.BoxInfo.Type {
		case mp4.BoxTypeMoov(), mp4.BoxTypeTrak():
			_, err := h.Expand()
			return nil, err
		}
		return nil, nil
	})
	if err != nil {
		return err
	}
	return inspectMoovChildren(r, path, moov, info)
}

// trackMeta collects fields found while walking a track, then assembles the
// TrackInfo after the walk because edts may precede or follow mdia.
type trackMeta struct {
	tkhd *mp4.Tkhd
	mdhd *mp4.Mdhd
	hdlr *mp4.Hdlr
	edts []*mp4.Elst
	stss *mp4.Stss
	stts *mp4.Stts
	// boxOffsets records each decoded box type and offset so diagnostics can
	// point at the box that failed.
	boxOffsets map[string]uint64
}

// InspectTrack expands a single trak, used by tests and by the fixture
// generator to round-trip files written through go-mp4.
func InspectTrack(r io.ReadSeeker, path string, trak *mp4.BoxInfo) (TrackInfo, model.ErrorList, error) {
	return inspectTrack(r, path, trak, 0)
}

// inspectTrack reads one track. movieTimescale is the mvhd timescale, which
// governs elst segment durations; zero disables the edit duration check.
func inspectTrack(r io.ReadSeeker, path string, trak *mp4.BoxInfo, movieTimescale uint32) (TrackInfo, model.ErrorList, error) {
	var errs model.ErrorList
	tm := &trackMeta{boxOffsets: map[string]uint64{}}
	_, err := mp4.ReadBoxStructureFromInternal(r, trak, trackHandler(path, trak.Offset, tm, &errs))
	if err != nil {
		return TrackInfo{}, errs, err
	}
	ti, terr := buildTrackInfo(path, trak.Offset, tm, movieTimescale)
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
		tm.boxOffsets[b.Type.String()] = b.Offset
		switch b.Type {
		case mp4.BoxTypeTrak():
			// The walk starts at the trak itself; descend into it.
			if _, err := h.Expand(); err != nil {
				return nil, err
			}
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
		case mp4.BoxTypeStts():
			box, _, err := h.ReadPayload()
			if err != nil {
				errs.Add(model.NewError(model.CodeMP4Structure, loc, "cannot decode stts").Wrap(err))
				return nil, nil
			}
			tm.stts = box.(*mp4.Stts)
		case mp4.BoxTypeMdia(), mp4.BoxTypeMinf(), mp4.BoxTypeStbl(), mp4.BoxTypeEdts():
			if _, err := h.Expand(); err != nil {
				return nil, err
			}
		}
		return nil, nil
	}
}

func buildTrackInfo(path string, base uint64, tm *trackMeta, movieTimescale uint32) (TrackInfo, *model.Error) {
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
			model.Loc{File: path, TrackID: ti.ID, Field: "mdhd.duration", HasByteOffset: true, ByteOffset: int64(base)}, "track %d duration", ti.ID).Wrap(err)
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
			// elst segment durations are expressed in the movie timescale,
			// while media_time is in the media timescale.
			segScale := ti.Timescale
			if movieTimescale != 0 {
				segScale = movieTimescale
			}
			dn, err := model.ToNanos(int64(dur), segScale)
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
	if movieTimescale == 0 {
		totalEdit = 0 // cannot compare across unknown movie timescale
	}
	if totalEdit > ti.DurationTicks {
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
		dts := sampleDecodeTimes(tm.stts)
		for i, n := range tm.stss.SampleNumber {
			if n == 0 || int(n) > len(dts) {
				return ti, model.NewError(model.CodeMP4ElstRange,
					model.Loc{File: path, TrackID: ti.ID, Field: fmt.Sprintf("stss[%d]", i), HasByteOffset: true, ByteOffset: int64(tm.boxOffsets["stss"])},
					"sync sample %d lies outside the %d samples in stts", n, len(dts))
			}
			media := int64(dts[n-1])
			if ti.MediaTime > 0 {
				media += ti.MediaTime
			}
			ns, err := model.ToNanos(media, ti.Timescale)
			if err != nil {
				return ti, model.NewError(model.CodeMP4Structure,
					model.Loc{File: path, TrackID: ti.ID, Field: fmt.Sprintf("stss[%d]", i), HasByteOffset: true, ByteOffset: int64(tm.boxOffsets["stss"])}, "keyframe time").Wrap(err)
			}
			ti.KeyframeTimes = append(ti.KeyframeTimes, ns)
		}
	}
	return ti, nil
}

// sampleDecodeTimes expands stts into the decode time of each sample,
// 0-based. A missing stts yields no times; callers then cannot map sync sample
// numbers to timestamps and report that instead of guessing.
func sampleDecodeTimes(stts *mp4.Stts) []uint64 {
	if stts == nil {
		return nil
	}
	var out []uint64
	var t uint64
	for _, e := range stts.Entries {
		for i := uint32(0); i < e.SampleCount; i++ {
			out = append(out, t)
			t += uint64(e.SampleDelta)
		}
	}
	return out
}

// ReadSyncSamples is a small helper that re-reads the stss of a track, kept
// separate for tests that need to confirm sparse keyframe preservation.
func ReadSyncSamples(f io.ReadSeeker, trak *mp4.BoxInfo) ([]uint64, error) {
	var out []uint64
	_, err := mp4.ReadBoxStructureFromInternal(f, trak, func(h *mp4.ReadHandle) (interface{}, error) {
		if h.BoxInfo.Type == mp4.BoxTypeTrak() {
			_, err := h.Expand()
			return nil, err
		}
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

// inspectMoovChildren performs the second pass that decodes payloads. Payload
// reads move the file cursor, so they must not interleave with an Expand that
// is still walking the same parent.
func inspectMoovChildren(r io.ReadSeeker, path string, moov *mp4.BoxInfo, info *Info) error {
	var mvhdSeen bool
	var tracks []*mp4.BoxInfo
	_, err := mp4.ReadBoxStructureFromInternal(r, moov, func(h *mp4.ReadHandle) (interface{}, error) {
		switch h.BoxInfo.Type {
		case mp4.BoxTypeMoov():
			_, e := h.Expand()
			return nil, e
		case mp4.BoxTypeTrak():
			b := h.BoxInfo
			tracks = append(tracks, &b)
			_, e := h.Expand()
			return nil, e
		case mp4.BoxTypeMvhd():
			box, _, e := h.ReadPayload()
			if e != nil {
				return nil, e
			}
			mvhd, ok := box.(*mp4.Mvhd)
			if !ok {
				return nil, fmt.Errorf("unexpected mvhd payload %T", box)
			}
			info.Timescale = mvhd.Timescale
			info.DurationTicks = mvhd.GetDuration()
			d, e := model.ToNanos(int64(info.DurationTicks), info.Timescale)
			if e != nil {
				return nil, e
			}
			info.Duration = d
			mvhdSeen = true
		}
		return nil, nil
	})
	if err != nil {
		return err
	}
	if !mvhdSeen {
		info.Diagnostics.Add(model.NewError(model.CodeMP4Structure,
			model.Loc{File: path, HasByteOffset: true, ByteOffset: int64(moov.Offset)},
			"moov has no mvhd box"))
	}
	for _, trak := range tracks {
		ti, terr, e := inspectTrack(r, path, trak, info.Timescale)
		if e != nil {
			return e
		}
		info.Diagnostics = append(info.Diagnostics, terr...)
		if len(terr) == 0 {
			info.Tracks = append(info.Tracks, ti)
		}
	}
	return nil
}
