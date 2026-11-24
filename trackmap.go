package mediacore

import (
	"fmt"
	"sort"
)

// TrackRule maps one source track to a target track number by kind and
// source ID. A rule with SourceID 0 applies to the next unmatched track of
// its kind in demux order (a wildcard); when several wildcards match, the
// first one wins and the ambiguity is an error if TargetID is also omitted.
type TrackRule struct {
	Kind     TrackKind `json:"kind"`
	SourceID uint32    `json:"source_id"`
	TargetID uint32    `json:"target_id,omitempty"`
}

// TrackMapConfig is the declarative mapping configuration.
type TrackMapConfig struct {
	Rules []TrackRule `json:"rules"`
	// KeepUnknown carries tracks with other handlers through under their
	// original ID. Such tracks never receive codec-specific rewriting.
	KeepUnknown bool `json:"keep_unknown"`
}

// MappedTrack is the result of applying a mapping to one source track.
type MappedTrack struct {
	SourceID    uint32    `json:"source_id"`
	TargetID    uint32    `json:"target_id"`
	Kind        TrackKind `json:"kind"`
	Codec       string    `json:"codec"`
	Timescale   uint32    `json:"timescale"`
	SampleCount int       `json:"sample_count"`
	Preserved   bool      `json:"preserved"`
}

// TrackMap is the validated mapping for one container.
type TrackMap struct {
	Tracks []MappedTrack `json:"tracks"`
	// ByTarget indexes Tracks by target ID.
	ByTarget map[uint32]int `json:"-"`
}

// TargetIDFor returns the target ID assigned to a source track, if any.
func (m *TrackMap) TargetIDFor(sourceID uint32) (uint32, bool) {
	for _, t := range m.Tracks {
		if t.SourceID == sourceID {
			return t.TargetID, true
		}
	}
	return 0, false
}

// BuildTrackMap applies cfg to c. It fails before any output is written when
// two rules would produce the same target ID, when a target ID is zero or
// duplicated, or when a configured kind does not exist in the source.
func BuildTrackMap(c *Container, cfg TrackMapConfig) (*TrackMap, error) {
	if c == nil || len(c.Tracks) == 0 {
		return nil, fail(KindTrackMap, "track-map", "container has no tracks")
	}
	if err := validateRules(cfg.Rules); err != nil {
		return nil, err
	}

	src := c.Tracks
	assigned := make([]bool, len(src))
	out := &TrackMap{ByTarget: map[uint32]int{}}

	// Explicit rules first, then wildcards; the order makes the outcome
	// independent of map iteration.
	explicit := filterRules(cfg.Rules, true)
	wildcard := filterRules(cfg.Rules, false)
	for _, rule := range explicit {
		idx, err := findSource(src, assigned, rule.Kind, rule.SourceID)
		if err != nil {
			return nil, err
		}
		assigned[idx] = true
		tid := rule.TargetID
		if tid == 0 {
			tid = rule.SourceID
		}
		if err := addMapped(out, &src[idx], tid, false); err != nil {
			return nil, err
		}
	}
	for _, rule := range wildcard {
		mapped := 0
		for i := range src {
			if assigned[i] || src[i].Kind != rule.Kind {
				continue
			}
			assigned[i] = true
			tid := rule.TargetID
			if tid == 0 {
				tid = src[i].ID
			}
			if err := addMapped(out, &src[i], tid, false); err != nil {
				return nil, err
			}
			mapped++
			break
		}
		if mapped == 0 {
			return nil, fail(KindTrackMap, "track-map", "no unmatched %s track for wildcard rule", rule.Kind)
		}
	}
	if cfg.KeepUnknown {
		for i := range src {
			if assigned[i] {
				continue
			}
			if src[i].Kind != KindOtherTrack {
				// A configured kind that was never matched is a
				// configuration error, not something to drop silently.
				return nil, fail(KindTrackMap, "track-map", "no rule matched %s track %d", src[i].Kind, src[i].ID)
			}
			assigned[i] = true
			tid, err := freeID(out.ByTarget, src[i].ID)
			if err != nil {
				return nil, err
			}
			if err := addMapped(out, &src[i], tid, true); err != nil {
				return nil, err
			}
		}
	}

	// Anything left requires an explicit rule; refuse to drop silently.
	for i := range src {
		if !assigned[i] {
			return nil, fail(KindTrackMap, "track-map", "source track %d (%s) has no mapping rule", src[i].ID, src[i].Kind)
		}
	}
	sort.Slice(out.Tracks, func(i, j int) bool { return out.Tracks[i].TargetID < out.Tracks[j].TargetID })
	return out, nil
}

