package normalize

import (
	"fmt"
	"sort"

	"mediaqc/internal/model"
)

// Source identifies where a color declaration came from; it also defines the
// priority ordering (higher wins) for conflicting metadata.
type Source int

const (
	SourceContainer Source = iota // mp4 colr box / DASH essential property
	SourceBitstream               // in-band VUI/SPS
	SourceManifest                // HLS/DASH advisory attribute
	SourceDefault                 // pipeline fallback, lowest priority
)

func (s Source) String() string {
	switch s {
	case SourceContainer:
		return "container"
	case SourceBitstream:
		return "bitstream"
	case SourceManifest:
		return "manifest"
	default:
		return "default"
	}
}

// ColorDesc is one color declaration. Primaries/Transfer/Matrix follow ISO
// codes; zero values mean "not declared" rather than a real code.
type ColorDesc struct {
	Primaries uint16
	Transfer  uint16
	Matrix    uint16
	FullRange bool
	Source    Source
	Raw       string // original declaration retained verbatim
}

// ColorDecision documents the reconciliation of conflicting declarations.
type ColorDecision struct {
	TrackID      string
	Selected     ColorDesc
	Quarantined  []ColorDesc // unknown/unsafe descriptions isolated
	AllDeclared  []ColorDesc // every original declaration, preserved
	Reason       string
}

// KnownPrimaries is the local whitelist of understood primary codes.
var KnownPrimaries = map[uint16]bool{1: true, 9: true, 6: true, 11: true}

// ColorPolicy reconciles color metadata with explicit priority rules.
type ColorPolicy struct {
	materialID string
}

// NewColorPolicy builds a policy scoped to a material.
func NewColorPolicy(materialID string) *ColorPolicy {
	return &ColorPolicy{materialID: materialID}
}

// Reconcile applies the rules:
//  1. Unknown descriptions are quarantined, never rewritten to a "safe" value.
//  2. Among known descriptions, the highest-priority source wins; ties are
//     broken by a deterministic order so reruns produce identical results.
//  3. If nothing known is present and only unknowns exist, the track is
//     flagged rather than assigned a default color space.
func (p *ColorPolicy) Reconcile(trackID string, descs []ColorDesc) (ColorDecision, error) {
	dec := ColorDecision{TrackID: trackID, AllDeclared: append([]ColorDesc(nil), descs...)}
	if len(descs) == 0 {
		return dec, model.NewError(model.CodeColorConflict, model.StageNormalize,
			p.materialID, trackID, "no color metadata declared")
	}
	ordered := append([]ColorDesc(nil), descs...)
	sort.SliceStable(ordered, func(i, j int) bool {
		if ordered[i].Source != ordered[j].Source {
			// Lower Source enum value = higher priority.
			return ordered[i].Source < ordered[j].Source
		}
		return ordered[i].Raw < ordered[j].Raw
	})

	var known []ColorDesc
	for _, d := range ordered {
		if !KnownPrimaries[d.Primaries] {
			dec.Quarantined = append(dec.Quarantined, d)
			continue
		}
		known = append(known, d)
	}
	if len(known) == 0 {
		return dec, model.NewError(model.CodeUnknownColor, model.StageNormalize,
			p.materialID, trackID,
			fmt.Sprintf("all %d color descriptions are unknown and were quarantined", len(descs)))
	}
	dec.Selected = known[0]
	dec.Reason = fmt.Sprintf("highest-priority %s declaration among %d known, %d quarantined",
		dec.Selected.Source, len(known), len(dec.Quarantined))
	// Detect conflicts among the known declarations for the evidence trail.
	if conflicting(known) {
		return dec, model.NewError(model.CodeColorConflict, model.StageNormalize,
			p.materialID, trackID,
			"conflicting known color descriptions; resolved by priority: "+dec.Reason)
	}
	return dec, nil
}

// conflicting reports whether two known descriptions carry different codes.
func conflicting(known []ColorDesc) bool {
	for i := 1; i < len(known); i++ {
		if known[i].Primaries != known[0].Primaries ||
			known[i].Transfer != known[0].Transfer ||
			known[i].Matrix != known[0].Matrix {
			return true
		}
	}
	return false
}

// MergeDefaults records an explicit pipeline default at the lowest priority.
// It never overrides a real declaration.
func MergeDefaults(descs []ColorDesc, fallback ColorDesc) []ColorDesc {
	fallback.Source = SourceDefault
	return append(descs, fallback)
}
