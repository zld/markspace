// Package mapping applies explicit source-track to target-track rules. The
// rules are declarative: an asset that misses a required source, claims a
// target twice, or carries an incompatible codec is rejected as a whole with
// a stable conflict report. No "best effort" track is ever substituted.
package mapping

import (
	"fmt"
	"sort"

	"mediacheck/internal/demux"
	merr "mediacheck/internal/errors"
)

// Kind is re-exported so callers can express rules without importing demux
// directly.
type Kind = demux.Kind

// Rule binds one parsed source track to one target slot.
type Rule struct {
	// SourceTrackID is the tkhd track ID this rule consumes.
	SourceTrackID uint32
	// TargetSlot is the output position; two rules must not share one.
	TargetSlot uint32
	// RequireCodec, when non-empty, demands this exact sample-entry fourCC.
	RequireCodec string
	// RequireKind demands this handler kind when non-empty.
	RequireKind Kind
}

// Conflict is one mapping failure, kept machine-readable for evidence.
type Conflict struct {
	Rule    Rule
	Code    string
	Message string
}

// Report summarizes a mapping attempt.
type Report struct {
	// Mapped lists source tracks in target-slot order on success.
	Mapped []*demux.Track
	// Conflicts is empty on success; otherwise the asset must be stopped.
	Conflicts []Conflict
}

// OK reports whether every rule resolved without conflict.
func (r *Report) OK() bool { return len(r.Conflicts) == 0 }

// supportedCodec is the closed set this kernel promises to deliver.
var supportedCodec = map[string]Kind{
	"avc1": demux.KindVideo,
	"hvc1": demux.KindVideo,
	"hev1": demux.KindVideo,
	"mp4a": demux.KindAudio,
}

// Apply validates the rules against parsed tracks and binds them. On any
// conflict it returns a Report whose Conflicts describe every problem found
// in that pass, and Mapped is nil: callers must not produce trusted output.
func Apply(tracks []*demux.Track, rules []Rule) *Report {
	rep := &Report{}
	byID := map[uint32]*demux.Track{}
	for _, tr := range tracks {
		byID[tr.ID] = tr
	}
	seenTarget := map[uint32]Rule{}
	bound := map[uint32]*demux.Track{}

	for _, rule := range rules {
		tr, ok := byID[rule.SourceTrackID]
		if !ok {
			rep.Conflicts = append(rep.Conflicts, Conflict{
				Rule: rule, Code: merr.CodeMissingTrack,
				Message: fmt.Sprintf("source track %d is absent", rule.SourceTrackID),
			})
			continue
		}
		if prev, dup := seenTarget[rule.TargetSlot]; dup {
			rep.Conflicts = append(rep.Conflicts, Conflict{
				Rule: rule, Code: merr.CodeDuplicateTarget,
				Message: fmt.Sprintf("target slot %d claimed by tracks %d and %d",
					rule.TargetSlot, prev.SourceTrackID, rule.SourceTrackID),
			})
			continue
		}
		if err := checkCompatibility(tr, rule); err != nil {
			rep.Conflicts = append(rep.Conflicts, Conflict{
				Rule: rule, Code: err.Code, Message: err.Message,
			})
			continue
		}
		seenTarget[rule.TargetSlot] = rule
		bound[rule.TargetSlot] = tr
	}
	if !rep.OK() {
		return rep
	}
	slots := make([]uint32, 0, len(bound))
	for slot := range bound {
		slots = append(slots, slot)
	}
	sort.Slice(slots, func(i, j int) bool { return slots[i] < slots[j] })
	for _, slot := range slots {
		rep.Mapped = append(rep.Mapped, bound[slot])
	}
	return rep
}

func checkCompatibility(tr *demux.Track, rule Rule) *merr.Error {
	wantKind, known := supportedCodec[tr.Codec]
	if !known {
		return merr.New(merr.CodeUnsupportedTrack,
			fmt.Sprintf("codec %q on track %d is not in the supported set", tr.Codec, tr.ID)).
			WithState(merr.StateFatal)
	}
	if rule.RequireCodec != "" && rule.RequireCodec != tr.Codec {
		return merr.New(merr.CodeUnsupportedTrack,
			fmt.Sprintf("track %d codec %q, rule requires %q", tr.ID, tr.Codec, rule.RequireCodec)).
			WithState(merr.StateFatal)
	}
	if rule.RequireKind != "" && tr.Kind != rule.RequireKind {
		return merr.New(merr.CodeUnsupportedTrack,
			fmt.Sprintf("track %d kind %q, rule requires %q", tr.ID, tr.Kind, rule.RequireKind)).
			WithState(merr.StateFatal)
	}
	if tr.Kind != wantKind {
		return merr.New(merr.CodeUnsupportedTrack,
			fmt.Sprintf("codec %q handler kind mismatch on track %d", tr.Codec, tr.ID)).
			WithState(merr.StateFatal)
	}
	return nil
}

// DefaultRules builds the canonical video->slot0, audio->slot1 rules for a
// parsed healthy container.
func DefaultRules(tracks []*demux.Track) []Rule {
	rules := make([]Rule, 0, len(tracks))
	slot := uint32(0)
	for _, tr := range tracks {
		rules = append(rules, Rule{
			SourceTrackID: tr.ID,
			TargetSlot:    slot,
			RequireCodec:  tr.Codec,
			RequireKind:   tr.Kind,
		})
		slot++
	}
	return rules
}
