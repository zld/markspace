// Package manifest parses the edit manifest that accompanies multi-camera
// capture. Parsing is strict and position-aware: every problem names the file,
// the track (when known) and the field, so an operator can locate it without
// guessing.
package manifest

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"mediasync/internal/model"
)

// Version is the only manifest schema version understood by this kernel.
const Version = 1

// Edit is one edit-list segment. Times are expressed in the track timescale.
type Edit struct {
	StartTicks    int64 `json:"start_ticks"`
	DurationTicks int64 `json:"duration_ticks"`
	MediaTimeTick int64 `json:"media_time_ticks"`
}

// Track describes one camera feed: its time base, non-zero start, edit
// segments and sparse keyframe index.
type Track struct {
	ID            uint32            `json:"id"`
	Kind          model.TrackKind   `json:"kind"`
	Timescale     uint32            `json:"timescale"`
	StartTicks    int64             `json:"start_ticks"`
	DurationTicks int64             `json:"duration_ticks"`
	Edit          []Edit            `json:"edit"`
	Keyframes     []int64           `json:"keyframes"`
	MediaFile     string            `json:"media_file"`
	Labels        map[string]string `json:"labels,omitempty"`
	path          string
	index         int
}

// Manifest is a fully validated capture manifest.
type Manifest struct {
	Version       int     `json:"version"`
	BaseTimescale uint32  `json:"base_timescale"`
	Tracks        []Track `json:"tracks"`
	path          string
}

// TrackByID returns a track by numeric id.
func (m *Manifest) TrackByID(id uint32) (Track, bool) {
	for _, t := range m.Tracks {
		if t.ID == id {
			return t, true
		}
	}
	return Track{}, false
}

// Path returns the source path the manifest was read from.
func (m *Manifest) Path() string { return m.path }

// Start returns the track start converted to the kernel time base.
func (t Track) Start() (model.Nanos, error) { return model.ToNanos(t.StartTicks, t.Timescale) }

// Duration returns the track duration on the kernel time base.
func (t Track) Duration() (model.Nanos, error) { return model.ToNanos(t.DurationTicks, t.Timescale) }

// End returns the exclusive end of the track on the kernel time base.
func (t Track) End() (model.Nanos, error) {
	s, err := t.Start()
	if err != nil {
		return 0, err
	}
	d, err := t.Duration()
	if err != nil {
		return 0, err
	}
	return s + d, nil
}

// KeyframeTimes converts the sparse keyframe index into kernel timestamps.
func (t Track) KeyframeTimes() ([]model.Nanos, error) {
	out := make([]model.Nanos, 0, len(t.Keyframes))
	for i, k := range t.Keyframes {
		if k < 0 {
			return nil, fmt.Errorf("track %d keyframe[%d] is negative", t.ID, i)
		}
		n, err := model.ToNanos(k, t.Timescale)
		if err != nil {
			return nil, fmt.Errorf("track %d keyframe[%d]: %w", t.ID, i, err)
		}
		out = append(out, n)
	}
	return out, nil
}

// EditRange returns the cumulative edit interval on the kernel time base.
// A negative segment duration is rejected before this is called.
func (t Track) EditRange() (model.Interval, error) {
	var start, end int64
	for _, e := range t.Edit {
		if e.DurationTicks < 0 {
			return model.Interval{}, fmt.Errorf("negative edit duration")
		}
		start = min64(start, e.StartTicks)
		end = max64(end, e.StartTicks+e.DurationTicks)
	}
	s, err := model.ToNanos(start, t.Timescale)
	if err != nil {
		return model.Interval{}, err
	}
	e, err := model.ToNanos(end, t.Timescale)
	if err != nil {
		return model.Interval{}, err
	}
	return model.Interval{Start: s, End: e}, nil
}

func min64(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}

func max64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

// ParseFile reads and validates a manifest from disk.
func ParseFile(path string) (*Manifest, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, model.NewError(model.CodeManifestSyntax, model.Loc{File: filepath.Base(path)}, "open manifest").Wrap(err)
	}
	defer f.Close()
	return Parse(f, filepath.Base(path))
}

