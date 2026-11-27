package maptrack

import (
	"mediapack/internal/demux"
	"mediapack/internal/model"
)

// candidate pairs a container format with one track's metadata, ordered for
// the MXF > MP4 precedence rule.
type candidate struct {
	source model.MetaSource
	meta   model.TrackMeta
}

// orderedCandidates collects every container carrying the same track id and
// sorts them by adjudication precedence (MXF first, then MP4). Determinism
// for equal formats comes from the container slice order.
func orderedCandidates(containers []*demux.Container, trackID uint32) []candidate {
	var mxf, mp4 []candidate
	for _, c := range containers {
		if !c.HasTrack(trackID) {
			continue
		}
		m := c.TrackMeta(trackID)
		switch c.Format {
		case "mxf":
			mxf = append(mxf, candidate{model.SourceMXF, m})
		case "mp4":
			mp4 = append(mp4, candidate{model.SourceMP4, m})
		}
	}
	return append(mxf, mp4...)
}

// adjudicate resolves rotation, crop and color for one track. Every candidate
// value is retained in the Decision along with the adopted one, so the report
// can show originals versus the winner.
func adjudicate(containers []*demux.Container, trackID uint32,
	def Defaults,
) (model.TrackMeta, []Decision) {
	cands := orderedCandidates(containers, trackID)
	decisions := make([]Decision, 0, 3)
	out := model.TrackMeta{TrackID: trackID}

	rot := adjudicateField(cands, "rotation",
		func(c candidate) (any, string, bool) {
			if c.meta.Rotation.Source != "" {
				return c.meta.Rotation.Value, c.meta.Rotation.Raw, true
			}
			return nil, "", false
		},
		def.Rotation)
	out.Rotation = asRotation(rot.Adopted)
	decisions = append(decisions, rot)

	crop := adjudicateField(cands, "crop",
		func(c candidate) (any, string, bool) {
			if c.meta.Crop.Source != "" {
				return c.meta.Crop.Value, c.meta.Crop.Raw, true
			}
			return nil, "", false
		},
		def.Crop)
	out.Crop = asCrop(crop.Adopted)
	decisions = append(decisions, crop)

	color := adjudicateField(cands, "color",
		func(c candidate) (any, string, bool) {
			if c.meta.Color.Source != "" {
				return c.meta.Color.Value, c.meta.Color.Raw, true
			}
			return nil, "", false
		},
		def.Color)
	out.Color = asColor(color.Adopted)
	decisions = append(decisions, color)

	// Identity fields belong to the bound source, not the cross-container
	// adjudication; only presentation metadata (rotation/crop/color) is
	// resolved by precedence. They are filled by the caller.
	return out, decisions
}

// adjudicateField is the generic precedence engine. extractor returns ok=false
// when a candidate does not carry the field. The fallback always qualifies.
func adjudicateField(cands []candidate, field string,
	extractor func(candidate) (any, string, bool), fallback any,
) Decision {
	d := Decision{Field: field}
	for _, c := range cands {
		v, raw, ok := extractor(c)
		if !ok {
			continue
		}
		f := model.Field[any]{Value: v, Source: c.source, Raw: raw}
		d.Candidates = append(d.Candidates, f)
	}
	d.Candidates = append(d.Candidates, model.Field[any]{
		Value: fallback, Source: model.SourceDefault,
		Raw: "default",
	})
	// Candidates are already MXF-before-MP4. Adopt the first container value;
	// if none carries the field, the trailing default candidate is used.
	d.Adopted = d.Candidates[len(d.Candidates)-1]
	for _, f := range d.Candidates {
		if f.Source != model.SourceDefault {
			d.Adopted = f
			break
		}
	}
	return d
}

func asRotation(f model.Field[any]) model.Field[model.Rotation] {
	return model.Field[model.Rotation]{
		Value: f.Value.(model.Rotation), Source: f.Source, Raw: f.Raw,
	}
}

func asCrop(f model.Field[any]) model.Field[model.Crop] {
	return model.Field[model.Crop]{
		Value: f.Value.(model.Crop), Source: f.Source, Raw: f.Raw,
	}
}

func asColor(f model.Field[any]) model.Field[model.Color] {
	return model.Field[model.Color]{
		Value: f.Value.(model.Color), Source: f.Source, Raw: f.Raw,
	}
}
