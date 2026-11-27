package manifest

import (
	"encoding/json"
	"sort"

	"mediacore/internal/clock"
	"mediacore/internal/fault"
)

// EncodeCurrent renders a manifest in the current schema. Each track uses the
// caller-supplied timescale, which must be large enough that rounding to a
// tick stays within one nanosecond of the source value.
func EncodeCurrent(m *Manifest, timescales map[string]int64) ([]byte, error) {
	trackNames := m.Tracks()
	sort.Strings(trackNames)
	doc := currentDoc{Version: int(VersionCurrent)}
	for _, name := range trackNames {
		ts, ok := timescales[name]
		if !ok || ts <= 0 {
			return nil, fault.Newf(fault.CodeManifestMalformed, "", "missing timescale for track %q", name)
		}
		doc.Tracks = append(doc.Tracks, currentTrack{Name: name, Timescale: ts})
	}
	for _, it := range m.Items {
		ts := timescales[it.Track]
		doc.Entries = append(doc.Entries, currentEntry{
			ID: it.ID, Codec: it.Codec, Path: it.Path, Track: it.Track,
			PTS:      it.PTS.Nanos() * ts / int64(clock.Second),
			Duration: it.Duration.Nanos() * ts / int64(clock.Second),
		})
	}
	return json.MarshalIndent(doc, "", "  ")
}
