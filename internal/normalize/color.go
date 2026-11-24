package normalize

import (
	"sort"

	"mediarc/internal/model"
)

// ColorReport retains every source value and records field conflicts.
type ColorReport struct {
	Sources     []model.SourcedColor  `json:"sources"`
	Conflicts   []model.ColorConflict `json:"conflicts"`
	Resolved    model.ColorMetadata   `json:"resolved"`
	HasConflict bool                  `json:"has_conflict"`
}

// ReconcileColor compares named color sources without mutating any source.
// Equal/empty values are not conflicts. Conflicting fields are omitted from
// Resolved so downstream code cannot accidentally treat one source as winner.
func ReconcileColor(sources []model.SourcedColor, faults *model.Collector) ColorReport {
	rep := ColorReport{Sources: append([]model.SourcedColor(nil), sources...)}
	fields := []struct {
		name string
		get  func(model.ColorMetadata) string
		set  func(*model.ColorMetadata, string)
	}{
		{"primaries", func(c model.ColorMetadata) string { return c.Primaries }, func(c *model.ColorMetadata, v string) { c.Primaries = v }},
		{"transfer", func(c model.ColorMetadata) string { return c.Transfer }, func(c *model.ColorMetadata, v string) { c.Transfer = v }},
		{"matrix", func(c model.ColorMetadata) string { return c.Matrix }, func(c *model.ColorMetadata, v string) { c.Matrix = v }},
		{"range", func(c model.ColorMetadata) string { return c.Range }, func(c *model.ColorMetadata, v string) { c.Range = v }},
	}
	for _, field := range fields {
		values := make(map[string]string)
		distinct := make(map[string]string)
		for _, s := range sources {
			v := field.get(s.Color)
			if v == "" || v == "unknown" {
				continue
			}
			values[s.Source] = v
			distinct[v] = s.Source
		}
		if len(distinct) <= 1 {
			for v := range distinct {
				field.set(&rep.Resolved, v)
			}
			continue
		}
		keys := make([]string, 0, len(values))
		for src := range values {
			keys = append(keys, src)
		}
		sort.Strings(keys)
		orderedValues := make(map[string]string, len(values))
		for _, k := range keys {
			orderedValues[k] = values[k]
		}
		conflict := model.ColorConflict{Field: field.name, Values: orderedValues}
		rep.Conflicts = append(rep.Conflicts, conflict)
		rep.HasConflict = true
		if faults != nil {
			faults.Add(model.Fault{Kind: model.FaultColorConflict, Source: field.name,
				Expected: "consistent " + field.name, Actual: joinValues(orderedValues),
				Message: "color metadata conflict; source values preserved", Recoverable: true})
		}
	}
	return rep
}

func joinValues(m map[string]string) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := ""
	for i, k := range keys {
		if i > 0 {
			out += ","
		}
		out += k + "=" + m[k]
	}
	return out
}
