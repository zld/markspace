package metadata

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"mediaprep/internal/model"
	"mediaprep/internal/source"
)

// Field is the winning provenance plus every conflicting assertion.
type Field struct {
	Name         string            `json:"name"`
	Value        string            `json:"value"`
	Scope        source.Scope      `json:"scope"`
	Offset       int64             `json:"offset"`
	Conflict     bool              `json:"conflict"`
	Alternatives []source.RawField `json:"alternatives"`
}

type Crop struct {
	X      int `json:"x"`
	Y      int `json:"y"`
	Width  int `json:"width"`
	Height int `json:"height"`
}

type Resolved struct {
	Track     string            `json:"track"`
	Width     int               `json:"width"`
	Height    int               `json:"height"`
	Fields    map[string]Field  `json:"fields"`
	Rotation  int               `json:"rotation"`
	Crop      *Crop             `json:"crop,omitempty"`
	Color     string            `json:"color,omitempty"`
	Conflicts []model.Exception `json:"conflicts,omitempty"`
}

// ResolveTrack applies sample/MXF descriptor > track > container precedence.
// Distinct values for one field produce a conflict exception, but the complete
// provenance is retained. Invalid winning geometry is returned as a locatable
// model.Error rather than being silently replaced by a lower-priority value.
func ResolveTrack(track *source.Track) (*Resolved, error) {
	out := &Resolved{
		Track:  track.Label,
		Width:  track.Width,
		Height: track.Height,
		Fields: map[string]Field{},
	}
	byName := map[string][]source.RawField{}
	for _, rf := range track.RawFields {
		byName[rf.Name] = append(byName[rf.Name], rf)
	}

	names := make([]string, 0, len(byName))
	for name := range byName {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		assertions := append([]source.RawField{}, byName[name]...)
		sort.SliceStable(assertions, func(i, j int) bool {
			if assertions[i].Scope.Rank() != assertions[j].Scope.Rank() {
				return assertions[i].Scope.Rank() < assertions[j].Scope.Rank()
			}
			return assertions[i].Offset < assertions[j].Offset
		})
		winner := assertions[0]
		conflict := false
		for _, a := range assertions[1:] {
			if a.Value != winner.Value {
				conflict = true
				break
			}
		}
		f := Field{
			Name: name, Value: winner.Value, Scope: winner.Scope, Offset: winner.Offset,
			Conflict: conflict, Alternatives: assertions,
		}
		out.Fields[name] = f
		if conflict {
			out.Conflicts = append(out.Conflicts, model.Exception{
				Kind: "metadata-conflict", Phase: model.PhaseMetadata, Track: track.Label,
				Offset: winner.Offset, HasOffset: true,
				Message: fmt.Sprintf("field %s has %d distinct assertions; kept %q from %s",
					name, distinctValues(assertions), winner.Value, winner.Scope),
				Resolution: fmt.Sprintf("retained highest-priority value %q (%s); all provenance stored",
					winner.Value, winner.Scope),
			})
		}
	}

	if f, ok := out.Fields["rotation"]; ok {
		r, err := strconv.Atoi(strings.TrimSpace(f.Value))
		if err != nil || (r != 0 && r != 90 && r != 180 && r != 270) {
			return nil, model.NewError(model.CodeInvalidMetadata, model.PhaseMetadata, track.Label, f.Offset,
				"rotation %q is not one of 0/90/180/270", f.Value)
		}
		out.Rotation = r
	}
	if f, ok := out.Fields["color"]; ok {
		out.Color = f.Value
	}
	if f, ok := out.Fields["crop"]; ok {
		crop, err := parseCrop(f.Value)
		if err != nil {
			return nil, model.NewError(model.CodeInvalidMetadata, model.PhaseMetadata, track.Label, f.Offset,
				"crop %q is invalid: %v", f.Value, err)
		}
		if out.Width <= 0 || out.Height <= 0 {
			return nil, model.NewError(model.CodeInvalidMetadata, model.PhaseMetadata, track.Label, f.Offset,
				"cannot validate crop without positive frame dimensions")
		}
		if crop.X < 0 || crop.Y < 0 || crop.Width <= 0 || crop.Height <= 0 ||
			crop.X+crop.Width > out.Width || crop.Y+crop.Height > out.Height {
			return nil, model.NewError(model.CodeInvalidMetadata, model.PhaseMetadata, track.Label, f.Offset,
				"crop %s lies outside %dx%d frame", f.Value, out.Width, out.Height)
		}
		out.Crop = crop
	}
	return out, nil
}

func parseCrop(s string) (*Crop, error) {
	parts := strings.Split(s, ",")
	if len(parts) != 4 {
		return nil, fmt.Errorf("want x,y,width,height")
	}
	nums := make([]int, 4)
	for i, p := range parts {
		n, err := strconv.Atoi(strings.TrimSpace(p))
		if err != nil {
			return nil, err
		}
		nums[i] = n
	}
	return &Crop{X: nums[0], Y: nums[1], Width: nums[2], Height: nums[3]}, nil
}

func distinctValues(fs []source.RawField) int {
	seen := map[string]bool{}
	for _, f := range fs {
		seen[f.Value] = true
	}
	return len(seen)
}
