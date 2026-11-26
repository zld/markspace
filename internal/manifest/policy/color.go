package policy

import (
	"fmt"
	"sort"
	"strings"

	"continuity/internal/media"
)

// Color priority levels. Numeric priority increases with specificity.
const (
	LevelContainer = "container"
	LevelTrack     = "track"
	LevelFrame     = "frame"
)

// ColorObservation is color metadata with its provenance and priority.
type ColorObservation struct {
	Field  string `json:"field"`
	Value  string `json:"value"`
	Level  string `json:"level"`
	Source string `json:"source"`
}

// ColorDecision explains the selected value and any discarded candidates.
type ColorDecision struct {
	Field              string             `json:"field"`
	Value              string             `json:"value"`
	Level              string             `json:"level"`
	Source             string             `json:"source"`
	Reason             string             `json:"reason"`
	ExplicitOverride   bool               `json:"explicit_override,omitempty"`
	ConflictCandidates []ColorObservation `json:"conflict_candidates,omitempty"`
}

// ColorPlan is the complete explainable color resolution.
type ColorPlan struct {
	Decisions []ColorDecision `json:"decisions"`
}

// ConflictError reports same-priority conflicting color sources.
type ConflictError struct {
	Structured *media.Error       `json:"structured"`
	Field      string             `json:"field"`
	Candidates []ColorObservation `json:"candidates"`
}

func (e *ConflictError) Error() string {
	if e == nil || e.Structured == nil {
		return "color conflict"
	}
	return e.Structured.Error()
}

func (e *ConflictError) Unwrap() []error {
	return []error{e.Structured, media.ErrColorConflict}
}

func priority(level string) int {
	switch level {
	case LevelFrame:
		return 3
	case LevelTrack:
		return 2
	case LevelContainer:
		return 1
	default:
		return 0
	}
}

// ResolveColor applies frame > track > container priority. Equal-priority
// disagreements require an explicit policy for that field.
func ResolveColor(observations []ColorObservation, explicit map[string]string) (*ColorPlan, error) {
	byField := map[string][]ColorObservation{}
	for _, ob := range observations {
		if ob.Field == "" || ob.Value == "" {
			continue
		}
		byField[ob.Field] = append(byField[ob.Field], ob)
	}

	fields := make([]string, 0, len(byField))
	for f := range byField {
		fields = append(fields, f)
	}
	sort.Strings(fields)

	plan := &ColorPlan{}
	for _, field := range fields {
		obs := byField[field]
		top := topPriority(obs)
		var candidates []ColorObservation
		for _, ob := range obs {
			if priority(ob.Level) == top {
				candidates = append(candidates, ob)
			}
		}
		distinct := distinctValues(candidates)
		if len(distinct) == 1 {
			chosen := candidates[0]
			plan.Decisions = append(plan.Decisions, ColorDecision{
				Field:  field,
				Value:  chosen.Value,
				Level:  chosen.Level,
				Source: chosen.Source,
				Reason: uniqueReason(chosen, obs),
			})
			continue
		}

		if value := strings.TrimSpace(explicit[field]); value != "" {
			plan.Decisions = append(plan.Decisions, ColorDecision{
				Field:              field,
				Value:              value,
				Level:              "explicit_policy",
				Source:             "explicit:" + field,
				Reason:             "same-priority sources conflicted; explicit policy selected the requested value",
				ExplicitOverride:   true,
				ConflictCandidates: append([]ColorObservation(nil), candidates...),
			})
			continue
		}

		sources := make([]string, 0, len(candidates))
		for _, c := range candidates {
			sources = append(sources, fmt.Sprintf("%s=%q@%s(%s)", c.Field, c.Value, c.Source, c.Level))
		}
		me := media.NewError(media.StageColor, -1, "color/"+field,
			fmt.Errorf("%w: %s", media.ErrColorConflict, strings.Join(sources, "; "))).
			WithKind("same_priority_conflict")
		return nil, &ConflictError{Structured: me, Field: field, Candidates: append([]ColorObservation(nil), candidates...)}
	}
	return plan, nil
}

func topPriority(obs []ColorObservation) int {
	top := 0
	for _, ob := range obs {
		if p := priority(ob.Level); p > top {
			top = p
		}
	}
	return top
}

func distinctValues(obs []ColorObservation) []string {
	seen := map[string]bool{}
	var out []string
	for _, ob := range obs {
		if !seen[ob.Value] {
			seen[ob.Value] = true
			out = append(out, ob.Value)
		}
	}
	sort.Strings(out)
	return out
}

func uniqueReason(chosen ColorObservation, all []ColorObservation) string {
	higherExists := false
	for _, ob := range all {
		if ob.Field == chosen.Field && priority(ob.Level) < priority(chosen.Level) {
			higherExists = true
		}
	}
	if higherExists {
		return fmt.Sprintf("%s metadata has highest precedence; selected from %s", chosen.Level, chosen.Source)
	}
	return fmt.Sprintf("unique %s value from %s", chosen.Level, chosen.Source)
}
