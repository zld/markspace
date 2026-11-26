package index

import (
	"encoding/json"
	"fmt"
	"sort"
)

// JSONEntry is the on-disk representation of one sample. Rational times are
// serialised as whole microseconds, the resolution used by the delivery
// summary, so the file round-trips at that precision.
type JSONEntry struct {
	TrackID uint32 `json:"track_id"`
	Number  int    `json:"number"`
	DTS     int64  `json:"dts_us"`
	End     int64  `json:"end_us"`
	PTS     int64  `json:"pts_us"`
	Offset  int64  `json:"offset"`
	Size    uint32 `json:"size"`
	Key     bool   `json:"keyframe"`
}

// JSONTrack groups entries for one track.
type JSONTrack struct {
	TrackID   uint32      `json:"track_id"`
	Kind      string      `json:"kind"`
	Timescale uint32      `json:"timescale"`
	Entries   []JSONEntry `json:"entries"`
}

// JSONIndex is the published index document. The entry list is sorted by
// track and time, making the file byte-stable for identical input.
type JSONIndex struct {
	Version int         `json:"version"`
	Source  string      `json:"source"`
	Tracks  []JSONTrack `json:"tracks"`
}

// Marshal renders the index as indented JSON with a trailing newline.
func (idx *Index) Marshal(source string, timescales map[uint32]uint32) ([]byte, error) {
	doc := JSONIndex{Version: 1, Source: source}
	for _, t := range idx.Tracks {
		ts := uint32(0)
		if timescales != nil {
			ts = timescales[t.TrackID]
		}
		jt := JSONTrack{TrackID: t.TrackID, Kind: t.Kind.String(), Timescale: ts}
		entries := append([]Entry(nil), t.Entries...)
		sort.SliceStable(entries, func(i, j int) bool {
			if c := entries[i].DTS.Cmp(entries[j].DTS); c != 0 {
				return c < 0
			}
			return entries[i].Number < entries[j].Number
		})
		for _, e := range entries {
			jt.Entries = append(jt.Entries, JSONEntry{
				TrackID: e.TrackID, Number: e.Number,
				DTS: e.DTS.Micros(), End: e.End.Micros(), PTS: e.PTS.Micros(),
				Offset: e.Offset, Size: e.Size, Key: e.Keyframe,
			})
		}
		doc.Tracks = append(doc.Tracks, jt)
	}
	data, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("index marshal: %w", err)
	}
	return append(data, '\n'), nil
}

// ParseJSON decodes an index document back into entries. It is used by tests
// and by resume checks that must prove the written index is readable.
func ParseJSON(data []byte) (*JSONIndex, error) {
	var doc JSONIndex
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("index parse: %w", err)
	}
	if doc.Version != 1 {
		return nil, fmt.Errorf("index parse: unsupported version %d", doc.Version)
	}
	return &doc, nil
}

// CumulativeOffsets returns the input byte offset range covered by an index
// document, from the smallest sample offset to the largest inclusive end.
func (doc *JSONIndex) ByteExtent() (int64, int64, bool) {
	var lo, hi int64
	found := false
	for _, t := range doc.Tracks {
		for _, e := range t.Entries {
			end := e.Offset + int64(e.Size)
			if !found || e.Offset < lo {
				lo = e.Offset
			}
			if !found || end > hi {
				hi = end
			}
			found = true
		}
	}
	return lo, hi, found
}

// SampleCount counts entries in an index document.
func (doc *JSONIndex) SampleCount() int {
	n := 0
	for _, t := range doc.Tracks {
		n += len(t.Entries)
	}
	return n
}
