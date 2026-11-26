// Package mapping holds the explicit version-and-track mapping table that
// decides which parsed source tracks feed which canonical timeline tracks.
package mapping

import (
	"fmt"
	"sort"

	"mediamerge/internal/clock"
	"mediamerge/internal/fault"
	"mediamerge/internal/track"
)

// Rule maps one explicit source (track id + version) onto a target track.
type Rule struct {
	Source        string      // source track identifier, must match track.Track.ID
	SourceVersion string      // explicit version label, must match track.Track.Version
	TargetTrack   string      // canonical timeline track id
	Language      string      // language tag asserted for the target
	Offset        clock.Ticks // timeline offset added to every cue
	Enabled       bool        // disabled rules are kept for audit but never match
}

func (r Rule) key() string { return r.Source + "@" + r.SourceVersion }

// Table is the validated mapping table.
type Table struct {
	rules map[string]Rule
	order []string
}

// NewTable validates and indexes the supplied rules.
func NewTable(rules []Rule) (*Table, error) {
	t := &Table{rules: make(map[string]Rule, len(rules))}
	for _, r := range rules {
		if r.Source == "" || r.SourceVersion == "" || r.TargetTrack == "" {
			return nil, fault.New(fault.StageMap, fault.KindState, r.Source,
				"mapping rule requires source, version and target track")
		}
		k := r.key()
		if _, dup := t.rules[k]; dup {
			return nil, fault.New(fault.StageMap, fault.KindState, k,
				"duplicate mapping rule for source/version pair")
		}
		t.rules[k] = r
		t.order = append(t.order, k)
	}
	sort.Strings(t.order)
	return t, nil
}

// Lookup returns the rule for a source/version pair. A miss is a missing-source
// fault so callers can turn it into a gap record.
func (t *Table) Lookup(source, version string) (Rule, error) {
	if r, ok := t.rules[source+"@"+version]; ok {
		if !r.Enabled {
			return Rule{}, fault.New(fault.StageMap, fault.KindMissing, source+"@"+version,
				"mapping rule is disabled")
		}
		return r, nil
	}
	return Rule{}, fault.New(fault.StageMap, fault.KindMissing, source+"@"+version,
		"no mapping rule for source/version pair")
}

// Rules returns the indexed rules in stable (source/version) order.
func (t *Table) Rules() []Rule {
	out := make([]Rule, 0, len(t.order))
	for _, k := range t.order {
		out = append(out, t.rules[k])
	}
	return out
}

// Apply maps a source track onto its target track, shifting every cue by the
// rule offset. The source/version pair must match the rule exactly.
func Apply(r Rule, src *track.Track) (*track.Track, error) {
	if src.ID != r.Source || src.Version != r.SourceVersion {
		return nil, fault.New(fault.StageMap, fault.KindState, src.ID,
			fmt.Sprintf("track %s@%s does not match rule %s@%s",
				src.ID, src.Version, r.Source, r.SourceVersion))
	}
	out := &track.Track{
		ID:       r.TargetTrack,
		Language: r.Language,
		Format:   src.Format,
		Version:  src.Version,
		Source:   src.Source,
		Cues:     make([]track.Cue, 0, len(src.Cues)),
	}
	for _, c := range src.Cues {
		start, err := c.Start.Add(r.Offset)
		if err != nil {
			return nil, err
		}
		end, err := c.End.Add(r.Offset)
		if err != nil {
			return nil, err
		}
		c.Start, c.End = start, end
		out.Cues = append(out.Cues, c)
	}
	return out, nil
}
