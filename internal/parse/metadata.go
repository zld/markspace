package parse

import (
	"fmt"
	"sort"
	"strings"
)

// ResolveMetadata merges descriptors from all sources field by field.
//
// A field present in a single source is adopted. Equal values are adopted
// automatically. Differing values require selectors[field] to name a source;
// otherwise the field is reported as a conflict and left unset. selectors
// entries for unknown fields are ignored.
func ResolveMetadata(sources []MetaSource, selectors map[string]string) (Meta, []FieldChoice, []Conflict) {
	var out Meta
	choices := make([]FieldChoice, 0, len(metaFields))
	conflicts := make([]Conflict, 0)
	ordered := append([]MetaSource(nil), sources...)
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].Name < ordered[j].Name })

	for _, field := range metaFields {
		cands := make([]Candidate, 0, len(ordered))
		seen := make(map[string]struct{})
		for _, src := range ordered {
			if v, ok := metaValue(src.Meta, field); ok {
				cands = append(cands, Candidate{Source: src.Name, Value: v})
				seen[v] = struct{}{}
			}
		}
		switch {
		case len(cands) == 0:
			// Absent everywhere: nothing to choose and no conflict.
		case len(cands) == 1:
			choices = append(choices, FieldChoice{Field: field, Source: cands[0].Source, Value: cands[0].Value, Auto: true})
			setMetaField(&out, field, sources, cands[0])
		case len(seen) == 1:
			choices = append(choices, FieldChoice{Field: field, Source: cands[0].Source, Value: cands[0].Value, Auto: true})
			setMetaField(&out, field, sources, cands[0])
		default:
			sel, hasSel := selectors[field]
			if hasSel {
				chosen, ok := findCandidate(cands, sel)
				if ok {
					choices = append(choices, FieldChoice{
						Field: field, Source: sel, Value: chosen.Value, HadConflict: true,
					})
					setMetaField(&out, field, sources, chosen)
					continue
				}
			}
			reason := "source not specified"
			if hasSel {
				reason = "selected source has no value"
			}
			conflicts = append(conflicts, Conflict{Field: field, Candidates: cands, Reason: reason})
			choices = append(choices, FieldChoice{Field: field, HadConflict: true})
		}
	}
	return out, choices, conflicts
}

func findCandidate(cands []Candidate, source string) (Candidate, bool) {
	for _, c := range cands {
		if c.Source == source {
			return c, true
		}
	}
	return Candidate{}, false
}

func setMetaField(out *Meta, field string, sources []MetaSource, chosen Candidate) {
	for _, src := range sources {
		if src.Name != chosen.Source || src.Meta == nil {
			continue
		}
		switch field {
		case FieldRotation:
			if src.Meta.Rotation != nil {
				v := *src.Meta.Rotation
				out.Rotation = &v
			}
		case FieldCrop:
			if src.Meta.Crop != nil {
				v := *src.Meta.Crop
				out.Crop = &v
			}
		case FieldColor:
			if src.Meta.Color != nil {
				v := *src.Meta.Color
				out.Color = &v
			}
		}
		return
	}
}

// CloneMeta exposes a defensive copy for callers that cache metadata.
func CloneMeta(m *Meta) *Meta { return m.clone() }

// DescribeChoices renders choices for the summary in stable order.
func DescribeChoices(choices []FieldChoice) string {
	parts := make([]string, 0, len(choices))
	for _, c := range choices {
		if c.Source == "" {
			parts = append(parts, fmt.Sprintf("%s=unresolved", c.Field))
			continue
		}
		parts = append(parts, fmt.Sprintf("%s=%s(%s)", c.Field, c.Source, c.Value))
	}
	return strings.Join(parts, ";")
}
