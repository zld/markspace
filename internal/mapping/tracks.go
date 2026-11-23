// Package mapping resolves fragmented MP4 tracks onto stable output
// slots using explicit rules keyed by track id, media type and codec.
// Conflicts and missing tracks produce diagnostics instead of silent
// remapping, and an unresolved conflict stops the affected output only.
package mapping

import (
	"fmt"
	"sort"

	"mediatool/internal/mp4"
	"mediatool/internal/timeline"
)

// Rule binds an input track identity to an output slot name.
type Rule struct {
	// OutputSlot is the stable name used in manifests (e.g. "video-main").
	OutputSlot string
	// TrackID, when non-zero, requires an exact MP4 track id match.
	TrackID uint32
	// MediaType is "video" or "audio"; empty means any type.
	MediaType string
	// CodecKey pins the required codec FourCC; empty means any codec.
	CodecKey string
}

// Slot is one resolved output: the rule that produced it and the track
// metadata it resolved to.
type Slot struct {
	Rule      Rule
	Track     mp4.TrackInfo
	Resolved  bool
	Diagnosis string
}

// Mapper holds explicit rules and the tracks observed in the init data.
type Mapper struct {
	rules  []Rule
	tracks map[uint32]mp4.TrackInfo
	// slots caches the last resolution; Recompute is idempotent.
	slots []Slot
}

// NewMapper builds a Mapper from explicit rules. Rules are stored in the
// given order, which is also the deterministic output order.
func NewMapper(rules []Rule) *Mapper {
	cp := make([]Rule, len(rules))
	copy(cp, rules)
	return &Mapper{rules: cp, tracks: make(map[uint32]mp4.TrackInfo)}
}

// SetTracks registers the tracks parsed from the init segment.
func (m *Mapper) SetTracks(tracks []mp4.TrackInfo) {
	for _, tr := range tracks {
		m.tracks[tr.TrackID] = tr
	}
}

// Recompute applies every rule deterministically and returns one Slot per
// rule. Calling it repeatedly with unchanged inputs yields identical
// output. Unresolvable rules leave Slot.Resolved false with a diagnosis;
// they never mutate another slot.
func (m *Mapper) Recompute() []Slot {
	slots := make([]Slot, len(m.rules))
	usedBy := make(map[uint32]int) // trackID -> rule index that claimed it
	for i, rule := range m.rules {
		slot := Slot{Rule: rule}
		candidates := m.candidates(rule)
		switch {
		case len(candidates) == 0:
			slot.Diagnosis = diagnoseMissing(rule, m.tracks)
		case len(candidates) > 1:
			slot.Diagnosis = fmt.Sprintf(
				"mapping_conflict: %d tracks match rule slot=%q", len(candidates), rule.OutputSlot)
		default:
			tr := candidates[0]
			if owner, taken := usedBy[tr.TrackID]; taken {
				slot.Diagnosis = fmt.Sprintf(
					"mapping_conflict: track %d already assigned to slot %q",
					tr.TrackID, m.rules[owner].OutputSlot)
			} else {
				slot.Track = tr
				slot.Resolved = true
				usedBy[tr.TrackID] = i
			}
		}
		slots[i] = slot
	}
	m.slots = slots
	return slots
}

// candidates returns all tracks satisfying a rule, sorted by TrackID so
// ambiguity is stable across runs.
func (m *Mapper) candidates(rule Rule) []mp4.TrackInfo {
	var out []mp4.TrackInfo
	for _, tr := range m.tracks {
		if rule.TrackID != 0 && tr.TrackID != rule.TrackID {
			continue
		}
		if rule.MediaType != "" && tr.MediaType != rule.MediaType {
			continue
		}
		if rule.CodecKey != "" && tr.CodecKey != rule.CodecKey {
			continue
		}
		out = append(out, tr)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].TrackID < out[j].TrackID })
	return out
}

// diagnoseMissing distinguishes a codec conflict (track exists but the
// codec does not match) from a wholly absent track.
func diagnoseMissing(rule Rule, tracks map[uint32]mp4.TrackInfo) string {
	for _, tr := range tracks {
		if rule.TrackID != 0 && tr.TrackID == rule.TrackID {
			if rule.CodecKey != "" && tr.CodecKey != rule.CodecKey {
				return fmt.Sprintf(
					"mapping_conflict: track %d codec %q != required %q",
					tr.TrackID, tr.CodecKey, rule.CodecKey)
			}
			if rule.MediaType != "" && tr.MediaType != rule.MediaType {
				return fmt.Sprintf(
					"mapping_conflict: track %d type %q != required %q",
					tr.TrackID, tr.MediaType, rule.MediaType)
			}
		}
	}
	return fmt.Sprintf("mapping_conflict: no track satisfies slot=%q trackID=%d type=%q codec=%q",
		rule.OutputSlot, rule.TrackID, rule.MediaType, rule.CodecKey)
}

// ResolvedSlots returns only the slots safe to write.
func (m *Mapper) ResolvedSlots() []Slot {
	var out []Slot
	for _, s := range m.slots {
		if s.Resolved {
			out = append(out, s)
		}
	}
	return out
}

// Failures returns the classified errors for every unresolved slot.
func (m *Mapper) Failures() []*timeline.MediaError {
	var out []*timeline.MediaError
	for _, s := range m.slots {
		if !s.Resolved {
			out = append(out, timeline.NewError(timeline.ClassMappingConflict, s.Diagnosis))
		}
	}
	return out
}
