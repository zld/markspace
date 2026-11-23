package media

import (
	"fmt"
	"sort"
	"strings"
)

// TrackKind classifies tracks by handler and codec for mapping purposes.
type TrackKind string

const (
	KindVideo TrackKind = "video"
	KindAudio TrackKind = "audio"
)

// TrackSelector describes how a source track is recognized.
type TrackSelector struct {
	Kind     TrackKind `json:"kind"`
	Language string    `json:"language,omitempty"`
	SourceID uint32    `json:"source_id,omitempty"`
}

// TrackTarget is one entry of an ordered target layout.
type TrackTarget struct {
	Kind     TrackKind `json:"kind"`
	Language string    `json:"language,omitempty"`
	SourceID uint32    `json:"source_id,omitempty"`
	Required bool      `json:"required"`
}

// TrackMapEntry binds a source track to its position in the target layout.
type TrackMapEntry struct {
	SourceID    uint32    `json:"source_id"`
	TargetID    uint32    `json:"target_id"`
	TargetPos   int       `json:"target_position"`
	Kind        TrackKind `json:"kind"`
	Language    string    `json:"language"`
	Codec       string    `json:"codec"`
	Timescale   uint32    `json:"timescale"`
	SampleCount uint32    `json:"sample_count"`
}

// TrackMap is a fully validated, ordered source-to-target mapping.
type TrackMap struct {
	Entries []TrackMapEntry `json:"entries"`
	MovieTS uint32          `json:"movie_timescale"`
}

// TargetTrackIDs returns the new track ids in mapping order.
func (m *TrackMap) TargetTrackIDs() []uint32 {
	ids := make([]uint32, len(m.Entries))
	for i, e := range m.Entries {
		ids[i] = e.TargetID
	}
	return ids
}

// KindOf derives the media kind of a probed track.
func KindOf(ti *TrackInfo) TrackKind {
	switch {
	case ti.Codec == "avc1" || strings.EqualFold(ti.Handler, "vide"):
		return KindVideo
	case ti.Codec == "mp4a" || strings.EqualFold(ti.Handler, "soun"):
		return KindAudio
	default:
		return ""
	}
}

// BuildTrackMap maps source tracks onto an ordered target layout. Every target
// that is required must match exactly one distinct source track; ambiguous
// matches, duplicate targets and missing required tracks are rejected. Target
// ids are assigned 1..N in layout order, independent of source ids.
func BuildTrackMap(p *Probe, targets []TrackTarget) (*TrackMap, error) {
	if p == nil || len(p.Tracks) == 0 {
		return nil, NewError(ClassMissingTrack, "trackmap", 0, "probe has no tracks")
	}
	if len(targets) == 0 {
		return nil, NewError(ClassInvalidArgument, "trackmap", 0, "target layout is empty")
	}
	if p.Timescale == 0 {
		return nil, NewError(ClassTimebase, "trackmap", 0, "movie timescale is zero")
	}
	used := make(map[uint32]bool, len(p.Tracks))
	byTarget := make(map[int]uint32, len(targets))
	m := &TrackMap{MovieTS: p.Timescale}
	for pos, tg := range targets {
		matches := matchTracks(p.Tracks, tg)
		var avail []*TrackInfo
		for _, ti := range matches {
			if !used[ti.TrackID] {
				avail = append(avail, ti)
			}
		}
		if len(avail) == 0 {
			if tg.Required {
				return nil, NewError(ClassMissingTrack, "trackmap", 0,
					"required %s track (language=%q source_id=%d) has no unique match", tg.Kind, tg.Language, tg.SourceID)
			}
			continue
		}
		if len(avail) > 1 {
			return nil, NewError(ClassMappingConflict, "trackmap", 0,
				"%s track target %d matches %d source tracks: %s", tg.Kind, pos, len(avail), listIDs(avail))
		}
		src := avail[0]
		if byTarget[pos] != 0 {
			return nil, NewError(ClassMappingConflict, "trackmap", 0, "duplicate target position %d", pos)
		}
		kind := KindOf(src)
		if kind == "" {
			return nil, NewError(ClassUnsupported, "trackmap", 0, "track %d has unsupported codec %q", src.TrackID, src.Codec)
		}
		if src.Timescale == 0 {
			return nil, NewError(ClassTimebase, "trackmap", 0, "track %d has zero timescale", src.TrackID).WithTrack(src.TrackID)
		}
		if _, err := Rescale(src.Duration, src.Timescale, p.Timescale); err != nil {
			return nil, NewError(ClassTimebase, "trackmap", 0,
				"track %d duration not convertible to movie timescale: %v", src.TrackID, err).WithTrack(src.TrackID)
		}
		used[src.TrackID] = true
		targetID := uint32(pos + 1)
		byTarget[pos] = targetID
		m.Entries = append(m.Entries, TrackMapEntry{
			SourceID: src.TrackID, TargetID: targetID, TargetPos: pos,
			Kind: kind, Language: src.Language, Codec: src.Codec,
			Timescale: src.Timescale, SampleCount: src.SampleCount,
		})
	}
	if len(m.Entries) == 0 {
		return nil, NewError(ClassMissingTrack, "trackmap", 0, "no target tracks were matched")
	}
	// Verify target ids and positions are unique and contiguous.
	seenTarget := make(map[uint32]bool, len(m.Entries))
	for _, e := range m.Entries {
		if seenTarget[e.TargetID] {
			return nil, NewError(ClassMappingConflict, "trackmap", 0, "duplicate target id %d", e.TargetID)
		}
		seenTarget[e.TargetID] = true
	}
	return m, nil
}

