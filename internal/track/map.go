package track

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"mediarc/internal/model"
)

// Rule is one explicit source-to-output track mapping. Zero-valued
// SampleRate/Layout mean "preserve and accept the source value"; they never
// authorize conversion.
type Rule struct {
	SourceID   uint32              `json:"source_id"`
	TargetID   uint32              `json:"target_id"`
	Type       model.TrackType     `json:"type"`
	Required   bool                `json:"required"`
	SampleRate int                 `json:"sample_rate,omitempty"`
	Layout     model.ChannelLayout `json:"layout,omitempty"`
}

type Mapping struct {
	SourceID uint32      `json:"source_id"`
	Source   model.Track `json:"source"`
	TargetID uint32      `json:"target_id"`
}

type Plan struct {
	Mappings []Mapping `json:"mappings"`
}

// ParseSpec parses "source:target,source:target". It is intentionally strict.
func ParseSpec(spec string) ([]Rule, error) {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return nil, nil
	}
	var rules []Rule
	seen := map[uint32]bool{}
	for _, part := range strings.Split(spec, ",") {
		kv := strings.Split(strings.TrimSpace(part), ":")
		if len(kv) != 2 {
			return nil, model.NewError(model.StageMap, model.CodeInvalidArgument,
				fmt.Sprintf("bad map rule %q", part), nil)
		}
		sid, err := strconv.ParseUint(strings.TrimSpace(kv[0]), 10, 32)
		if err != nil {
			return nil, model.NewError(model.StageMap, model.CodeInvalidArgument, "bad source track id", err)
		}
		tid, err := strconv.ParseUint(strings.TrimSpace(kv[1]), 10, 32)
		if err != nil {
			return nil, model.NewError(model.StageMap, model.CodeInvalidArgument, "bad target track id", err)
		}
		if sid == 0 || tid == 0 || seen[uint32(sid)] {
			return nil, model.NewError(model.StageMap, model.CodeInvalidArgument,
				fmt.Sprintf("invalid or repeated source in %q", part), nil)
		}
		seen[uint32(sid)] = true
		rules = append(rules, Rule{SourceID: uint32(sid), TargetID: uint32(tid)})
	}
	return rules, nil
}

// PlanMapping validates explicit rules against demuxed tracks. When rules omit
// Type, the source type is used for duplicate-type checks only where callers
// require a unique track class.
func PlanMapping(tracks []model.Track, rules []Rule) (*Plan, error) {
	byID := make(map[uint32]model.Track, len(tracks))
	for _, tr := range tracks {
		byID[tr.ID] = tr
	}

	targets := map[uint32]uint32{}
	sources := map[uint32]bool{}
	plan := &Plan{}

	for i := range rules {
		rule := rules[i]
		tr, ok := byID[rule.SourceID]
		if !ok {
			if rule.Required {
				err := model.NewError(model.StageMap, model.CodeMissingRequired,
					fmt.Sprintf("required source track %d is absent", rule.SourceID), nil)
				return nil, model.AtTrack(err, rule.SourceID)
			}
			continue
		}
		if rule.Type != "" && tr.Type != rule.Type {
			err := model.NewError(model.StageMap, model.CodeMissingRequired,
				fmt.Sprintf("track %d has type %s, want %s", rule.SourceID, tr.Type, rule.Type), nil)
			return nil, model.AtTrack(err, rule.SourceID)
		}
		if rule.TargetID == 0 {
			rule.TargetID = rule.SourceID
		}
		if prev, dup := targets[rule.TargetID]; dup {
			err := model.NewError(model.StageMap, model.CodeDuplicateTarget,
				fmt.Sprintf("target %d assigned to tracks %d and %d", rule.TargetID, prev, rule.SourceID), nil)
			return nil, model.AtTrack(err, rule.SourceID)
		}
		if rule.SampleRate != 0 && tr.SampleRate != rule.SampleRate {
			err := model.NewError(model.StageMap, model.CodeRateMismatch,
				fmt.Sprintf("track %d rate=%d does not match explicit rate=%d; resampling is disabled",
					tr.ID, tr.SampleRate, rule.SampleRate), nil)
			return nil, model.AtTrack(err, tr.ID)
		}
		if rule.Layout != "" && tr.Layout != rule.Layout {
			err := model.NewError(model.StageMap, model.CodeLayoutMismatch,
				fmt.Sprintf("track %d layout=%s does not match explicit layout=%s; downmix is disabled",
					tr.ID, tr.Layout, rule.Layout), nil)
			return nil, model.AtTrack(err, tr.ID)
		}
		targets[rule.TargetID] = rule.SourceID
		sources[rule.SourceID] = true
		mapped := tr
		mapped.ID = rule.TargetID
		plan.Mappings = append(plan.Mappings, Mapping{
			SourceID: rule.SourceID, Source: mapped, TargetID: rule.TargetID,
		})
	}

	for _, rule := range rules {
		if rule.Required && !sources[rule.SourceID] {
			err := model.NewError(model.StageMap, model.CodeMissingRequired,
				fmt.Sprintf("required track %d was not mapped", rule.SourceID), nil)
			return nil, model.AtTrack(err, rule.SourceID)
		}
	}
	if len(plan.Mappings) == 0 {
		return nil, model.NewError(model.StageMap, model.CodeMissingRequired, "no tracks selected", nil)
	}
	sort.Slice(plan.Mappings, func(i, j int) bool { return plan.Mappings[i].TargetID < plan.Mappings[j].TargetID })
	return plan, nil
}

// DefaultRules creates an explicit identity mapping. Required media tracks are
// video and audio when present in the input fixture contract.
func DefaultRules(tracks []model.Track) []Rule {
	rules := make([]Rule, 0, len(tracks))
	for _, tr := range tracks {
		rules = append(rules, Rule{
			SourceID: tr.ID, TargetID: tr.ID, Type: tr.Type,
			Required: tr.Type == model.TrackVideo || tr.Type == model.TrackAudio,
		})
	}
	return rules
}
