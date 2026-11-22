// Package manifest parses the self-generated multi-track manifests and edit
// lists. All timestamps inside a manifest are milliseconds on the shared
// clock base (see package clock); non-zero source start offsets are first
// class fields rather than implicit zeros.
package manifest

import (
	"bytes"
	"encoding/json"
	"io"
	"sort"

	"github.com/medialab/medialab/internal/clock"
)

const (
	KindVideo = "video"
	KindAudio = "audio"
)

// TrackSpec describes one source track and its sparse keyframe positions.
type TrackSpec struct {
	ID         string  `json:"id"`
	Kind       string  `json:"kind"`
	ClockRate  uint32  `json:"clock_rate"`
	SSRC       uint32  `json:"ssrc"`
	StartMs    int64   `json:"start_ms"`     // non-zero source start time
	DurationMs int64   `json:"duration_ms"`  // source material length
	Keyframes  []int64 `json:"keyframes_ms"` // positions relative to StartMs
}

// Edit maps a half-open source interval onto the output timeline.
type Edit struct {
	Track          string `json:"track"`
	SourceStartMs  int64 `json:"source_start_ms"`
	TimelineStartMs int64 `json:"timeline_start_ms"`
	DurationMs     int64  `json:"duration_ms"`
}

// Manifest is the full multi-track description consumed by the pipeline.
type Manifest struct {
	Version  int         `json:"version"`
	Seed     int64       `json:"seed"`
	TickHz   int64       `json:"tick_hz"`
	Tracks   []TrackSpec `json:"tracks"`
	Edits    []Edit      `json:"edits"`
}

// Parse decodes a manifest and runs every structural invariant. A truncated
// document (or one missing required sections) is reported as KindTruncated.
func Parse(r io.Reader) (*Manifest, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return nil, clock.NewError(clock.KindTruncated, "manifest", "", 0, err.Error())
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return nil, clock.NewError(clock.KindTruncated, "manifest", "", 0, "empty document")
	}
	var m Manifest
	dec := json.NewDecoder(bytes.NewReader(data))
	if err := dec.Decode(&m); err != nil {
		kind := clock.KindOutOfRange
		if err == io.ErrUnexpectedEOF || err == io.EOF {
			kind = clock.KindTruncated
		}
		return nil, clock.NewError(kind, "manifest", "", 0, err.Error())
	}
	if dec.Decode(new(json.RawMessage)) != io.EOF {
		return nil, clock.NewError(clock.KindTruncated, "manifest", "", 0, "trailing JSON values")
	}
	if err := m.Validate(); err != nil {
		return nil, err
	}
	return &m, nil
}

// Write serializes the manifest deterministically.
func (m *Manifest) Write(w io.Writer) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(m)
}

// TrackByID resolves a track reference, reporting an out-of-range error that
// carries the offending track id.
func (m *Manifest) TrackByID(id string) (*TrackSpec, error) {
	for i := range m.Tracks {
		if m.Tracks[i].ID == id {
			return &m.Tracks[i], nil
		}
	}
	return nil, clock.NewError(clock.KindOutOfRange, "manifest", id, 0, "edit references unknown track")
}

// Validate enforces: positive geometry, no negative times, keyframes inside
// the track, no overlapping edits and no out-of-range edit references.
func (m *Manifest) Validate() error {
	if m.TickHz != clock.TicksPerSecond {
		return clock.NewError(clock.KindOutOfRange, "manifest", "", 0,
			"tick_hz must match the shared clock base")
	}
	if len(m.Tracks) == 0 {
		return clock.NewError(clock.KindTruncated, "manifest", "", 0, "no tracks declared")
	}
	ids := map[string]bool{}
	for i := range m.Tracks {
		tr := &m.Tracks[i]
		if tr.ID == "" {
			return clock.NewError(clock.KindTruncated, "manifest", tr.ID, 0, "track without id")
		}
		if ids[tr.ID] {
			return clock.NewError(clock.KindOverlap, "manifest", tr.ID, 0, "duplicate track id")
		}
		ids[tr.ID] = true
		if tr.Kind != KindVideo && tr.Kind != KindAudio {
			return clock.NewError(clock.KindOutOfRange, "manifest", tr.ID, 0,
				"track kind must be video or audio")
		}
		if tr.ClockRate == 0 {
			return clock.NewError(clock.KindOutOfRange, "manifest", tr.ID, tr.StartMs, "zero clock rate")
		}
		if tr.StartMs < 0 || tr.DurationMs <= 0 {
			return clock.NewError(clock.KindNegativeTime, "manifest", tr.ID, tr.StartMs,
				"negative start or non-positive duration")
		}
		if err := checkKeyframes(tr); err != nil {
			return err
		}
	}
	return m.validateEdits()
}

