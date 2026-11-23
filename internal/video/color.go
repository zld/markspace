// Package video normalizes ISO/IEC 14496-12 color metadata.
//
// Multiple sources may describe the same program with different color
// primaries, transfer characteristics, matrix coefficients, or range. The
// normalizer resolves conflicts by a configured source priority and records
// both the chosen values and where they came from. A track with no usable
// metadata is marked unknown; it is never silently treated as BT.709.
package video

import (
	"fmt"
	"sort"
	"strings"

	"mediatool/internal/fault"
)

// Color is normalized color metadata. Zero-valued fields are "unknown" and
// carry no meaning; a known value must be set with all three code points.
type Color struct {
	Primaries uint16 `json:"primaries"`
	Transfer  uint16 `json:"transfer"`
	Matrix    uint16 `json:"matrix"`
	FullRange bool   `json:"full_range"`
	Known     bool   `json:"known"`
	Source    string `json:"source,omitempty"`
	// SourceRank is the priority rank that produced the choice, lower wins.
	SourceRank int `json:"source_rank,omitempty"`
}

// Key returns a comparable identity for the color code points.
func (c Color) Key() string {
	return fmt.Sprintf("%d/%d/%d/%t", c.Primaries, c.Transfer, c.Matrix, c.FullRange)
}

// String renders a compact human-readable description.
func (c Color) String() string {
	if !c.Known {
		return "unknown"
	}
	return fmt.Sprintf("p%d/t%d/m%d/full=%t", c.Primaries, c.Transfer, c.Matrix, c.FullRange)
}

// Candidate is one source's view of the color metadata.
type Candidate struct {
	// Source names the origin, e.g. "container" or "sidecar:cam-b".
	Source string
	// Priority is the configured rank; lower values win.
	Priority int
	Color    Color
	// Offset is the first byte of the metadata record in the input.
	Offset int64
}

// Conflict records a candidate that lost to a higher-priority source.
type Conflict struct {
	Source   string `json:"source"`
	Priority int    `json:"priority"`
	Color    Color  `json:"color"`
	Offset   int64  `json:"offset"`
}

// Decision is the outcome of normalization.
type Decision struct {
	Chosen    Color      `json:"chosen"`
	Conflicts []Conflict `json:"conflicts,omitempty"`
}

// Normalize resolves color candidates by priority.
//
// Candidates with Priority < 0 are ignored. Ties are broken by source name so
// the result is deterministic across runs and map iteration orders. A conflict
// among sources at or above the winning priority is returned as a located
// warning fault alongside the decision; callers may log it and continue.
func Normalize(candidates []Candidate) (Decision, error) {
	valid := make([]Candidate, 0, len(candidates))
	for _, c := range candidates {
		if c.Priority < 0 {
			continue
		}
		valid = append(valid, c)
	}
	if len(valid) == 0 {
		return Decision{Chosen: Color{Known: false}}, nil
	}
	sort.SliceStable(valid, func(i, j int) bool {
		if valid[i].Priority != valid[j].Priority {
			return valid[i].Priority < valid[j].Priority
		}
		return valid[i].Source < valid[j].Source
	})
	winner := valid[0]
	dec := Decision{Chosen: winner.Color}
	dec.Chosen.Source = winner.Source
	dec.Chosen.SourceRank = winner.Priority
	if !winner.Color.Known {
		dec.Chosen = Color{Known: false, Source: winner.Source, SourceRank: winner.Priority}
	}

	var conflictErr error
	for _, c := range valid[1:] {
		if !c.Color.Known {
			continue
		}
		if c.Color.Key() == winner.Color.Key() {
			continue
		}
		dec.Conflicts = append(dec.Conflicts, Conflict{Source: c.Source, Priority: c.Priority, Color: c.Color, Offset: c.Offset})
		if conflictErr == nil {
			conflictErr = (&fault.Fault{
				Kind:     fault.KindColor,
				Offset:   c.Offset,
				Recovery: fault.RecoverContinue,
				Severity: fault.SeverityWarning,
			}).WithDetail("color %s from %s loses to %s from %s", c.Color, c.Source, winner.Color, winner.Source)
		}
	}
	return dec, conflictErr
}

// Record renders the decision as a stable manifest string of the form
// "<color>;source=<name>;rank=<n>;conflicts=<src:color,...>".
func (d Decision) Record() string {
	var b strings.Builder
	b.WriteString(d.Chosen.String())
	b.WriteString(";source=")
	if d.Chosen.Source == "" {
		b.WriteString("unknown")
	} else {
		b.WriteString(d.Chosen.Source)
	}
	fmt.Fprintf(&b, ";rank=%d", d.Chosen.SourceRank)
	if len(d.Conflicts) > 0 {
		b.WriteString(";conflicts=")
		for i, c := range d.Conflicts {
			if i > 0 {
				b.WriteByte(',')
			}
			fmt.Fprintf(&b, "%s:%s", c.Source, c.Color)
		}
	}
	return b.String()
}
