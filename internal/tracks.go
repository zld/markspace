package continuity

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
)

// Track mapping decouples source track_IDs from output tracks. A target track
// is keyed by media kind plus codec, so a primary/backup switchover that keeps
// the same encoding maps onto one continuous output track, while a change of
// encoding is rejected rather than approximated.

// TargetTrack is one output track of the remuxed timeline.
type TargetTrack struct {
	Key          string
	Kind         MediaKind
	Codec        Codec
	Timescale    uint32
	CodecConfig  []byte
	Width        uint16
	Height       uint16
	ChannelCount uint16
	SampleRate   uint32
}

// TrackMap is a complete, immutable source-to-target mapping. Entries are
// sorted by source key so the mapping hash is deterministic.
type TrackMap struct {
	Entries []Mapping
	// Targets lists target tracks keyed by TargetTrack.Key.
	Targets []TargetTrack
}

// Mapping records how one source track projects onto a target track.
type Mapping struct {
	// Source is the manifest/representation label (e.g. "main" or "backup").
	Source   string
	SourceID uint32
	SourceKey string
	Target   string
	// StableKey includes the source identity, keeping per-source provenance.
	StableKey string
}

// Lookup returns the mapping for a source label and track_ID.
func (tm *TrackMap) Lookup(source string, sourceID uint32) (Mapping, bool) {
	for _, m := range tm.Entries {
		if m.Source == source && m.SourceID == sourceID {
			return m, true
		}
	}
	return Mapping{}, false
}

// Target returns the target track for a key.
func (tm *TrackMap) Target(key string) (TargetTrack, bool) {
	for _, t := range tm.Targets {
		if t.Key == key {
			return t, true
		}
	}
	return TargetTrack{}, false
}

// TrackSource pairs a source label with its demuxed init segment.
type TrackSource struct {
	Label string
	Init  *InitSegment
}

// ErrNoTrackSource is returned when BuildTrackMap is called with no sources.
var ErrNoTrackSource = fmt.Errorf("no track sources")

// BuildTrackMap builds a stable mapping across every source. Two source tracks
// may share a target only when their kind, codec and codec configuration are
// byte-identical and their timescales agree; otherwise the mapping is rejected
// as non-lossless.
func BuildTrackMap(sources []TrackSource) (*TrackMap, error) {
	if len(sources) == 0 {
		return nil, NewError(KindMapping, Location{Offset: -1, Sample: -1}, "no track sources")
	}
	tm := &TrackMap{}
	targets := map[string]*TargetTrack{}
	seenSource := map[string]bool{}
	for _, src := range sources {
		if src.Label == "" {
			return nil, NewError(KindMapping, Location{Offset: -1, Sample: -1}, "source label must not be empty")
		}
		if seenSource[src.Label] {
			return nil, NewError(KindMapping, Location{File: src.Label, Offset: -1, Sample: -1}, "duplicate source label %q", src.Label)
		}
		seenSource[src.Label] = true
		if src.Init == nil {
			return nil, NewError(KindMapping, Location{File: src.Label, Offset: -1, Sample: -1}, "source %q has no init segment", src.Label)
		}
		for _, tr := range src.Init.Tracks {
			if tr.Key == "" {
				return nil, NewError(KindMapping, Location{File: src.Label, Offset: -1, Sample: -1}, "track %d has no stable key", tr.SourceID)
			}
			cur, ok := targets[tr.Key]
			if !ok {
				targets[tr.Key] = &TargetTrack{
					Key: tr.Key, Kind: tr.Kind, Codec: tr.Codec, Timescale: tr.Timescale,
					CodecConfig: append([]byte(nil), tr.CodecConfig...),
					Width: tr.Width, Height: tr.Height, ChannelCount: tr.ChannelCount, SampleRate: tr.SampleRate,
				}
			} else if err := compatibleTarget(cur, tr, src.Label); err != nil {
				return nil, err
			}
			stable := fmt.Sprintf("%s/%s/%d", src.Label, tr.Key, tr.SourceID)
			for _, existing := range tm.Entries {
				if existing.Source == src.Label && existing.SourceID == tr.SourceID {
					return nil, NewError(KindMapping, Location{File: src.Label, Offset: -1, Sample: -1}, "duplicate track_ID %d in source %q", tr.SourceID, src.Label)
				}
			}
			tm.Entries = append(tm.Entries, Mapping{
				Source:    src.Label,
				SourceID:  tr.SourceID,
				SourceKey: tr.Key,
				Target:    tr.Key,
				StableKey: stable,
			})
		}
	}
	if len(tm.Entries) == 0 {
		return nil, NewError(KindMapping, Location{Offset: -1, Sample: -1}, "no tracks to map")
	}
	for _, t := range targets {
		tm.Targets = append(tm.Targets, *t)
	}
	sort.Slice(tm.Targets, func(i, j int) bool { return tm.Targets[i].Key < tm.Targets[j].Key })
	sort.Slice(tm.Entries, func(i, j int) bool {
		if tm.Entries[i].Source != tm.Entries[j].Source {
			return tm.Entries[i].Source < tm.Entries[j].Source
		}
		return tm.Entries[i].SourceID < tm.Entries[j].SourceID
	})
	return tm, nil
}

