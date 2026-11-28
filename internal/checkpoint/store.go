// Package checkpoint persists completed work so an interrupted run resumes
// without recomputing finished intervals.
package checkpoint

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"mediaregress/internal/fingerprint"
	"mediaregress/internal/media"
	"mediaregress/internal/silence"
	"mediaregress/internal/split"
)

// FormatVersion is bumped whenever the on-disk layout changes incompatibly.
const FormatVersion = 1

// Interval is a completed nanosecond span on one track.
type Interval struct {
	Start media.Ticks `json:"start_ns"`
	End   media.Ticks `json:"end_ns"`
}

// TrackState is the resumable state of one track lane.
type TrackState struct {
	TrackID     string `json:"track"`
	InputDigest string `json:"input_digest"`
	// CompletedStages lists stages whose results are durable for Intervals.
	CompletedStages []media.Stage `json:"completed_stages"`
	Intervals       []Interval    `json:"completed_intervals"`
	// Segments are the durable split results for the completed intervals.
	Segments []split.Segment `json:"segments,omitempty"`
	Silence  []silence.Range `json:"silence_ranges,omitempty"`
	// Fingerprints are the durable per-segment results; only segments fully
	// inside a completed interval are persisted.
	Fingerprints []fingerprint.Result `json:"fingerprints,omitempty"`
	OutputDigest string               `json:"output_digest,omitempty"`
}

// State is the whole checkpoint document.
type State struct {
	Version     int         `json:"version"`
	InputPath   string      `json:"input_path"`
	InputDigest string      `json:"input_digest"`
	InputSize   int64       `json:"input_size"`
	Stage       media.Stage `json:"stage"`
	// ConfigDigest covers every option that changes the produced document.
	// A run with different options must not reuse results computed under the
	// old ones, even when the input bytes are identical.
	ConfigDigest string       `json:"config_digest,omitempty"`
	CreatedUnix  int64        `json:"created_unix"`
	UpdatedUnix  int64        `json:"updated_unix"`
	Tracks       []TrackState `json:"tracks"`
	Resumed      bool         `json:"resumed,omitempty"`
}

// Store writes checkpoint state atomically under a directory.
type Store struct {
	dir string
}

// NewStore returns a store rooted at dir. The directory is created lazily so a
// dry run leaves no artifacts.
func NewStore(dir string) *Store { return &Store{dir: dir} }

// Dir returns the backing directory.
func (s *Store) Dir() string { return s.dir }

func (s *Store) path(name string) string { return filepath.Join(s.dir, name) }

// Load reads the state file. A missing file is not an error: it yields a nil
// state so the caller can start fresh. A corrupt file is an error with the
// checkpoint class so the CLI can exit with the checkpoint code.
func (s *Store) Load() (*State, error) {
	b, err := os.ReadFile(s.path("state.json"))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, media.WrapError(media.ClassCheckpoint, media.StageCheckpoint, "", 0,
			"read checkpoint", err)
	}
	var st State
	if err := json.Unmarshal(b, &st); err != nil {
		return nil, media.WrapError(media.ClassCheckpoint, media.StageCheckpoint, "", 0,
			"decode checkpoint", err)
	}
	if st.Version != FormatVersion {
		return nil, media.Errorf(media.ClassCheckpoint, media.StageCheckpoint, "", 0,
			"checkpoint format %d, want %d", st.Version, FormatVersion)
	}
	return &st, nil
}

