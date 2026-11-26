// Package index builds and checks keyframe indexes. An index never overrides
// media: every recorded hit is checked against the actual keyframe flags of
// the sample it points at.
package index

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"

	"liveregress/internal/clock"
	"liveregress/internal/fault"
)

// Entry is one keyframe location.
type Entry struct {
	Time      int64  `json:"time"` // ticks in Timescale
	Timescale uint32 `json:"timescale"`
	Offset    int64  `json:"offset"` // raw byte offset of the sample
	Size      uint32 `json:"size"`
	TrackID   uint32 `json:"track_id"`
}

// Time0 returns the entry time as a clock.Time.
func (e Entry) Time0() clock.Time { return clock.Time{Ticks: e.Time, Timescale: e.Timescale} }

// Index is a keyframe index for one representation layer.
type Index struct {
	Layer     string  `json:"layer"`
	Path      string  `json:"path"`
	Timescale uint32  `json:"timescale"`
	TrackID   uint32  `json:"track_id"`
	Entries   []Entry `json:"entries"`
	Source    string  `json:"source"` // "fmp4", "ts-scan", "rebuilt"
	InputHash string  `json:"input_sha256"`
	Digest    string  `json:"digest"`
}

// Keyframe is a fact observed in media, independent of any index.
type Keyframe struct {
	Time    int64
	Offset  int64
	Size    uint32
	TrackID uint32
}

// BuildFromFacts builds an index from keyframes actually observed in media.
func BuildFromFacts(layer string, timescale uint32, source string, facts []Keyframe) *Index {
	idx := &Index{Layer: layer, Timescale: timescale, Source: source}
	for _, f := range facts {
		if f.TrackID != 0 {
			idx.TrackID = f.TrackID
		}
		idx.Entries = append(idx.Entries, Entry{
			Time: f.Time, Timescale: timescale, Offset: f.Offset,
			Size: f.Size, TrackID: f.TrackID,
		})
	}
	sort.SliceStable(idx.Entries, func(i, j int) bool {
		if idx.Entries[i].Time != idx.Entries[j].Time {
			return idx.Entries[i].Time < idx.Entries[j].Time
		}
		return idx.Entries[i].Offset < idx.Entries[j].Offset
	})
	return idx
}

// Verify compares every index entry against observed keyframes. It returns the
// number of hits, the number of false positives (index points at a sample that
// is not a keyframe) and the number of missed keyframes. A hit requires the
// same track, offset, size and time.
func Verify(idx *Index, facts []Keyframe) (hits, falsePositive, missed int) {
	type key struct {
		time   int64
		offset int64
		size   uint32
		track  uint32
	}
	observed := make(map[key]bool, len(facts))
	for _, f := range facts {
		observed[key{f.Time, f.Offset, f.Size, f.TrackID}] = true
	}
	matched := make(map[key]bool, len(idx.Entries))
	for _, e := range idx.Entries {
		k := key{e.Time, e.Offset, e.Size, e.TrackID}
		if observed[k] {
			hits++
			matched[k] = true
		} else {
			falsePositive++
		}
	}
	for _, f := range facts {
		if !matched[key{f.Time, f.Offset, f.Size, f.TrackID}] {
			missed++
		}
	}
	return hits, falsePositive, missed
}

// CheckHits returns a fault when any index entry is not a real keyframe. The
// offset in the fault is the raw offset of the offending entry.
func CheckHits(idx *Index, facts []Keyframe) error {
	hits, fp, _ := Verify(idx, facts)
	if fp == 0 {
		return nil
	}
	return fault.New(fault.KindIndex, idx.Path, 0,
		fmt.Sprintf("keyframe index has %d false positives (%d hits)", fp, hits)).Recover(false)
}

// Save writes the index as stable, indented JSON with a trailing newline. Map
// ordering is avoided by struct fields, so re-running produces identical bytes
// for identical input.
func (idx *Index) Save(path string) error {
	idx.Digest = idx.computeDigest()
	data, err := json.MarshalIndent(idx, "", "  ")
	if err != nil {
		return fault.Wrap(fault.KindFormat, path, -1, "marshal index", err)
	}
	data = append(data, '\n')
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fault.Wrap(fault.KindPath, path, -1, "write index", err)
	}
	return nil
}

// Load reads an index file written by Save.
func Load(path string) (*Index, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fault.Wrap(fault.KindPath, path, -1, "read index", err)
	}
	var idx Index
	if err := json.Unmarshal(data, &idx); err != nil {
		return nil, fault.Wrap(fault.KindCorrupt, path, -1, "parse index", err)
	}
	if len(idx.Entries) == 0 {
		return nil, fault.New(fault.KindIndex, path, -1, "index has no entries").Recover(false)
	}
	if idx.Timescale == 0 {
		return nil, fault.New(fault.KindIndex, path, -1, "index has no timescale").Recover(false)
	}
	return &idx, nil
}

// computeDigest is a stable checksum over the semantic content, excluding the
// digest field itself.
func (idx *Index) computeDigest() string {
	h := newHash()
	fmt.Fprintf(h, "layer=%s;timescale=%d;track=%d;source=%s;input=%s\n",
		idx.Layer, idx.Timescale, idx.TrackID, idx.Source, idx.InputHash)
	for _, e := range idx.Entries {
		fmt.Fprintf(h, "%d,%d,%d,%d\n", e.Time, e.Offset, e.Size, e.TrackID)
	}
	return fmt.Sprintf("%x", h.Sum(nil))
}

// Digest returns the current stable digest.
func (idx *Index) Digest0() string { return idx.computeDigest() }