func compatibleTarget(target *TargetTrack, tr Track, source string) error {
	loc := Location{File: source, Offset: -1, Sample: -1}
	if target.Kind != tr.Kind {
		return NewError(KindMapping, loc, "track %q maps %s onto %s; kinds differ", tr.Key, tr.Kind, target.Kind)
	}
	if target.Codec != tr.Codec {
		return NewError(KindMapping, loc, "track %q maps codec %s onto %s; a transcode would be required", tr.Key, tr.Codec, target.Codec)
	}
	if target.Timescale != tr.Timescale {
		return NewError(KindMapping, loc, "track %q timescale %d differs from mapped %d", tr.Key, tr.Timescale, target.Timescale)
	}
	if !equalBytes(target.CodecConfig, tr.CodecConfig) {
		return NewError(KindMapping, loc, "track %q codec configuration differs between sources; mapping is not lossless", tr.Key)
	}
	if target.Width != tr.Width || target.Height != tr.Height {
		return NewError(KindMapping, loc, "track %q geometry %dx%d differs from mapped %dx%d", tr.Key, tr.Width, tr.Height, target.Width, target.Height)
	}
	if target.ChannelCount != tr.ChannelCount || target.SampleRate != tr.SampleRate {
		return NewError(KindMapping, loc, "track %q audio layout differs between sources", tr.Key)
	}
	return nil
}

func equalBytes(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// Hash returns a stable digest over the mapping. It covers source labels,
// track_IDs, target keys and the byte content of every target codec config so
// checkpoints can detect a changed mapping.
func (tm *TrackMap) Hash() string {
	h := sha256.New()
	h.Write([]byte("continuity/trackmap/v1\n"))
	for _, t := range tm.Targets {
		fmt.Fprintf(h, "target|%s|%s|%s|%d|%dx%d|%d|%d|%x\n",
			t.Key, t.Kind, t.Codec, t.Timescale, t.Width, t.Height, t.ChannelCount, t.SampleRate, t.CodecConfig)
	}
	for _, m := range tm.Entries {
		fmt.Fprintf(h, "map|%s|%d|%s|%s\n", m.Source, m.SourceID, m.SourceKey, m.Target)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// MissingTargets lists source keys in tm that have no target track; used by
// acceptance output to report mapping gaps.
func (tm *TrackMap) MissingTargets() []string {
	var missing []string
	for _, m := range tm.Entries {
		if _, ok := tm.Target(m.Target); !ok {
			missing = append(missing, m.StableKey)
		}
	}
	sort.Strings(missing)
	return missing
}

// Describe renders one line per mapping for terminal output.
func (tm *TrackMap) Describe() []string {
	out := make([]string, 0, len(tm.Entries))
	for _, m := range tm.Entries {
		out = append(out, fmt.Sprintf("%s track %d -> %s (%s)", m.Source, m.SourceID, m.Target, m.StableKey))
	}
	return out
}

// TrackKeyFor derives the stable output key for a media kind and codec.
func TrackKeyFor(kind MediaKind, codec Codec) string {
	return strings.ToLower(fmt.Sprintf("%s-%s", kind, codec))
}
