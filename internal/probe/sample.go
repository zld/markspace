package probe

import (
	"fmt"
	"io"

	"github.com/abema/go-mp4"
	"github.com/broadcast/mediad/internal/model"
)

// parseTrack turns one trak box into a model.Track. Sample-table limits and
// consistency are validated here; every defect is reported through errs.
func parseTrack(r io.ReadSeeker, trak *mp4.BoxInfo, cfg ProbeConfig, errs *model.ErrorSet) (*model.Track, error) {
	paths := []mp4.BoxPath{
		{mp4.BoxTypeTkhd()},
		{mp4.BoxTypeMdia(), mp4.BoxTypeMdhd()},
		{mp4.BoxTypeMdia(), mp4.BoxTypeHdlr()},
		{mp4.BoxTypeMdia(), mp4.BoxTypeMinf(), mp4.BoxTypeStbl(), mp4.BoxTypeStts()},
		{mp4.BoxTypeMdia(), mp4.BoxTypeMinf(), mp4.BoxTypeStbl(), mp4.BoxTypeCtts()},
		{mp4.BoxTypeMdia(), mp4.BoxTypeMinf(), mp4.BoxTypeStbl(), mp4.BoxTypeStsc()},
		{mp4.BoxTypeMdia(), mp4.BoxTypeMinf(), mp4.BoxTypeStbl(), mp4.BoxTypeStsz()},
		{mp4.BoxTypeMdia(), mp4.BoxTypeMinf(), mp4.BoxTypeStbl(), mp4.BoxTypeStco()},
		{mp4.BoxTypeMdia(), mp4.BoxTypeMinf(), mp4.BoxTypeStbl(), mp4.BoxTypeCo64()},
		{mp4.BoxTypeMdia(), mp4.BoxTypeMinf(), mp4.BoxTypeStbl(), mp4.BoxTypeStss()},
	}
	bips, err := mp4.ExtractBoxesWithPayload(r, trak, paths)
	if err != nil {
		return nil, err
	}
	var (
		tkhd *mp4.Tkhd
		mdhd *mp4.Mdhd
		hdlr *mp4.Hdlr
		stts *mp4.Stts
		ctts *mp4.Ctts
		stsc *mp4.Stsc
		stsz *mp4.Stsz
		stco *mp4.Stco
		co64 *mp4.Co64
		stss *mp4.Stss
	)
	for _, b := range bips {
		switch b.Info.Type {
		case mp4.BoxTypeTkhd():
			tkhd = b.Payload.(*mp4.Tkhd)
		case mp4.BoxTypeMdhd():
			mdhd = b.Payload.(*mp4.Mdhd)
		case mp4.BoxTypeHdlr():
			hdlr = b.Payload.(*mp4.Hdlr)
		case mp4.BoxTypeStts():
			stts = b.Payload.(*mp4.Stts)
		case mp4.BoxTypeCtts():
			ctts = b.Payload.(*mp4.Ctts)
		case mp4.BoxTypeStsc():
			stsc = b.Payload.(*mp4.Stsc)
		case mp4.BoxTypeStsz():
			stsz = b.Payload.(*mp4.Stsz)
		case mp4.BoxTypeStco():
			stco = b.Payload.(*mp4.Stco)
		case mp4.BoxTypeCo64():
			co64 = b.Payload.(*mp4.Co64)
		case mp4.BoxTypeStss():
			stss = b.Payload.(*mp4.Stss)
		}
	}
	if tkhd == nil || mdhd == nil || stts == nil || stsc == nil || stsz == nil {
		return nil, fmt.Errorf("incomplete sample table on track %d", safeTrackID(tkhd))
	}
	if mdhd.Timescale == 0 {
		return nil, fmt.Errorf("track %d declares zero timescale", safeTrackID(tkhd))
	}

	t := &model.Track{
		ID:                   tkhd.TrackID,
		Timescale:            mdhd.Timescale,
		Handler:              hdlrName(hdlr),
		Kind:                 classifyHandler(hdlr),
		KeyframeInterval:     cfg.MaxKeyframeGap,
		KeyframeIndexPresent: stss != nil,
	}

	sizes := expandSizes(stsz, errs, t.ID)
	if len(sizes) > model.MaxSamples {
		return nil, &model.LimitError{What: "samples", Got: len(sizes), Max: model.MaxSamples}
	}
	deltas := expandStts(stts, len(sizes), errs, t)
	offsets, chunkOf := expandChunks(stsc, stco, co64, sizes, errs, t)
	compositions := expandCtts(ctts, len(sizes), errs, t)

	// A leading stts run with zero-sized samples is impossible, so an offset
	// before the first delta is expressed via an empty edit (elst). When no
	// edit list exists the timeline starts at zero.
	startOffset := model.Zero()
	if elst, elErr := extractOne(r, trak,
		mp4.BoxTypeEdts(), mp4.BoxTypeElst()); elErr == nil {
		if e, ok := elst.Payload.(*mp4.Elst); ok && len(e.Entries) > 0 {
			mt := e.GetMediaTime(0)
			if mt > 0 {
				off, oerr := model.FromUnits(mt, mdhd.Timescale)
				if oerr != nil {
					return nil, oerr
				}
				startOffset = off
			}
		}
	}

	t.Samples = buildSamplesAt(sizes, deltas, compositions, offsets, chunkOf,
		mdhd.Timescale, stss, t, errs, startOffset)
	if len(t.Samples) == 0 {
		return nil, fmt.Errorf("track %d has no samples", t.ID)
	}
	// Audit 16-bit sample numbering (RTP-style) for gap-free sequencing.
	AuditSampleNumbers(t, errs)
	t.StartTime = t.Samples[0].DecodeTime
	t.EndTime = t.Samples[len(t.Samples)-1].DecodeTime.Add(t.Samples[len(t.Samples)-1].Duration)

	attachFormat(r, trak, t, cfg, errs)
	analyzeKeyframes(t, cfg, errs)
	return t, nil
}

func safeTrackID(t *mp4.Tkhd) uint32 {
	if t == nil {
		return 0
	}
	return t.TrackID
}

func hdlrName(h *mp4.Hdlr) string {
	if h == nil {
		return ""
	}
	return string(h.HandlerType[:])
}

func classifyHandler(h *mp4.Hdlr) model.MediaKind {
	if h == nil {
		return model.KindOther
	}
	switch string(h.HandlerType[:]) {
	case "vide":
		return model.KindVideo
	case "soun":
		return model.KindAudio
	case "hint":
		return model.KindHint
	default:
		return model.KindOther
	}
}

// expandSizes resolves the uniform-size vs per-sample-size stsz variants.
func expandSizes(stsz *mp4.Stsz, errs *model.ErrorSet, trackID uint32) []uint32 {
	if stsz.SampleSize != 0 {
		out := make([]uint32, stsz.SampleCount)
		for i := range out {
			out[i] = stsz.SampleSize
		}
		return out
	}
	if int(stsz.SampleCount) != len(stsz.EntrySize) {
		errs.Add(model.MediaError{
			Kind: model.KindTruncatedBox, TrackID: trackID, SampleIndex: -1,
			Detail: fmt.Sprintf("stsz count %d != entries %d", stsz.SampleCount, len(stsz.EntrySize)),
		})
	}
	return append([]uint32(nil), stsz.EntrySize...)
}
