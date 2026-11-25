package index

import (
	"sort"

	"mediacore/internal/clock"
)

// Range is an event-time interval with left-closed/right-open semantics.
type Range struct {
	Start clock.EventTime `json:"start_ns"`
	End   clock.EventTime `json:"end_ns"`
}

// QueryResult contains the covering keyframe and in-range samples.
type QueryResult struct {
	IndexKey string        `json:"index_key"`
	Range    Range         `json:"range"`
	Keyframe *Keyframe     `json:"keyframe,omitempty"`
	Samples  []TimedSample `json:"samples"`
}

// Query returns the nearest preceding keyframe and samples whose event starts
// satisfy Start <= sample < End.
func (idx *Index) Query(r Range) (*QueryResult, error) {
	if idx == nil || idx.TimeMap == nil {
		return nil, clock.New(clock.CategoryTimebase, "", 0, 0, "index has no timebase")
	}
	if r.End <= r.Start {
		return nil, clock.New(clock.CategoryInvalidArgument, idx.Path, idx.Track, 0,
			"query range end must be after start")
	}
	if idx.TimeScale == 0 {
		return nil, clock.New(clock.CategoryTimebase, idx.Path, idx.Track, 0, "zero track timescale")
	}
	res := &QueryResult{IndexKey: idx.Key, Range: r}
	ki := sort.Search(len(idx.Keyframes), func(i int) bool {
		return idx.Keyframes[i].EventStart > r.Start
	}) - 1
	if ki < 0 {
		return nil, clock.New(clock.CategoryOutOfRange, idx.Path, idx.Track, 0,
			"no preceding random-access keyframe covers query start")
	}
	res.Keyframe = &idx.Keyframes[ki]
	first := sort.Search(len(idx.Samples), func(i int) bool {
		return idx.Samples[i].EventStart >= r.Start
	})
	for i := first; i < len(idx.Samples); i++ {
		s := idx.Samples[i]
		if s.EventStart >= r.End {
			break
		}
		res.Samples = append(res.Samples, s)
	}
	return res, nil
}

// QueryAll queries several tracks on the shared event timeline.
func QueryAll(indexes []*Index, r Range) (map[string]*QueryResult, error) {
	if len(indexes) == 0 {
		return nil, clock.New(clock.CategoryMissingTrack, "", 0, 0, "no indexes to query")
	}
	out := make(map[string]*QueryResult, len(indexes))
	for _, idx := range indexes {
		if idx == nil {
			return nil, clock.New(clock.CategoryMissingTrack, "", 0, 0, "nil index in multi-track query")
		}
		got, err := idx.Query(r)
		if err != nil {
			return nil, err
		}
		out[idx.Key] = got
	}
	return out, nil
}

// QueryNamed enforces requested-track presence before querying.
func QueryNamed(all map[string]*Index, names []string, r Range) (map[string]*QueryResult, error) {
	out := make(map[string]*QueryResult, len(names))
	for _, name := range names {
		idx, ok := all[name]
		if !ok {
			return nil, clock.New(clock.CategoryMissingTrack, name, 0, 0, "requested track is absent from manifest/index set")
		}
		got, err := idx.Query(r)
		if err != nil {
			return nil, err
		}
		out[name] = got
	}
	return out, nil
}

// CheckTimebases ensures every index maps to the same nanosecond event basis.
// Different positive track timescales are expected and converted by TimeMap.
func CheckTimebases(indexes []*Index) error {
	for _, idx := range indexes {
		if idx == nil || idx.TimeMap == nil || idx.TimeScale == 0 {
			path, track := "", 0
			if idx != nil {
				path, track = idx.Path, idx.Track
			}
			return clock.New(clock.CategoryTimebase, path, track, 0,
				"track lacks a compatible common event timebase")
		}
	}
	return nil
}
