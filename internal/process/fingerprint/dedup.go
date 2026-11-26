package fingerprint

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"

	"mediaprep/internal/source"
)

// Ref preserves where one identical byte fingerprint came from.
type Ref struct {
	Path  string `json:"path"`
	Track string `json:"track"`
	Role  string `json:"role"`
	Index int    `json:"sample_count"`
}

type Group struct {
	Fingerprint string `json:"fingerprint"`
	Role        string `json:"role"`
	Size        int    `json:"size"`
	Refs        []Ref  `json:"refs"`
	DuplicateOf string `json:"duplicate_of,omitempty"`
}

// TrackFingerprint hashes the track role and the concatenation of sample
// payloads in sample-table order. Metadata/provenance is deliberately absent.
func TrackFingerprint(track *source.Track) (string, int) {
	h := sha256.New()
	_, _ = h.Write([]byte(track.Role))
	_, _ = h.Write([]byte{0}) // unambiguous role/body separator
	size := 0
	for _, sample := range track.Samples {
		_, _ = h.Write(sample.Payload)
		size += len(sample.Payload)
	}
	return hex.EncodeToString(h.Sum(nil)), size
}

type SourcedTrack struct {
	Path  string
	Track *source.Track
}

// Dedup merges tracks with the same role + ordered sample bytes fingerprint.
// Every source reference is retained. The returned duplicate count is the
// number of extra references beyond the first in each merged group.
func Dedup(items []SourcedTrack) []Group {
	byHash := map[string]*Group{}
	var order []string
	for _, item := range items {
		track := item.Track
		fp, size := TrackFingerprint(track)
		g, ok := byHash[fp]
		if !ok {
			g = &Group{Fingerprint: fp, Role: track.Role, Size: size, DuplicateOf: track.Label}
			byHash[fp] = g
			order = append(order, fp)
		}
		g.Refs = append(g.Refs, Ref{
			Path: item.Path, Track: track.Label, Role: track.Role, Index: len(track.Samples),
		})
	}
	groups := make([]Group, 0, len(order))
	for _, fp := range order {
		g := byHash[fp]
		sort.SliceStable(g.Refs, func(i, j int) bool {
			if g.Refs[i].Path != g.Refs[j].Path {
				return g.Refs[i].Path < g.Refs[j].Path
			}
			return g.Refs[i].Track < g.Refs[j].Track
		})
		groups = append(groups, *g)
	}
	sort.SliceStable(groups, func(i, j int) bool { return groups[i].Fingerprint < groups[j].Fingerprint })
	return groups
}

func CountDuplicates(groups []Group) int {
	n := 0
	for _, g := range groups {
		if len(g.Refs) > 1 {
			n += len(g.Refs) - 1
		}
	}
	return n
}
