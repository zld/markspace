package meta

import "fmt"

// Resolve merges track descriptions into a single Resolved description.
//
// Priority rules (documented and enforced, never last-write-wins):
//  1. Sources are ranked MXF > MP4 > default; within one container, the first
//     track declaring a field wins and later conflicting declarations are
//     recorded as "conflict" decisions with both values in the detail text.
//  2. Rotation: higher-priority non-zero degrees win; zero means "no
//     rotation" and never overrides an existing value.
//  3. Crop: a higher-priority complete window (width>0, height>0) wins.
//  4. Colour: each of primaries, transfer and matrix is resolved as its own
//     field with its own priority; full-range is boolean-OR of all sources.
//
// No input is silently dropped: every set, kept and conflict is in Decisions.
func Resolve(descs []*TrackDescription) (*Resolved, error) {
	if len(descs) == 0 {
		return nil, fmt.Errorf("meta.Resolve: no track descriptions supplied")
	}
	r := &Resolved{colPri: map[string]int{}}
	rank := func(d *TrackDescription) int {
		switch d.Container {
		case KindMXF:
			return PriorityMXF
		case KindMP4:
			return PriorityMP4
		default:
			return PriorityDefault
		}
	}
	for _, d := range descs {
		if d == nil {
			continue
		}
		p := rank(d)
		src := fmt.Sprintf("%s/track%d", d.Container, d.TrackID)
		if d.Width != 0 && r.Width == 0 {
			r.Width = d.Width
			r.Height = d.Height
			r.Decisions = append(r.Decisions, Decision{
				Field: "dimensions", Action: "set", Source: src,
				Detail: fmt.Sprintf("%dx%d", d.Width, d.Height),
			})
		}
		if d.Rotation != nil && d.Rotation.Degrees != 0 {
			rot := *d.Rotation
			rot.Source, rot.Priority = src, p
			switch {
			case r.Rotation.Degrees == 0:
				r.Rotation = rot
				r.decide("rotation", "set", src, fmt.Sprintf("%ddeg", rot.Degrees))
			case p > r.Rotation.Priority:
				r.decide("rotation", "conflict", src,
					fmt.Sprintf("replace %ddeg(%s) with %ddeg", r.Rotation.Degrees, r.Rotation.Source, rot.Degrees))
				r.Rotation = rot
			default:
				r.decide("rotation", "conflict", src,
					fmt.Sprintf("keep %ddeg(%s), reject %ddeg", r.Rotation.Degrees, r.Rotation.Source, rot.Degrees))
			}
		}
		if d.Crop != nil && d.Crop.Width > 0 && d.Crop.Height > 0 {
			crop := *d.Crop
			crop.Source, crop.Priority = src, p
			switch {
			case !r.HasCrop():
				r.Crop = crop
				r.decide("crop", "set", src, cropString(crop))
			case p > r.Crop.Priority:
				r.decide("crop", "conflict", src,
					fmt.Sprintf("replace %s(%s) with %s", cropString(r.Crop), r.Crop.Source, cropString(crop)))
				r.Crop = crop
			default:
				r.decide("crop", "conflict", src,
					fmt.Sprintf("keep %s(%s), reject %s", cropString(r.Crop), r.Crop.Source, cropString(crop)))
			}
		}
		if d.Colour != nil {
			mergeColour(r, d.Colour, src, p)
		}
	}
	return r, nil
}

func (r *Resolved) decide(field, action, source, detail string) {
	r.Decisions = append(r.Decisions, Decision{Field: field, Action: action, Source: source, Detail: detail})
}

func cropString(c Crop) string {
	return fmt.Sprintf("x=%d y=%d %dx%d", c.X, c.Y, c.Width, c.Height)
}

// mergeColour resolves each nclx field independently against its own stored
// priority so an MXF primaries value can override an MP4 one even when the
// transfer characteristic stays sourced from MP4.
func mergeColour(r *Resolved, in *Colour, src string, p int) {
	put := func(field string, cur *uint16, v uint16) {
		if v == 0 {
			return
		}
		oldP := r.colPri[field]
		switch {
		case *cur == 0:
			*cur = v
			r.colPri[field] = p
			r.decide(field, "set", src, fmt.Sprintf("value=%d", v))
		case *cur == v:
			r.decide(field, "kept", src, fmt.Sprintf("value=%d agrees", v))
		case p > oldP:
			r.decide(field, "conflict", src,
				fmt.Sprintf("replace %d(pri%d) with %d(pri%d)", *cur, oldP, v, p))
			*cur = v
			r.colPri[field] = p
		default:
			r.decide(field, "conflict", src,
				fmt.Sprintf("keep %d(pri%d), reject %d(pri%d)", *cur, oldP, v, p))
		}
	}
	put("colour.primaries", &r.Colour.Primaries, in.Primaries)
	put("colour.transfer", &r.Colour.Transfer, in.Transfer)
	put("colour.matrix", &r.Colour.Matrix, in.Matrix)
	if r.HasColour() {
		r.Colour.Source, r.Colour.Priority = src, p
	}
	if in.FullRange {
		r.Colour.FullRange = true
		r.decide("colour.fullrange", "set", src, "OR-merged across sources")
	}
}