// Save writes the state atomically: write a temp file, sync, rename. A crash
// mid-write therefore leaves the previous checkpoint intact.
func (s *Store) Save(st *State) error {
	if st == nil {
		return media.NewError(media.ClassCheckpoint, media.StageCheckpoint, "", 0, "nil checkpoint state")
	}
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		return media.WrapError(media.ClassCheckpoint, media.StageCheckpoint, "", 0, "create checkpoint dir", err)
	}
	now := time.Now().Unix()
	st.UpdatedUnix = now
	if st.CreatedUnix == 0 {
		st.CreatedUnix = now
	}
	st.Version = FormatVersion
	b, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return media.WrapError(media.ClassCheckpoint, media.StageCheckpoint, "", 0, "encode checkpoint", err)
	}
	tmp, err := os.CreateTemp(s.dir, "state-*.tmp")
	if err != nil {
		return media.WrapError(media.ClassCheckpoint, media.StageCheckpoint, "", 0, "create temp checkpoint", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(append(b, '\n')); err != nil {
		tmp.Close()
		return media.WrapError(media.ClassCheckpoint, media.StageCheckpoint, "", 0, "write temp checkpoint", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return media.WrapError(media.ClassCheckpoint, media.StageCheckpoint, "", 0, "sync temp checkpoint", err)
	}
	if err := tmp.Close(); err != nil {
		return media.WrapError(media.ClassCheckpoint, media.StageCheckpoint, "", 0, "close temp checkpoint", err)
	}
	if err := os.Rename(tmpName, s.path("state.json")); err != nil {
		return media.WrapError(media.ClassCheckpoint, media.StageCheckpoint, "", 0, "publish checkpoint", err)
	}
	return nil
}

// Track returns the state for a track ID, or nil.
func (st *State) Track(id string) *TrackState {
	for i := range st.Tracks {
		if st.Tracks[i].TrackID == id {
			return &st.Tracks[i]
		}
	}
	return nil
}

// EnsureTrack adds an empty state for a track if absent.
func (st *State) EnsureTrack(id, digest string) *TrackState {
	if t := st.Track(id); t != nil {
		return t
	}
	st.Tracks = append(st.Tracks, TrackState{TrackID: id, InputDigest: digest})
	sort.Slice(st.Tracks, func(i, j int) bool { return st.Tracks[i].TrackID < st.Tracks[j].TrackID })
	return st.Track(id)
}

// Normalize sorts tracks and intervals so two logically equal runs serialise
// identically. Without this, map iteration order would leak into the output.
func (st *State) Normalize() {
	sort.Slice(st.Tracks, func(i, j int) bool { return st.Tracks[i].TrackID < st.Tracks[j].TrackID })
	for i := range st.Tracks {
		t := &st.Tracks[i]
		sort.Slice(t.Intervals, func(a, b int) bool {
			if t.Intervals[a].Start != t.Intervals[b].Start {
				return t.Intervals[a].Start < t.Intervals[b].Start
			}
			return t.Intervals[a].End < t.Intervals[b].End
		})
		t.Intervals = mergeIntervals(t.Intervals)
		sort.Slice(t.Segments, func(a, b int) bool { return t.Segments[a].ID < t.Segments[b].ID })
		sort.Slice(t.Fingerprints, func(a, b int) bool {
			return t.Fingerprints[a].SegmentID < t.Fingerprints[b].SegmentID
		})
		sort.Slice(t.Silence, func(a, b int) bool {
			return t.Silence[a].Window.Start < t.Silence[b].Window.Start
		})
		sort.Slice(t.CompletedStages, func(a, b int) bool {
			return t.CompletedStages[a] < t.CompletedStages[b]
		})
	}
}

// mergeIntervals coalesces touching or overlapping intervals.
func mergeIntervals(in []Interval) []Interval {
	if len(in) == 0 {
		return nil
	}
	out := []Interval{in[0]}
	for _, cur := range in[1:] {
		last := &out[len(out)-1]
		if cur.Start <= last.End {
			if cur.End > last.End {
				last.End = cur.End
			}
			continue
		}
		out = append(out, cur)
	}
	return out
}

// Completed reports whether [start,end) is fully covered by the track's
// completed intervals.
func (t *TrackState) Completed(start, end media.Ticks) bool {
	if end <= start {
		return false
	}
	for _, iv := range t.Intervals {
		if iv.Start <= start && end <= iv.End {
			return true
		}
	}
	return false
}

// AddInterval records a completed span and coalesces. Sorting happens before
// merging because an interrupted run may report spans in any order.
func (t *TrackState) AddInterval(start, end media.Ticks) {
	t.Intervals = append(t.Intervals, Interval{Start: start, End: end})
	sort.Slice(t.Intervals, func(a, b int) bool {
		if t.Intervals[a].Start != t.Intervals[b].Start {
			return t.Intervals[a].Start < t.Intervals[b].Start
		}
		return t.Intervals[a].End < t.Intervals[b].End
	})
	t.Intervals = mergeIntervals(t.Intervals)
}

// AddStage records a completed stage idempotently.
func (t *TrackState) AddStage(stage media.Stage) {
	for _, s := range t.CompletedStages {
		if s == stage {
			return
		}
	}
	t.CompletedStages = append(t.CompletedStages, stage)
	sort.Slice(t.CompletedStages, func(a, b int) bool { return t.CompletedStages[a] < t.CompletedStages[b] })
}

// HasStage reports whether a stage completed for the whole track.
func (t *TrackState) HasStage(stage media.Stage) bool {
	for _, s := range t.CompletedStages {
		if s == stage {
			return true
		}
	}
	return false
}

// Invalidated returns tracks whose stored input digest no longer matches.
// Only these tracks have their indexes and downstream results discarded.
func (st *State) Invalidated(current map[string]string) []string {
	var out []string
	for i := range st.Tracks {
		cur, ok := current[st.Tracks[i].TrackID]
		if !ok || st.Tracks[i].InputDigest != cur {
			out = append(out, st.Tracks[i].TrackID)
		}
	}
	sort.Strings(out)
	return out
}

// ResetTrack clears a single track's indexes and downstream results, leaving
// every other track untouched.
func (st *State) ResetTrack(id string) {
	for i := range st.Tracks {
		if st.Tracks[i].TrackID == id {
			st.Tracks[i].Intervals = nil
			st.Tracks[i].Segments = nil
			st.Tracks[i].Silence = nil
			st.Tracks[i].Fingerprints = nil
			st.Tracks[i].CompletedStages = nil
			st.Tracks[i].OutputDigest = ""
			return
		}
	}
}

// ApplyDigests records the current per-track digests. Tracks whose digest
// changed are reset; new tracks are added.
func (st *State) ApplyDigests(current map[string]string) (reset []string) {
	for id, digest := range current {
		t := st.EnsureTrack(id, "")
		if t.InputDigest != digest {
			if t.InputDigest != "" {
				reset = append(reset, id)
			}
			t.InputDigest = digest
			st.ResetTrack(id)
			st.EnsureTrack(id, digest).InputDigest = digest
		}
	}
	sort.Strings(reset)
	return reset
}

// String is a short debugging form.
func (st *State) String() string {
	return fmt.Sprintf("checkpoint{v%d tracks=%d stage=%s}", st.Version, len(st.Tracks), st.Stage)
}
