package metadata

import "mediasync/internal/model"

// Verdict is the adjudicated result across MXF, MP4 and defaults. Each
// disputed field keeps the winner, every original value and a conflict flag.
type Verdict struct {
	Rotation     Field[int]        `json:"rotation"`
	Crop         Field[Crop]       `json:"crop"`
	Color        Field[ColorInfo]  `json:"color"`
	Timescale    Field[uint32]     `json:"timescale"`
	DurationTick Field[int64]      `json:"duration_tick"`
	Tracks       []TrackVerdict    `json:"tracks"`
	Conflicts    []model.ErrorRecord `json:"conflicts"`
}

// TrackVerdict is the adjudicated view of one matched track pair.
type TrackVerdict struct {
	Kind         string          `json:"kind"`
	MXFID        uint32          `json:"mxf_id,omitempty"`
	MP4ID        uint32          `json:"mp4_id,omitempty"`
	DurationTick Field[int64]    `json:"duration_tick"`
	Timescale    Field[uint32]   `json:"timescale"`
	Rotation     Field[int]      `json:"rotation"`
	Crop         Field[Crop]     `json:"crop"`
	Color        Field[ColorInfo] `json:"color"`
	Width        Field[uint32]   `json:"width"`
	Height       Field[uint32]   `json:"height"`
}

// Adjudicate merges the two containers with fixed priority MXF > MP4 >
// default. Every disagreement is recorded as a warning-level conflict and
// flagged on the owning field. Tracks are matched by kind; a kind seen in
// only one container is still emitted with a single-source verdict.
func Adjudicate(mxf, mp4 *ContainerMeta, def Defaults, rec *model.Recorder) *Verdict {
	v := &Verdict{}
	v.Rotation = merge(mxf.Rotation, mp4.Rotation, def.Rotation, mxf.Declared["rotation"], mp4.Declared["rotation"], "rotation", rec)
	v.Crop = merge(mxf.Crop, mp4.Crop, def.Crop, mxf.Declared["crop"], mp4.Declared["crop"], "crop", rec)
	v.Color = merge(mxf.Color, mp4.Color, def.Color, mxf.Declared["color"], mp4.Declared["color"], "color", rec)
	v.Timescale = merge(mxf.Timescale, mp4.Timescale, 0, mxf.Declared["timescale"], mp4.Declared["timescale"], "timescale", rec)
	v.DurationTick = merge(mxf.DurationTicks, mp4.DurationTicks, 0, mxf.Declared["duration"], mp4.Declared["duration"], "duration", rec)

	seen := map[string]bool{}
	for _, mt := range mxf.Tracks {
		seen[mt.Kind] = true
		var pt *TrackMeta
		for i := range mp4.Tracks {
			if mp4.Tracks[i].Kind == mt.Kind {
				pt = &mp4.Tracks[i]
				break
			}
		}
		v.Tracks = append(v.Tracks, adjudicateTrack(&mt, pt, rec))
	}
	for i := range mp4.Tracks {
		if seen[mp4.Tracks[i].Kind] {
			continue
		}
		pt := mp4.Tracks[i]
		v.Tracks = append(v.Tracks, adjudicateTrack(nil, &pt, rec))
	}
	v.Conflicts = rec.Records()
	return v
}

func adjudicateTrack(mt, pt *TrackMeta, rec *model.Recorder) TrackVerdict {
	out := TrackVerdict{Kind: kindOf(mt, pt)}
	var dM, dP map[string]bool
	mxDur, mpDur, mxTS, mpTS := int64(0), int64(0), uint32(0), uint32(0)
	mxRot, mpRot := 0, 0
	var mxCrop, mpCrop Crop
	var mxColor, mpColor ColorInfo
	mxW, mxH, mpW, mpH := uint32(0), uint32(0), uint32(0), uint32(0)
	if mt != nil {
		out.MXFID = mt.TrackID
		dM = mt.Declared
		mxDur, mxTS, mxRot, mxCrop, mxColor, mxW, mxH =
			mt.DurationTicks, mt.Timescale, mt.Rotation, mt.Crop, mt.Color, mt.Width, mt.Height
	}
	if pt != nil {
		out.MP4ID = pt.TrackID
		dP = pt.Declared
		mpDur, mpTS, mpRot, mpCrop, mpColor, mpW, mpH =
			pt.DurationTicks, pt.Timescale, pt.Rotation, pt.Crop, pt.Color, pt.Width, pt.Height
	}
	out.DurationTick = merge(mxDur, mpDur, 0, dM["duration"], dP["duration"], out.Kind+".duration", rec)
	out.Timescale = merge(mxTS, mpTS, 0, dM["timescale"], dP["timescale"], out.Kind+".timescale", rec)
	out.Rotation = merge(mxRot, mpRot, 0, dM["rotation"], dP["rotation"], out.Kind+".rotation", rec)
	out.Crop = merge(mxCrop, mpCrop, Crop{}, dM["crop"], dP["crop"], out.Kind+".crop", rec)
	out.Color = merge(mxColor, mpColor, DefaultDefaults().Color, dM["color"], dP["color"], out.Kind+".color", rec)
	out.Width = merge(mxW, mpW, 0, dM["width"], dP["width"], out.Kind+".width", rec)
	out.Height = merge(mxH, mpH, 0, dM["height"], dP["height"], out.Kind+".height", rec)
	return out
}

func kindOf(a, b *TrackMeta) string {
	if a != nil {
		return a.Kind
	}
	return b.Kind
}

// merge implements MXF > MP4 > default. Presence is taken from the explicit
// declared flags rather than zero-value checks, because zero is a valid
// declared value for fields such as rotation.
func merge[T comparable](mxf, mp4, def T, mxfSet, mp4Set bool, field string, rec *model.Recorder) Field[T] {
	f := Field[T]{Original: map[Source]T{}}
	if mxfSet {
		f.Original[SourceMXF] = mxf
	}
	if mp4Set {
		f.Original[SourceMP4] = mp4
	}
	f.Original[SourceDefault] = def
	switch {
	case mxfSet:
		f.Value, f.Winner = mxf, SourceMXF
	case mp4Set:
		f.Value, f.Winner = mp4, SourceMP4
	default:
		f.Value, f.Winner = def, SourceDefault
	}
	if mxfSet && mp4Set && mxf != mp4 {
		f.Conflict = true
		rec.Add(model.ErrorRecord{Code: model.ErrMetadataConflict, Severity: model.SevWarn,
			Track: field, Offset: -1, Message: "MXF/MP4 disagree; MXF value wins"})
	}
	return f
}
