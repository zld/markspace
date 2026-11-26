// Package track contains the canonical, timebase-normalized track model
// shared by the parsers, mapping rules, merger and report.
package track

import (
	"fmt"

	"mediamerge/internal/clock"
)

// Cue is one subtitle cue on the shared 90kHz timebase.
type Cue struct {
	ID      string      // stable identifier, e.g. source path "#" sequence
	Index   int         // source cue sequence number (1-based, 0 if unknown)
	Start   clock.Ticks // inclusive
	End     clock.Ticks // exclusive, always >= Start
	Text    string      // payload, UTF-8, trailing newlines trimmed
	Source  string      // origin identifier (file path or track name)
	Version string      // explicit source version label
}

// Validate rejects reversed or empty intervals.
func (c Cue) Validate() error {
	if c.End < c.Start {
		return fmt.Errorf("cue %s has reversed interval", c.ID)
	}
	return nil
}

// Track is a parsed source track before normalization into a timeline.
type Track struct {
	ID       string
	Language string
	Format   string // "srt" | "vtt"
	Version  string
	Source   string // origin file
	Cues     []Cue
}

// Kind distinguishes timeline entries.
type Kind string

const (
	KindCue Kind = "cue"
	KindGap Kind = "gap"
	KindRej Kind = "rejected"
)

// Entry is one ordered item on a merged timeline.
type Entry struct {
	Kind    Kind
	Cue     *Cue // when Kind == KindCue
	Start   clock.Ticks
	End     clock.Ticks
	Reasons []string // gap/reject diagnostics
	Sources []string // provenance for every contributing source
}

// Gap records a missing expected contribution with its provenance.
type Gap struct {
	TrackID string
	Source  string
	Version string
	Start   clock.Ticks
	End     clock.Ticks
	Reason  string
}

// Rejected records a cue that failed validation under the active policy.
type Rejected struct {
	Source string
	Line   int
	Start  clock.Ticks
	End    clock.Ticks
	Reason string
}

// Timeline is the normalized output of the merger.
type Timeline struct {
	ID       string
	Language string
	Entries  []Entry
	Gaps     []Gap
	Rejected []Rejected
}

// Cues returns only the accepted cue entries, in timeline order.
func (t *Timeline) Cues() []*Cue {
	out := make([]*Cue, 0, len(t.Entries))
	for i := range t.Entries {
		if t.Entries[i].Kind == KindCue {
			out = append(out, t.Entries[i].Cue)
		}
	}
	return out
}
