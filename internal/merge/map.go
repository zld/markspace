// Package merge maps samples from multiple versions onto one timeline by
// track and rational time, comparing content fingerprints. It records every
// input/output offset, time and byte difference, lists missing versions per
// position, and flags positions whose content disagrees.
package merge

import (
	"fmt"
	"sort"

	"mediarc/internal/model"
)

// Sample is one repaired, fingerprinted sample from one version.
type Sample struct {
	Version   string
	Track     int
	Ticks     int64 // corrected PTS on Scale
	Scale     int64
	SrcOffset int64
	Length    int64
	Hash      string // content fingerprint
}

// Entry records how one version's sample maps into the merged output.
type Entry struct {
	Version  string `json:"version"`
	InOffset int64  `json:"in_offset"`
	OutOff   int64  `json:"out_offset"` // archive offset, -1 when not committed
	Dt       int64  `json:"dt"`         // ticks vs chosen sample, on the item scale
	ByteDiff int64  `json:"byte_diff"`  // length vs chosen sample
	Hash     string `json:"hash"`
}

// Item is one mapping position: a (track, time) point across versions.
type Item struct {
	Track    int      `json:"track"`
	Ticks    int64    `json:"ticks"`
	Scale    int64    `json:"scale"`
	Chosen   string   `json:"chosen"`
	Conflict bool     `json:"conflict"`
	Entries  []Entry  `json:"entries"`
	Missing  []string `json:"missing,omitempty"`
}

// Result summarises a merge run.
type Result struct {
	Items     []Item
	Mapped    int
	Missing   int
	Conflicts int
}

// key is a normalized (track, rational time) mapping position.
type key struct {
	track int
	num   int64
	den   int64
}

func norm(t model.Time) (int64, int64) {
	g := gcd(abs(t.Ticks), t.Scale)
	return t.Ticks / g, t.Scale / g
}

func abs(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}

func gcd(a, b int64) int64 {
	for b != 0 {
		a, b = b, a%b
	}
	if a == 0 {
		return 1
	}
	return a
}

// Map groups samples by (track, normalized time). The chosen version is the
// lexicographically smallest present; chosen entries receive sequential
// output offsets in item order. Positions with differing fingerprints are
// flagged as conflicts; versions absent from a position are listed missing.
func Map(samples []Sample, versions []string) *Result {
	groups := make(map[key][]Sample)
	for _, s := range samples {
		n, d := norm(model.Time{Ticks: s.Ticks, Scale: s.Scale})
		k := key{s.Track, n, d}
		groups[k] = append(groups[k], s)
	}
	keys := make([]key, 0, len(groups))
	for k := range groups {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].track != keys[j].track {
			return keys[i].track < keys[j].track
		}
		li, ri := keys[i], keys[j]
		l := model.Time{Ticks: li.num, Scale: li.den}
		return l.Cmp(model.Time{Ticks: ri.num, Scale: ri.den}) < 0
	})
	res := &Result{}
	var outOff int64
	for _, k := range keys {
		gs := groups[k]
		sort.Slice(gs, func(i, j int) bool { return gs[i].Version < gs[j].Version })
		present := make(map[string]Sample, len(gs))
		for _, s := range gs {
			if _, dup := present[s.Version]; !dup {
				present[s.Version] = s
			}
		}
		chosen := present[sortedFirst(present)]
		item := Item{Track: k.track, Ticks: k.num, Scale: k.den, Chosen: chosen.Version}
		for _, v := range versions {
			s, ok := present[v]
			if !ok {
				item.Missing = append(item.Missing, v)
				res.Missing++
				continue
			}
			e := Entry{Version: v, InOffset: s.SrcOffset, OutOff: -1, Hash: s.Hash}
			if v == chosen.Version {
				e.OutOff = outOff
				outOff += s.Length
			} else {
				ct, err := model.Time{Ticks: chosen.Ticks, Scale: chosen.Scale}.Convert(item.Scale)
				st, err2 := model.Time{Ticks: s.Ticks, Scale: s.Scale}.Convert(item.Scale)
				if err == nil && err2 == nil {
					e.Dt = st.Ticks - ct.Ticks
				}
				e.ByteDiff = s.Length - chosen.Length
				if s.Hash != chosen.Hash {
					item.Conflict = true
				}
			}
			item.Entries = append(item.Entries, e)
		}
		if item.Conflict {
			res.Conflicts++
		}
		res.Items = append(res.Items, item)
	}
	res.Mapped = len(res.Items)
	return res
}

func sortedFirst(m map[string]Sample) string {
	first := ""
	for v := range m {
		if first == "" || v < first {
			first = v
		}
	}
	return first
}

// String renders a one-line summary for terminal output.
func (r *Result) String() string {
	return fmt.Sprintf("mapped=%d missing=%d conflicts=%d", r.Mapped, r.Missing, r.Conflicts)
}
