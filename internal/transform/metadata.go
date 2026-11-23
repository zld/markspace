// Package transform applies deterministic metadata normalization: original
// declarations are preserved while a single normalized rotation, crop and
// color choice is emitted. Unresolvable conflicts are flagged for review
// rather than dropping the track.
package transform

import (
	"mediapipe/internal/fault"
	"mediapipe/internal/model"
)

// Normalized is the deterministic transform result for one track.
type Normalized struct {
	TrackID     string
	Rotation    int
	Crop        model.Crop
	Color       *model.ColorDecl
	ColorReview bool
}

// Normalize rewrites track metadata in place and returns the summary. The
// priority policy is fixed and order-independent:
//
//  1. Original declarations stay on the track untouched.
//  2. Rotation is normalized to the nearest quarter turn actually present.
//  3. Crop is normalized to non-negative margins clamped to frame bounds.
//  4. Color: identical declarations collapse; the first full-range nclx wins;
//     contradictory nclx triples are marked for review, never discarded.
func Normalize(tr *model.Track, errs *fault.Batch) Normalized {
	out := Normalized{TrackID: tr.ID, Rotation: normalizeRotation(tr.Rotation.Degrees)}
	normalizeCrop(tr, &out)
	normalizeColor(tr, &out, errs)
	return out
}

func normalizeRotation(deg int) int {
	switch deg {
	case 90, 180, 270:
		return deg
	default:
		return 0
	}
}

func normalizeCrop(tr *model.Track, out *Normalized) {
	crop := model.Crop{}
	if tr.Crop != nil {
		crop = *tr.Crop
	}
	w, h := tr.Width, tr.Height
	if w <= 0 {
		w = crop.Left + crop.Right
	}
	if h <= 0 {
		h = crop.Top + crop.Bottom
	}
	clamp := func(v, bound int64) int64 {
		if v < 0 {
			return 0
		}
		if bound > 0 && v > bound {
			return bound
		}
		return v
	}
	crop.Left = clamp(crop.Left, w)
	crop.Right = clamp(crop.Right, w)
	crop.Top = clamp(crop.Top, h)
	crop.Bottom = clamp(crop.Bottom, h)
	if w > 0 && crop.Left+crop.Right > w {
		crop.Right = w - crop.Left
	}
	if h > 0 && crop.Top+crop.Bottom > h {
		crop.Bottom = h - crop.Top
	}
	tr.Crop = &crop
	out.Crop = crop
}

func normalizeColor(tr *model.Track, out *Normalized, errs *fault.Batch) {
	decls := tr.Color.Declarations
	if len(decls) == 0 {
		tr.Color.Chosen = nil
		return
	}
	first := decls[0]
	identical := true
	for _, d := range decls[1:] {
		if !sameNCLX(d, first) {
			identical = false
			break
		}
	}
	chosen := first
	if !identical {
		// Deterministic tie-break: prefer a full-range nclx, else keep the
		// first declaration in file order. Either way the contradictory
		// source descriptions force a human-review flag; no track is dropped.
		for i := range decls {
			if decls[i].ColorType == "nclx" && decls[i].FullRange {
				chosen = decls[i]
				break
			}
		}
		tr.Color.Conflict = true
		out.ColorReview = true
		errs.Add(fault.New(fault.KindColorConflict,
			"conflicting color declarations; track retained and flagged",
			tr.SourceFile, first.SourceOff, true))
	}
	tr.Color.Review = out.ColorReview
	tr.Color.Chosen = &chosen
	out.Color = &chosen
}

func sameNCLX(a, b model.ColorDecl) bool {
	return a.ColorType == b.ColorType &&
		a.Primaries == b.Primaries &&
		a.Transfer == b.Transfer &&
		a.Matrix == b.Matrix &&
		a.FullRange == b.FullRange
}

// NormalizeAll applies the policy to every track and returns summaries keyed
// by track ID for the manifest.
func NormalizeAll(tl *model.Timeline, errs *fault.Batch) map[string]Normalized {
	out := map[string]Normalized{}
	for _, tr := range tl.Tracks {
		out[tr.ID] = Normalize(tr, errs)
	}
	return out
}