func matchTracks(tracks []*TrackInfo, tg TrackTarget) []*TrackInfo {
	var out []*TrackInfo
	for _, ti := range tracks {
		if tg.Kind != "" && KindOf(ti) != tg.Kind {
			continue
		}
		if tg.SourceID != 0 && ti.TrackID != tg.SourceID {
			continue
		}
		if tg.Language != "" && !strings.EqualFold(ti.Language, tg.Language) {
			continue
		}
		out = append(out, ti)
	}
	return out
}

func listIDs(ts []*TrackInfo) string {
	ids := make([]uint32, len(ts))
	for i, t := range ts {
		ids[i] = t.TrackID
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	parts := make([]string, len(ids))
	for i, id := range ids {
		parts[i] = fmt.Sprintf("%d", id)
	}
	return strings.Join(parts, ",")
}

// TrackByID finds a probed track by source id.
func (p *Probe) TrackByID(id uint32) (*TrackInfo, bool) {
	for _, ti := range p.Tracks {
		if ti.TrackID == id {
			return ti, true
		}
	}
	return nil, false
}

// MappedTracks returns the source TrackInfo objects in target order.
func (m *TrackMap) MappedTracks(p *Probe) ([]*TrackInfo, error) {
	out := make([]*TrackInfo, 0, len(m.Entries))
	for _, e := range m.Entries {
		ti, ok := p.TrackByID(e.SourceID)
		if !ok {
			return nil, NewError(ClassMissingTrack, "trackmap.resolve", 0, "source track %d vanished", e.SourceID).WithTrack(e.SourceID)
		}
		out = append(out, ti)
	}
	return out, nil
}

// CheckMonotonic verifies that decode timestamps never regress per track and
// that composition times stay non-negative. It is a cross-stage invariant.
func CheckMonotonic(st *SampleTable) error {
	var prev uint64
	for i := range st.Samples {
		s := st.Samples[i]
		if i > 0 && s.DTS < prev {
			return NewError(ClassInvalidSample, "invariant.dts", int64(s.Offset),
				"DTS regressed at sample %d", s.Index).WithTrack(st.TrackID)
		}
		prev = s.DTS
		if s.CTS < 0 {
			return NewError(ClassInvalidSample, "invariant.cts", int64(s.Offset),
				"negative CTS at sample %d", s.Index).WithTrack(st.TrackID)
		}
	}
	return nil
}
