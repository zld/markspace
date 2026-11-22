package media

import (
	"fmt"
	"sort"

	"mediadist/timeline"
)

// ColorMeta carries the signaling-relevant color fields. Empty strings
// mean "not signaled" and are counted separately from conflicts.
type ColorMeta struct {
	Source       string
	Primaries    string
	Transfer     string
	MatrixCoeffs string
	Range        string
}

// ColorStats separates missing signaling from conflicting signaling.
type ColorStats struct {
	Missing   int
	Conflicts int
	Sources   int
}

// ColorDecision records, per field, the chosen output value and every
// source value that was observed, including the losing ones.
type ColorDecision struct {
	Field    string
	Value    string
	Source   string
	Observed map[string]string
}

// ColorReport is the deterministic merge result.
type ColorReport struct {
	Decision ColorMeta
	PerField []ColorDecision
	Stats    ColorStats
}

// ColorMerger resolves color metadata across sources with a fixed
// priority order. Conflicting values are kept in the report; the output
// always comes from the highest-priority source that signals the field.
type ColorMerger struct {
	priority map[string]int
}

// NewColorMerger takes sources ordered from highest to lowest priority.
func NewColorMerger(sources []string) *ColorMerger {
	m := &ColorMerger{priority: map[string]int{}}
	for i, s := range sources {
		m.priority[s] = i
	}
	return m
}

var colorFields = []struct {
	name string
	get  func(ColorMeta) string
}{
	{"primaries", func(c ColorMeta) string { return c.Primaries }},
	{"transfer", func(c ColorMeta) string { return c.Transfer }},
	{"matrix_coeffs", func(c ColorMeta) string { return c.MatrixCoeffs }},
	{"range", func(c ColorMeta) string { return c.Range }},
}

// Merge combines one ColorMeta per source. Unknown sources are rejected
// so the priority table cannot be bypassed accidentally.
func (m *ColorMerger) Merge(metas []ColorMeta) (*ColorReport, error) {
	rep := &ColorReport{}
	seenSources := map[string]bool{}
	for _, meta := range metas {
		if _, ok := m.priority[meta.Source]; !ok {
			return nil, timeline.NewError("UNKNOWN_METADATA_SOURCE",
				"color metadata source has no configured priority",
				map[string]any{"source": meta.Source})
		}
		seenSources[meta.Source] = true
	}
	rep.Stats.Sources = len(seenSources)

	for _, f := range colorFields {
		observed := map[string]string{}
		distinct := map[string]string{}
		var missing int
		for _, meta := range metas {
			v := f.get(meta)
			if v == "" {
				missing++
				continue
			}
			observed[meta.Source] = v
			distinct[v] = meta.Source
		}
		rep.Stats.Missing += missing
		if len(distinct) > 1 {
			rep.Stats.Conflicts++
		}
		decision := ColorDecision{Field: f.name, Observed: observed}
		if winner, val, ok := m.pickWinner(observed); ok {
			decision.Source, decision.Value = winner, val
		}
		rep.PerField = append(rep.PerField, decision)
		applyColorField(&rep.Decision, f.name, decision.Value)
	}
	return rep, nil
}

// pickWinner returns the value from the highest-priority source that
// actually signals the field.
func (m *ColorMerger) pickWinner(observed map[string]string) (string, string, bool) {
	type sv struct {
		source, value string
		rank          int
	}
	candidates := make([]sv, 0, len(observed))
	for source, value := range observed {
		candidates = append(candidates, sv{source, value, m.priority[source]})
	}
	if len(candidates) == 0 {
		return "", "", false
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].rank != candidates[j].rank {
			return candidates[i].rank < candidates[j].rank
		}
		return candidates[i].source < candidates[j].source
	})
	return candidates[0].source, candidates[0].value, true
}

func applyColorField(c *ColorMeta, field, value string) {
	switch field {
	case "primaries":
		c.Primaries = value
	case "transfer":
		c.Transfer = value
	case "matrix_coeffs":
		c.MatrixCoeffs = value
	case "range":
		c.Range = value
	default:
		panic(fmt.Sprintf("unknown color field %q", field))
	}
}
