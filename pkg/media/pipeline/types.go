// Package pipeline orchestrates the multi-bitrate repair: inputs fan out to
// per-representation workers that parse and repair independently, then the
// repaired boundaries fan into the shared RTP clock for cross-rendition
// verification. A representation that fails fatally or diverges beyond one
// tick is isolated; every other representation keeps processing.
package pipeline

import (
	"cmafrepair/pkg/media/clock"
	"cmafrepair/pkg/media/cmaf"
	"cmafrepair/pkg/media/timebase"
)

// RepInput is one rendition's work item.
type RepInput struct {
	Name          string
	InitPath      string
	FragmentPaths []string // already ordered by media sequence
	// Anchor calibrates the representation against the shared clock. When
	// nil, RTPTimestamps is used to derive one.
	Anchor *clock.ClockAnchor
	// RTPTimestamps optionally carries the publish-clock 32-bit timestamps
	// in order; the unwrapper counts wraps/rollbacks and seeds an anchor.
	RTPTimestamps []uint32
	ClockHz       uint32
}

// RepResult is one rendition's repaired state plus its defect log.
type RepResult struct {
	Name      string
	Init      *cmaf.InitInfo
	Fragments []*cmaf.Fragment
	Timeline  *cmaf.Timeline
	Anchor    clock.ClockAnchor
	Wraps     int
	Rollbacks int
	Isolated  bool
	Fatal     *timebase.MediaError // non-nil when the rep could not be repaired
	Defects   []*timebase.MediaError
}

// Options controls fan-out behavior.
type Options struct {
	// MaxConcurrent bounds simultaneous representation workers.
	MaxConcurrent int
}

// DefaultOptions returns safe streaming defaults.
func DefaultOptions() Options {
	return Options{MaxConcurrent: 4}
}

// collectDefects flattens every defect recorded on a representation's
// fragments and timeline entries.
func collectDefects(frags []*cmaf.Fragment, tl *cmaf.Timeline) []*timebase.MediaError {
	var out []*timebase.MediaError
	for _, f := range frags {
		out = append(out, f.Defects...)
	}
	if tl != nil {
		for i := range tl.Entries {
			out = append(out, tl.Entries[i].Defects...)
		}
	}
	return out
}