func validateRules(rules []TrackRule) error {
	if len(rules) == 0 {
		return fail(KindTrackMap, "track-map", "no mapping rules configured")
	}
	seen := map[uint32]bool{}
	for i, r := range rules {
		if r.Kind == "" {
			return fail(KindTrackMap, "track-map", "rule %d has no kind", i)
		}
		if r.Kind != KindVideoTrack && r.Kind != KindAudioTrack && r.Kind != KindOtherTrack {
			return fail(KindTrackMap, "track-map", "rule %d has unsupported kind %q", i, r.Kind)
		}
		if r.TargetID == 0 {
			continue
		}
		if seen[r.TargetID] {
			return fail(KindTrackMap, "track-map", "target ID %d is claimed by more than one rule", r.TargetID)
		}
		seen[r.TargetID] = true
	}
	return nil
}

func filterRules(rules []TrackRule, explicit bool) []TrackRule {
	var out []TrackRule
	for _, r := range rules {
		if (r.SourceID != 0) == explicit {
			out = append(out, r)
		}
	}
	return out
}

func findSource(src []Track, assigned []bool, kind TrackKind, id uint32) (int, error) {
	found := -1
	for i := range src {
		if assigned[i] || src[i].Kind != kind {
			continue
		}
		if src[i].ID == id {
			if found >= 0 {
				// Impossible for unique IDs, but keep the check local.
				return -1, fail(KindTrackMap, "track-map", "source track %d is ambiguous", id)
			}
			found = i
		}
	}
	if found < 0 {
		return -1, fail(KindTrackMap, "track-map", "no %s source track with ID %d", kind, id)
	}
	return found, nil
}

func addMapped(out *TrackMap, t *Track, targetID uint32, preserved bool) error {
	if targetID == 0 {
		return fail(KindTrackMap, "track-map", "target ID for source track %d resolves to zero", t.ID)
	}
	if _, dup := out.ByTarget[targetID]; dup {
		return fail(KindTrackMap, "track-map", "target ID %d collides before any output was written", targetID)
	}
	out.ByTarget[targetID] = len(out.Tracks)
	out.Tracks = append(out.Tracks, MappedTrack{
		SourceID:    t.ID,
		TargetID:    targetID,
		Kind:        t.Kind,
		Codec:       t.Codec,
		Timescale:   t.Timescale,
		SampleCount: t.SampleCount(),
		Preserved:   preserved,
	})
	return nil
}

// freeID returns id when unused, else the smallest unused positive ID.
func freeID(used map[uint32]int, id uint32) (uint32, error) {
	if id != 0 {
		if _, ok := used[id]; !ok {
			return id, nil
		}
	}
	for candidate := uint32(1); candidate < 1<<20; candidate++ {
		if _, ok := used[candidate]; !ok {
			return candidate, nil
		}
	}
	return 0, fail(KindTrackMap, "track-map", "no free target track ID")
}

// Describe renders the mapping for terminal output.
func (m *TrackMap) Describe() string {
	if len(m.Tracks) == 0 {
		return "track-map: empty"
	}
	out := fmt.Sprintf("track-map: %d tracks", len(m.Tracks))
	for _, t := range m.Tracks {
		out += fmt.Sprintf("\n  %d:%s -> %d (%s, %d samples)", t.SourceID, t.Kind, t.TargetID, t.Codec, t.SampleCount)
	}
	return out
}
