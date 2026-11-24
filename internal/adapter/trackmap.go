package adapter

import (
	"fmt"
	"sort"

	"mediad/internal/media"
)

// MappedTrack binds a source track to its deterministic output slot.
type MappedTrack struct {
	OutputID int    // 1-based, stable across runs
	Label    string // e.g. "video/und/avc1+3f2a#0"
	Track    *Track
}

// MapTracks assigns output slots to every track of every input stream.
//
// Rules, applied in a fully deterministic order:
//   - tracks sort by (type rank, language, codec key, source, track id), so
//     duplicate languages and multiple same-type tracks get stable slots;
//   - tracks with missing codec parameters keep their raw bytes and are
//     labelled with the "raw" codec key rather than being dropped;
//   - duplicate (type, language, codec) groups are suffixed #0, #1, ...
//
// The mapping is a pure function of the inputs: identical manifests always
// reproduce identical output IDs.
func MapTracks(streams ...*Stream) ([]MappedTrack, error) {
	var all []*Track
	for _, st := range streams {
		all = append(all, st.Tracks...)
	}
	if len(all) == 0 {
		return nil, media.New(media.PhaseMap, -1, -1, "no tracks to map", nil)
	}
	sort.SliceStable(all, func(i, j int) bool {
		a, b := all[i], all[j]
		if ra, rb := typeRank(a.Type), typeRank(b.Type); ra != rb {
			return ra < rb
		}
		if a.Language != b.Language {
			return a.Language < b.Language
		}
		if ka, kb := CodecKey(a), CodecKey(b); ka != kb {
			return ka < kb
		}
		if a.Source != b.Source {
			return a.Source < b.Source
		}
		return a.ID < b.ID
	})

	out := make([]MappedTrack, 0, len(all))
	groupCount := map[string]int{}
	for i, t := range all {
		group := fmt.Sprintf("%s/%s/%s", t.Type, langOr(t.Language, "und"), CodecKey(t))
		n := groupCount[group]
		groupCount[group] = n + 1
		out = append(out, MappedTrack{
			OutputID: i + 1,
			Label:    fmt.Sprintf("%s#%d", group, n),
			Track:    t,
		})
	}
	return out, nil
}

func typeRank(t media.TrackType) int {
	switch t {
	case media.TrackVideo:
		return 0
	case media.TrackAudio:
		return 1
	default:
		return 2
	}
}

func langOr(s, def string) string {
	if s == "" {
		return def
	}
	return s
}