func checkKeyframes(tr *TrackSpec) error {
	prev := int64(-1)
	for _, k := range tr.Keyframes {
		if k < 0 {
			return clock.NewError(clock.KindNegativeTime, "manifest", tr.ID, tr.StartMs+k,
				"keyframe before track start")
		}
		if k >= tr.DurationMs {
			return clock.NewError(clock.KindOutOfRange, "manifest", tr.ID, tr.StartMs+k,
				"keyframe beyond track duration")
		}
		if k <= prev {
			return clock.NewError(clock.KindNonMonotonic, "manifest", tr.ID, tr.StartMs+k,
				"keyframes must be strictly increasing and sparse")
		}
		prev = k
	}
	return nil
}

func (m *Manifest) validateEdits() error {
	if len(m.Edits) == 0 {
		return clock.NewError(clock.KindTruncated, "manifest", "", 0, "no edits declared")
	}
	groups := map[string][]Edit{}
	for i := range m.Edits {
		e := &m.Edits[i]
		tr, err := m.TrackByID(e.Track)
		if err != nil {
			return err
		}
		if e.SourceStartMs < 0 || e.TimelineStartMs < 0 {
			return clock.NewError(clock.KindNegativeTime, "manifest", e.Track,
				e.TimelineStartMs, "edit starts before epoch")
		}
		if e.DurationMs <= 0 {
			return clock.NewError(clock.KindNegativeTime, "manifest", e.Track,
				e.TimelineStartMs, "edit duration must be positive")
		}
		srcEnd, err := clock.CheckedAdd(e.SourceStartMs, e.DurationMs)
		if err != nil {
			return err
		}
		if srcEnd > tr.DurationMs {
			return clock.NewError(clock.KindOutOfRange, "manifest", e.Track,
				tr.StartMs+srcEnd, "edit extends beyond source material")
		}
		if _, err := clock.CheckedAdd(e.TimelineStartMs, e.DurationMs); err != nil {
			return err
		}
		groups[e.Track] = append(groups[e.Track], *e)
	}
	for track, edits := range groups {
		sort.Slice(edits, func(i, j int) bool {
			return edits[i].TimelineStartMs < edits[j].TimelineStartMs
		})
		for i := 1; i < len(edits); i++ {
			prevEnd, err := clock.CheckedAdd(edits[i-1].TimelineStartMs, edits[i-1].DurationMs)
			if err != nil {
				return err
			}
			if edits[i].TimelineStartMs < prevEnd {
				return clock.NewError(clock.KindOverlap, "manifest", track,
					edits[i].TimelineStartMs, "edits overlap on output timeline")
			}
		}
	}
	return nil
}

// Resolve maps a source-relative tick (offset from track start) to an output
// timeline tick through the edit list. A position outside every edit is an
// out-of-range error carrying the absolute source position.
func (m *Manifest) Resolve(trackID string, sourceTick int64) (int64, error) {
	tr, err := m.TrackByID(trackID)
	if err != nil {
		return 0, err
	}
	for i := range m.Edits {
		e := &m.Edits[i]
		if e.Track != trackID {
			continue
		}
		if sourceTick >= e.SourceStartMs && sourceTick < e.SourceStartMs+e.DurationMs {
			out, err := clock.CheckedAdd(e.TimelineStartMs, sourceTick-e.SourceStartMs)
			if err != nil {
				return 0, err
			}
			return out, nil
		}
	}
	return 0, clock.NewError(clock.KindOutOfRange, "manifest", trackID,
		tr.StartMs+sourceTick, "source position not covered by any edit")
}
