// Package index reconciles an externally supplied keyframe index against the
// keyframes discovered by scanning the elementary stream. The index is never
// trusted: every entry is matched within a configurable tick tolerance, and
// hits, misses (real keyframes the index omitted) and false positives (index
// entries pointing at no keyframe) are reported with their byte offsets.
package index

import (
	"sort"

	mediaerr "mediacheck/internal/errors"
	"mediacheck/internal/timeline"
)

// Hard caps keep pathological index files from exhausting memory.
const (
	MaxIndexEntries       = 1_000_000
	DefaultToleranceTicks = 2
)

// Entry is one external keyframe index record for a track. TS is expressed
// in the index's own time base and preserved verbatim as OriginalTS.
type Entry struct {
	Track      string            `json:"track"`
	TS         int64             `json:"ts"`
	Base       timeline.TimeBase `json:"timebase"`
	ByteOffset int64             `json:"byte_offset"`
}

// Keyframe is a keyframe discovered by the stream scanner, in the track's
// canonical analysis time base.
type Keyframe struct {
	TS     int64
	Offset int64
}

// Config controls reconciliation.
type Config struct {
	// ToleranceTicks is the maximum absolute tick difference (in the
	// analysis time base) for an index entry to count as a hit.
	ToleranceTicks int64
	// MaxEntries bounds the accepted index size.
	MaxEntries int
}

// Validate applies defaults and hard caps.
func (c *Config) Validate() error {
	if c.MaxEntries == 0 {
		c.MaxEntries = MaxIndexEntries
	}
	if c.MaxEntries < 0 || c.MaxEntries > MaxIndexEntries {
		return mediaerr.NewCoded(mediaerr.StageIndex, "", "index.config",
			"max entries %d outside cap %d", c.MaxEntries, MaxIndexEntries)
	}
	if c.ToleranceTicks < 0 {
		return mediaerr.NewCoded(mediaerr.StageIndex, "", "index.config",
			"tolerance must be non-negative, got %d", c.ToleranceTicks)
	}
	if c.ToleranceTicks == 0 {
		c.ToleranceTicks = DefaultToleranceTicks
	}
	return nil
}

// Kind classifies a reconciliation record.
type Kind string

const (
	KindHit   Kind = "hit"
	KindMiss  Kind = "miss"           // keyframe present in stream, absent from index
	KindFalse Kind = "false-positive" // index entry with no keyframe
)

// Record is one matched or unmatched reconciliation outcome. Offset fields
// let the report point at the exact bytes involved.
type Record struct {
	Kind       Kind  `json:"kind"`
	StreamTS   int64 `json:"stream_ts,omitempty"`
	IndexTS    int64 `json:"index_ts,omitempty"`
	StreamOff  int64 `json:"stream_offset,omitempty"`
	IndexOff   int64 `json:"index_offset,omitempty"`
	DeltaTicks int64 `json:"delta_ticks,omitempty"`
}

// Report is the full reconciliation result for one track.
type Report struct {
	Track          string   `json:"track"`
	Hits           int      `json:"hits"`
	Misses         int      `json:"misses"`
	FalsePositives int      `json:"false_positives"`
	HitRate        float64  `json:"hit_rate"`
	Records        []Record `json:"records"`
}

// Reconcile matches sorted external entries against sorted scanned keyframes
// in the analysis time base. Index timestamps are rescaled from their own
// time base; a rescale overflow yields a structured index error.
func Reconcile(track string, entries []Entry, keyframes []Keyframe,
	analysisBase timeline.TimeBase, cfg Config) (*Report, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if len(entries) > cfg.MaxEntries {
		return nil, mediaerr.NewCoded(mediaerr.StageIndex, track, "index.too-large",
			"%d entries exceed cap %d", len(entries), cfg.MaxEntries)
	}
	rep := &Report{Track: track}

	// Two-pointer match after projecting every index timestamp into the
	// analysis time base. Sort defensively so callers need not pre-sort.
	scanned := make([]Keyframe, len(keyframes))
	copy(scanned, keyframes)
	sort.Slice(scanned, func(i, j int) bool { return scanned[i].TS < scanned[j].TS })

	proj := make([]Entry, len(entries))
	copy(proj, entries)
	for i := range proj {
		st, err := timeline.Rescale(proj[i].TS, proj[i].Base, analysisBase)
		if err != nil {
			return nil, mediaerr.Wrap(err, mediaerr.StageIndex, track,
				"index entry %d timestamp rescale failed", i)
		}
		proj[i].TS = st
	}
	sort.Slice(proj, func(i, j int) bool { return proj[i].TS < proj[j].TS })

	usedKF := make([]bool, len(scanned))
	usedIdx := make([]bool, len(proj))

	// Greedy nearest match: for each index entry pick the closest unused
	// keyframe inside the tolerance window.
	for ie, e := range proj {
		best := -1
		var bestDelta int64 = 1 << 62
		for ki, kf := range scanned {
			if usedKF[ki] {
				continue
			}
			d := abs64(kf.TS - e.TS)
			if d <= cfg.ToleranceTicks && d < bestDelta {
				best = ki
				bestDelta = d
			}
		}
		if best < 0 {
			rep.Records = append(rep.Records, Record{
				Kind: KindFalse, IndexTS: e.TS, IndexOff: e.ByteOffset,
			})
			rep.FalsePositives++
			continue
		}
		usedKF[best] = true
		usedIdx[ie] = true
		rep.Records = append(rep.Records, Record{
			Kind: KindHit, StreamTS: scanned[best].TS, IndexTS: e.TS,
			StreamOff: scanned[best].Offset, IndexOff: e.ByteOffset,
			DeltaTicks: scanned[best].TS - e.TS,
		})
		rep.Hits++
	}
	for ki, kf := range scanned {
		if !usedKF[ki] {
			rep.Records = append(rep.Records, Record{
				Kind: KindMiss, StreamTS: kf.TS, StreamOff: kf.Offset,
			})
			rep.Misses++
		}
	}

	// Hit rate is defined against real keyframes: how many the index found.
	if len(scanned) > 0 {
		rep.HitRate = float64(rep.Hits) / float64(len(scanned))
	} else if len(proj) == 0 {
		rep.HitRate = 1.0 // empty index over an empty stream is a perfect match
	} else {
		rep.HitRate = 0.0
	}
	return rep, nil
}

func abs64(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}
