// Package diagnose defines the offset-carrying error model shared by every
// stage and turns individual findings into half-open corrupt byte ranges.
package diagnose

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"mediad/internal/core"
)

// Category is the stable, machine-readable fault class.
type Category string

const (
	// CatTruncatedBox marks a box whose declared extent runs past the input.
	CatTruncatedBox Category = "truncated_box"
	// CatMP4Length marks a malformed MP4 box length field.
	CatMP4Length Category = "mp4_length"
	// CatSRTTime marks an unparsable SRT timing line.
	CatSRTTime Category = "srt_time"
	// CatVTTTime marks an unparsable WebVTT timing line.
	CatVTTTime Category = "vtt_time"
	// CatTruncatedCue marks a subtitle cue whose body is cut off.
	CatTruncatedCue Category = "truncated_cue"
	// CatRTPSequence marks a discontinuity in RTP sequence numbers.
	CatRTPSequence Category = "rtp_sequence_gap"
	// CatRTPWrap marks a 32-bit RTP timestamp rollover.
	CatRTPWrap Category = "rtp_wrap"
	// CatNodeDrift marks a measured node clock drift correction.
	CatNodeDrift Category = "node_drift"
	// CatTimecodeLabel marks an invalid or misplaced timecode label.
	CatTimecodeLabel Category = "timecode_label"
	// CatIndexOutOfRange marks a sample/index reference outside its source.
	CatIndexOutOfRange Category = "index_out_of_range"
	// CatMonotonic marks a clock that regressed after normalization.
	CatMonotonic Category = "time_non_monotonic"
	// CatInvariant marks a failed cross-stage invariant.
	CatInvariant Category = "invariant"
	// CatUnsupported marks a structurally valid but unsupported input.
	CatUnsupported Category = "unsupported"
)

// Stage names the pipeline phase in which a finding was produced.
type Stage string

const (
	StageIngest    Stage = "ingest"
	StageNormalize Stage = "normalize"
	StageIndex     Stage = "index"
	StageDiagnose  Stage = "diagnose"
	StageValidate  Stage = "validate"
)

var allStages = []Stage{StageIngest, StageNormalize, StageIndex, StageDiagnose, StageValidate}

// ValidStage reports whether s is one of the five pipeline stages.
func ValidStage(s Stage) bool {
	for _, v := range allStages {
		if s == v {
			return true
		}
	}
	return false
}

// AllStages returns the canonical stage order.
func AllStages() []Stage {
	out := make([]Stage, len(allStages))
	copy(out, allStages)
	return out
}

// Issue is an offset-carrying finding. The span is always half-open and is
// expressed in bytes of the original asset, never in normalized coordinates.
type Issue struct {
	Category    Category  `json:"category"`
	Stage       Stage     `json:"stage"`
	Format      string    `json:"format"`
	Asset       string    `json:"asset"`
	Span        core.Span `json:"span"`
	Detail      string    `json:"detail,omitempty"`
	Recoverable bool      `json:"recoverable"`
	Node        string    `json:"node,omitempty"`
}

// Offset returns the original byte offset where the fault starts.
func (i Issue) Offset() int64 { return i.Span.Start }

// Error implements error so an Issue can be returned directly from parsers.
func (i Issue) Error() string {
	return fmt.Sprintf("%s/%s: %s at %s [%d,%d): %s",
		i.Asset, i.Stage, i.Category, i.Format, i.Span.Start, i.Span.End, i.Detail)
}

// Validate checks that every field is well formed and the span is inside total.
func (i Issue) Validate(total int64) error {
	if i.Category == "" {
		return errors.New("diagnose: empty category")
	}
	if !ValidStage(i.Stage) {
		return fmt.Errorf("diagnose: invalid stage %q", i.Stage)
	}
	if i.Format == "" || i.Asset == "" {
		return errors.New("diagnose: missing format or asset")
	}
	if i.Span.Start < 0 || i.Span.End < i.Span.Start {
		return fmt.Errorf("diagnose: invalid span [%d,%d)", i.Span.Start, i.Span.End)
	}
	if total >= 0 && i.Span.End > total {
		return fmt.Errorf("diagnose: span [%d,%d) exceeds asset size %d", i.Span.Start, i.Span.End, total)
	}
	return nil
}

// Issues is a deterministic, sortable collection.
type Issues []Issue

// Add appends and keeps the collection ordered.
func (is *Issues) Add(i Issue) { *is = append(*is, i) }

// Sort orders by asset, offset, category, stage and detail.
func (is Issues) Sort() {
	sort.SliceStable(is, func(a, b int) bool {
		x, y := is[a], is[b]
		if x.Asset != y.Asset {
			return x.Asset < y.Asset
		}
		if x.Span.Start != y.Span.Start {
			return x.Span.Start < y.Span.Start
		}
		if x.Span.End != y.Span.End {
			return x.Span.End < y.Span.End
		}
		if x.Category != y.Category {
			return x.Category < y.Category
		}
		if x.Stage != y.Stage {
			return x.Stage < y.Stage
		}
		return x.Detail < y.Detail
	})
}

// CountByCategory returns category counts in sorted order.
func (is Issues) CountByCategory() []CategoryCount {
	m := make(map[Category]int, len(is))
	for _, i := range is {
		m[i.Category]++
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, string(k))
	}
	sort.Strings(keys)
	out := make([]CategoryCount, 0, len(keys))
	for _, k := range keys {
		out = append(out, CategoryCount{Category: Category(k), Count: m[Category(k)]})
	}
	return out
}

// CategoryCount pairs a category with its frequency.
type CategoryCount struct {
	Category Category `json:"category"`
	Count    int      `json:"count"`
}

// Summary renders a stable one-line list such as "mp4_length=1,vtt_time=1".
func (is Issues) Summary() string {
	cc := is.CountByCategory()
	parts := make([]string, 0, len(cc))
	for _, c := range cc {
		parts = append(parts, fmt.Sprintf("%s=%d", c.Category, c.Count))
	}
	return strings.Join(parts, ",")
}

// IsDamage reports whether the category represents damaged input that
// downstream retry logic should not treat as healthy.
func (i Issue) IsDamage() bool {
	switch i.Category {
	case CatTruncatedBox, CatMP4Length, CatSRTTime, CatVTTTime, CatTruncatedCue,
		CatRTPSequence, CatNodeDrift, CatTimecodeLabel, CatIndexOutOfRange,
		CatMonotonic, CatInvariant, CatUnsupported:
		return true
	}
	return false
}