// Parse decodes and validates a manifest. All findings are collected so a
// single run reports every defect in a stable file order.
func Parse(r io.Reader, name string) (*Manifest, error) {
	raw, err := io.ReadAll(io.LimitReader(r, 8<<20))
	if err != nil {
		return nil, model.NewError(model.CodeManifestSyntax, model.Loc{File: name}, "read manifest").Wrap(err)
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var top map[string]json.RawMessage
	if err := dec.Decode(&top); err != nil {
		return nil, model.NewError(model.CodeManifestSyntax, model.Loc{File: name}, "invalid JSON").Wrap(err)
	}
	if dec.More() {
		return nil, model.NewError(model.CodeManifestSyntax, model.Loc{File: name}, "trailing data after manifest object")
	}

	var errs model.ErrorList
	m := &Manifest{path: name}
	pos := 0

	// version
	if v, ok := top["version"]; !ok {
		errs.Add(model.NewError(model.CodeManifestMissing, model.Loc{File: name, TrackIndex: -1, Field: "version"}, "manifest is missing field"))
	} else if err := json.Unmarshal(v, &m.Version); err != nil {
		errs.Add(model.NewError(model.CodeManifestSyntax, model.Loc{File: name, TrackIndex: -1, Field: "version"}, "expected integer").Wrap(err))
	} else if m.Version != Version {
		errs.Add(model.NewError(model.CodeManifestInvalid, model.Loc{File: name, TrackIndex: -1, Field: "version"}, "unsupported manifest version %d", m.Version))
	}

	// base_timescale
	if v, ok := top["base_timescale"]; !ok {
		errs.Add(model.NewError(model.CodeManifestMissing, model.Loc{File: name, TrackIndex: -1, Field: "base_timescale"}, "manifest is missing field"))
	} else if err := json.Unmarshal(v, &m.BaseTimescale); err != nil {
		errs.Add(model.NewError(model.CodeManifestSyntax, model.Loc{File: name, TrackIndex: -1, Field: "base_timescale"}, "expected integer").Wrap(err))
	} else if m.BaseTimescale == 0 {
		errs.Add(model.NewError(model.CodeManifestInvalid, model.Loc{File: name, TrackIndex: -1, Field: "base_timescale"}, "timescale must be positive"))
	}

	// tracks
	trackRaw, ok := top["tracks"]
	if !ok {
		errs.Add(model.NewError(model.CodeManifestMissing, model.Loc{File: name, TrackIndex: -1, Field: "tracks"}, "manifest is missing field"))
		return m, errs
	}
	var tracks []json.RawMessage
	if err := json.Unmarshal(trackRaw, &tracks); err != nil {
		errs.Add(model.NewError(model.CodeManifestSyntax, model.Loc{File: name, TrackIndex: -1, Field: "tracks"}, "expected array").Wrap(err))
		return m, errs
	}
	if len(tracks) == 0 {
		errs.Add(model.NewError(model.CodeManifestInvalid, model.Loc{File: name, TrackIndex: -1, Field: "tracks"}, "manifest has no tracks"))
	}
	seen := map[uint32]int{}
	for i, rawTrack := range tracks {
		t, terr := parseTrack(rawTrack, name, i)
		if terr != nil {
			if list, ok := terr.(model.ErrorList); ok {
				for _, e := range list {
					errs.Add(e)
				}
			} else if me, ok := terr.(*model.Error); ok {
				errs.Add(me)
			} else {
				errs.Add(model.NewError(model.CodeManifestSyntax, model.Loc{File: name, TrackIndex: i}, "invalid track").Wrap(terr))
			}
		}
		if t == nil {
			pos++
			continue
		}
		if prev, dup := seen[t.ID]; dup {
			errs.Add(model.NewError(model.CodeManifestDuplicate, model.Loc{File: name, TrackID: t.ID, TrackIndex: i, Field: "id"}, "track id duplicates track %d", prev))
		}
		seen[t.ID] = i
		t.path = name
		t.index = pos
		m.Tracks = append(m.Tracks, *t)
		pos++
	}
	if len(errs) > 0 {
		return m, errs
	}
	return m, nil
}
