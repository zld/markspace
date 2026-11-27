package asset

import "sort"

// Issue codes are stable, machine-readable findings from version mapping.
const (
	IssueGap      = "ASSET_GAP"       // interval with no covering version
	IssueConflict = "ASSET_CONFLICT"  // same interval, different content digest
	IssueTruncated = "ASSET_TRUNCATED" // fragment parse failed (e.g. cut short)
)

// Interval is a closed-open [StartNS, EndNS) range on the unified time base.
type Interval struct {
	StartNS int64 `json:"start_ns"`
	EndNS   int64 `json:"end_ns"`
}

// Version binds one concrete asset rendition to an interval with a digest.
type Version struct {
	ProgramKey string   `json:"program_key"`
	TrackID    string   `json:"track_id"`
	Interval   Interval `json:"interval"`
	Source     string   `json:"source"`      // relative path of the segment
	Digest     string   `json:"digest"`      // sha256 of fragment content
	Priority   int      `json:"priority"`    // explicit tie-break rule; higher wins
	Sequence   int64    `json:"sequence"`
	Frames     int      `json:"frames"`
}

// Issue is one gap/conflict finding with a stable code and location.
type Issue struct {
	Code       string   `json:"code"`
	ProgramKey string   `json:"program_key"`
	TrackID    string   `json:"track_id"`
	Interval   Interval `json:"interval"`
	Detail     string   `json:"detail"`
}

// TrackMap holds every known version of one track plus the expected span.
type TrackMap struct {
	ProgramKey string    `json:"program_key"`
	TrackID    string    `json:"track_id"`
	Expected   Interval  `json:"expected"`
	Versions   []Version `json:"versions"`
}

// Map is the full per-program version registry.
type Map struct {
	Programs map[string]map[string]*TrackMap `json:"programs"`
}

// NewMap creates an empty registry.
func NewMap() *Map {
	return &Map{Programs: map[string]map[string]*TrackMap{}}
}

// Register adds a version, creating the track bucket on demand.
func (m *Map) Register(v Version) {
	tracks, ok := m.Programs[v.ProgramKey]
	if !ok {
		tracks = map[string]*TrackMap{}
		m.Programs[v.ProgramKey] = tracks
	}
	tm, ok := tracks[v.TrackID]
	if !ok {
		tm = &TrackMap{ProgramKey: v.ProgramKey, TrackID: v.TrackID}
		tracks[v.TrackID] = tm
	}
	tm.Versions = append(tm.Versions, v)
	if v.Interval.StartNS < tm.Expected.StartNS || len(tm.Versions) == 1 {
		tm.Expected.StartNS = v.Interval.StartNS
	}
	if v.Interval.EndNS > tm.Expected.EndNS {
		tm.Expected.EndNS = v.Interval.EndNS
	}
}

// sortedVersions returns versions ordered by interval start, then priority.
func sortedVersions(vs []Version) []Version {
	out := append([]Version(nil), vs...)
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Interval.StartNS != out[j].Interval.StartNS {
			return out[i].Interval.StartNS < out[j].Interval.StartNS
		}
		if out[i].Priority != out[j].Priority {
			return out[i].Priority > out[j].Priority
		}
		return out[i].Source < out[j].Source
	})
	return out
}

func overlap(a, b Interval) bool {
	return a.StartNS < b.EndNS && b.StartNS < a.EndNS
}
