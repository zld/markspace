package meta

import "fmt"

// Resolve merges one track's MXF and MP4 views into a single TrackMeta.
//
// Precedence on every conflicting field is fixed: MXF wins over MP4, and an
// explicit container value wins over the documented default. Each disagreement
// is recorded as a Conflict naming both raw values and the winning source, so
// downstream reports can attribute every arbitration. Inputs are never
// mutated; conflicts live on the returned TrackMeta only.
func Resolve(mxf, mp4 *TrackMeta) *TrackMeta {
	base := pickBase(mxf, mp4)
	out := &TrackMeta{
		TrackNo: base.TrackNo,
		Kind:    base.Kind,
		Clock:   base.Clock,
		Width:   base.Width,
		Height:  base.Height,
	}

	resolveRotation(out, mxf, mp4)
	resolveCrop(out, mxf, mp4)
	resolveColor(out, mxf, mp4)
	return out
}

// ResolveAll pairs MXF and MP4 tracks by track number. Tracks present in only
// one container pass through unchanged (their missing fields take defaults).
func ResolveAll(mxfRes, mp4Res *Result) *Result {
	out := &Result{}
	byNo := map[int]*TrackMeta{}
	var order []int
	for _, t := range mxfRes.Tracks {
		byNo[t.TrackNo] = t
		order = append(order, t.TrackNo)
	}
	for _, t := range mp4Res.Tracks {
		if _, ok := byNo[t.TrackNo]; !ok {
			order = append(order, t.TrackNo)
		}
	}
	for _, no := range order {
		out.Tracks = append(out.Tracks, Resolve(byNo[no], findTrack(mp4Res, no)))
	}
	out.Errors = append(out.Errors, mxfRes.Errors...)
	out.Errors = append(out.Errors, mp4Res.Errors...)
	return out
}

func findTrack(r *Result, no int) *TrackMeta {
	for _, t := range r.Tracks {
		if t.TrackNo == no {
			return t
		}
	}
	return nil
}

func pickBase(mxf, mp4 *TrackMeta) *TrackMeta {
	if mxf != nil {
		return mxf
	}
	return mp4
}

// resolveRotation treats a zero rotation as "not authored" so a real rotation
// from either container is never shadowed by a zero default.
func resolveRotation(out, mxf, mp4 *TrackMeta) {
	const field = "rotation"
	switch {
	case mxf != nil && mxf.Rotation != 0:
		out.Rotation, out.RotSrc = mxf.Rotation, SourceMXF
		if mp4 != nil && mp4.Rotation != 0 && mp4.Rotation != mxf.Rotation {
			out.Conflicts = append(out.Conflicts, Conflict{
				Field: field, Chosen: SourceMXF,
				Values: map[Source]string{
					SourceMXF: fmt.Sprintf("%d", mxf.Rotation),
					SourceMP4: fmt.Sprintf("%d", mp4.Rotation),
				},
			})
		}
	case mp4 != nil && mp4.Rotation != 0:
		out.Rotation, out.RotSrc = mp4.Rotation, SourceMP4
	default:
		out.Rotation, out.RotSrc = 0, SourceDefault
	}
}

func resolveCrop(out, mxf, mp4 *TrackMeta) {
	mxfHas := mxf != nil && mxf.Crop.Width > 0 && mxf.Crop.Height > 0
	mp4Has := mp4 != nil && mp4.Crop.Width > 0 && mp4.Crop.Height > 0
	switch {
	case mxfHas:
		out.Crop = mxf.Crop
		out.Crop.Source = SourceMXF
		if mp4Has && !sameCrop(mxf.Crop, mp4.Crop) {
			out.Conflicts = append(out.Conflicts, Conflict{
				Field: "crop", Chosen: SourceMXF,
				Values: map[Source]string{
					SourceMXF: cropText(mxf.Crop),
					SourceMP4: cropText(mp4.Crop),
				},
			})
		}
	case mp4Has:
		out.Crop = mp4.Crop
		out.Crop.Source = SourceMP4
	default:
		out.Crop = FullFrame(out.Width, out.Height)
	}
}

func resolveColor(out, mxf, mp4 *TrackMeta) {
	mxfHas := mxf != nil && mxf.Color.Primaries != ""
	mp4Has := mp4 != nil && mp4.Color.Primaries != ""
	switch {
	case mxfHas:
		out.Color = mxf.Color
		out.Color.Source = SourceMXF
		if mp4Has && !sameColor(mxf.Color, mp4.Color) {
			out.Conflicts = append(out.Conflicts, Conflict{
				Field: "color", Chosen: SourceMXF,
				Values: map[Source]string{
					SourceMXF: colorText(mxf.Color),
					SourceMP4: colorText(mp4.Color),
				},
			})
		}
	case mp4Has:
		out.Color = mp4.Color
		out.Color.Source = SourceMP4
	default:
		out.Color = Color{Primaries: DefaultColorPrimaries, Transfer: DefaultColorTransfer,
			Matrix: DefaultColorMatrix, Source: SourceDefault}
	}
}

func sameCrop(a, b Crop) bool {
	return a.X == b.X && a.Y == b.Y && a.Width == b.Width && a.Height == b.Height
}

func sameColor(a, b Color) bool {
	return a.Primaries == b.Primaries && a.Transfer == b.Transfer && a.Matrix == b.Matrix
}

func cropText(c Crop) string {
	return fmt.Sprintf("x=%d,y=%d,w=%d,h=%d", c.X, c.Y, c.Width, c.Height)
}

func colorText(c Color) string {
	return fmt.Sprintf("%s/%s/%s", c.Primaries, c.Transfer, c.Matrix)
}
