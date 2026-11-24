package container

import (
	"github.com/local/medialab/internal/model"
)

// ArbitratedMeta is the single metadata view the pipeline consumes after
// MXF/MP4/default arbitration. Conflicts are preserved rather than hidden.
type ArbitratedMeta struct {
	Color          ColorDesc
	Rotation       int
	ConflictCount  int
	Conflicts      []Conflict
	Tracks         []TrackDesc
	VideoTimeScale int64 // winning video timescale in Hz
}

// Conflict records one raw disagreement and the winning source.
type Conflict struct {
	Field    string
	MXFValue string
	MP4Value string
	Winner   string // "mxf" | "mp4" | "default"
}

// Default values used only when neither container supplies a field.
var (
	defaultColor    = ColorDesc{Primaries: 1, Transfer: 1, Matrix: 1, Source: "default"}
	defaultRotation = 0
)

// Arbitrate merges an MXF and an MP4 metadata view. Priority for every
// disputed field is fixed: MXF wins over MP4, MP4 wins over the built-in
// default. Each disagreement increments ConflictCount and is listed in
// Conflicts so the summary stays auditable.
func Arbitrate(mxf *MXFMeta, mp4 *MP4Meta, book *model.ErrorBook) ArbitratedMeta {
	out := ArbitratedMeta{}

	// Colour primaries.
	var mxfColor, mp4Color *ColorDesc
	if mxf != nil {
		mxfColor = mxf.FileMeta.Color
	}
	if mp4 != nil {
		mp4Color = mp4.FileMeta.Color
	}
	switch {
	case mxfColor != nil && mp4Color != nil:
		out.Color = *mxfColor
		out.Color.Source = "mxf"
		if mxfColor.Primaries != mp4Color.Primaries ||
			mxfColor.Transfer != mp4Color.Transfer || mxfColor.Matrix != mp4Color.Matrix {
			c := Conflict{Field: "color_primaries",
				MXFValue: colorString(*mxfColor), MP4Value: colorString(*mp4Color), Winner: "mxf"}
			out.Conflicts = append(out.Conflicts, c)
			book.Add(model.ErrorRecord{Code: model.CodeConflict,
				Detail: "MXF and MP4 colour descriptions differ; MXF wins"})
		}
	case mxfColor != nil:
		out.Color = *mxfColor
	case mp4Color != nil:
		out.Color = *mp4Color
	default:
		out.Color = defaultColor
		c := Conflict{Field: "color_primaries", Winner: "default",
			MXFValue: "absent", MP4Value: "absent"}
		out.Conflicts = append(out.Conflicts, c)
	}

	// Rotation.
	mxfRot, mp4Rot := 0, 0
	hasMXFRot, hasMP4Rot := false, false
	if mxf != nil && len(mxf.Tracks) > 0 {
		mxfRot, hasMXFRot = mxf.Tracks[0].Rotation, true
	}
	if mp4 != nil {
		mp4Rot, hasMP4Rot = mp4.FileMeta.Rotation, true
	}
	switch {
	case hasMXFRot && hasMP4Rot:
		out.Rotation = mxfRot
		if mxfRot != mp4Rot {
			out.Conflicts = append(out.Conflicts, Conflict{
				Field: "rotation", MXFValue: itoa(mxfRot), MP4Value: itoa(mp4Rot), Winner: "mxf"})
			book.Add(model.ErrorRecord{Code: model.CodeConflict,
				Detail: "MXF and MP4 rotation differ; MXF wins"})
		}
	case hasMXFRot:
		out.Rotation = mxfRot
	case hasMP4Rot:
		out.Rotation = mp4Rot
	default:
		out.Rotation = defaultRotation
	}

	// Tracks: prefer MXF edit-rate track when present, else MP4 video.
	if mxf != nil && len(mxf.Tracks) > 0 {
		out.Tracks = append(out.Tracks, mxf.Tracks...)
		out.VideoTimeScale = mxf.Tracks[0].TimescaleHz
	}
	if mp4 != nil {
		out.Tracks = append(out.Tracks, mp4.Tracks...)
		if out.VideoTimeScale == 0 && len(mp4.Tracks) > 0 {
			out.VideoTimeScale = mp4.Tracks[0].TimescaleHz
		}
	}
	out.ConflictCount = len(out.Conflicts)
	return out
}

func colorString(c ColorDesc) string {
	return "nclx(" + itoa(int(c.Primaries)) + "," + itoa(int(c.Transfer)) +
		"," + itoa(int(c.Matrix)) + ")"
}

// itoa avoids pulling strconv into a hot comparison path and keeps the
// conflict text format stable.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}
