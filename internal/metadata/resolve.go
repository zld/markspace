package metadata

import (
	"encoding/json"
	"sort"

	"media-audit/internal/media"
)

const SourceDefault = "DEFAULT"

type Candidate struct {
	Source  string         `json:"source"`
	TrackID uint32         `json:"track_id"`
	Meta    media.Metadata `json:"metadata"`
}

type Party struct {
	Source  string `json:"source"`
	TrackID uint32 `json:"track_id"`
	Value   string `json:"value"`
}

type Conflict struct {
	Field  string  `json:"field"`
	Winner Party   `json:"winner"`
	Losers []Party `json:"losers"`
}

type Result struct {
	Metadata  media.Metadata `json:"metadata"`
	Source    string         `json:"source"`
	Conflicts []Conflict     `json:"conflicts"`
}

func Default() media.Metadata {
	return media.Metadata{
		HasRotation:     true,
		RotationDegrees: 0,
		Color:           &media.Color{Primaries: 1, Transfer: 1, Matrix: 1, PrimariesName: "BT.709", TransferName: "BT.709", MatrixName: "BT.709"},
	}
}

func rank(source string) int {
	switch source {
	case string(media.FormatMXF):
		return 0
	case string(media.FormatMP4):
		return 1
	default:
		return 2
	}
}

func valueString(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

type declared struct {
	party Party
	value any
}

func resolveField(name string, candidates []Candidate, def any, declaredFn func(Candidate) (any, bool)) (any, Party, []Conflict) {
	values := make([]declared, 0, len(candidates)+1)
	for _, c := range candidates {
		if v, ok := declaredFn(c); ok {
			values = append(values, declared{
				value: v,
				party: Party{Source: c.Source, TrackID: c.TrackID, Value: valueString(v)},
			})
		}
	}
	sort.SliceStable(values, func(i, j int) bool {
		if rank(values[i].party.Source) != rank(values[j].party.Source) {
			return rank(values[i].party.Source) < rank(values[j].party.Source)
		}
		return values[i].party.TrackID < values[j].party.TrackID
	})
	if def != nil {
		values = append(values, declared{value: def, party: Party{Source: SourceDefault, Value: valueString(def)}})
	}
	if len(values) == 0 {
		return nil, Party{}, nil
	}
	winner := values[0]
	winnerKey := valueString(winner.value)
	var losers []Party
	var conflict *Conflict
	for _, v := range values[1:] {
		if valueString(v.value) != winnerKey {
			if conflict == nil {
				conflict = &Conflict{Field: name, Winner: winner.party}
			}
			conflict.Losers = append(conflict.Losers, v.party)
			losers = append(losers, v.party)
		}
	}
	if conflict != nil {
		return winner.value, winner.party, []Conflict{*conflict}
	}
	return winner.value, winner.party, nil
}

func normalizeColor(c *media.Color) *media.Color {
	if c == nil {
		return nil
	}
	out := *c
	names := map[uint16]string{1: "BT.709", 9: "BT.2020", 6: "BT.601"}
	if out.PrimariesName == "" {
		out.PrimariesName = names[out.Primaries]
	}
	if out.TransferName == "" {
		out.TransferName = names[out.Transfer]
	}
	if out.MatrixName == "" {
		out.MatrixName = names[out.Matrix]
	}
	return &out
}

// Resolve applies MXF, then MP4, then built-in defaults. Every lower-priority
// different declaration is retained as a conflict party.
func Resolve(candidates []Candidate) Result {
	out := Result{Metadata: Default(), Source: SourceDefault}

	if v, p, conflicts := resolveField("rotation", candidates, int32(0), func(c Candidate) (any, bool) {
		return c.Meta.RotationDegrees, c.Meta.HasRotation
	}); v != nil {
		out.Metadata.RotationDegrees = v.(int32)
		out.Metadata.HasRotation = true
		if p.Source != "" {
			out.Source = p.Source
		}
		out.Conflicts = append(out.Conflicts, conflicts...)
	}

	if v, _, conflicts := resolveField("crop", candidates, (*media.Crop)(nil), func(c Candidate) (any, bool) {
		return c.Meta.Crop, c.Meta.Crop != nil
	}); v != nil {
		out.Metadata.Crop = v.(*media.Crop)
		out.Conflicts = append(out.Conflicts, conflicts...)
	}

	if v, _, conflicts := resolveField("color", candidates, out.Metadata.Color, func(c Candidate) (any, bool) {
		return normalizeColor(c.Meta.Color), c.Meta.Color != nil
	}); v != nil {
		out.Metadata.Color = v.(*media.Color)
		out.Conflicts = append(out.Conflicts, conflicts...)
	}

	if v, _, conflicts := resolveField("metadata_version", candidates, uint32(1), func(c Candidate) (any, bool) {
		return c.Meta.MetadataVersion, c.Meta.MetadataVersion != 0
	}); v != nil {
		out.Metadata.MetadataVersion = v.(uint32)
		out.Conflicts = append(out.Conflicts, conflicts...)
	}
	return out
}
