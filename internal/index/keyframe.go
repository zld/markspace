// Package index builds seekable keyframe indexes and MP4 index metadata.
package index

import (
	"sort"

	"medpipe/internal/fault"
	"medpipe/internal/ingest"
)

// Keyframe is one seek point. FragmentHash binds the index entry to the
// fragment/frame checksum produced during ingest or rendering.
type Keyframe struct {
	TrackID      uint16   `json:"track_id"`
	TimestampNS  int64    `json:"timestamp_ns"`
	Offset       int64    `json:"offset"`
	SampleNumber uint32   `json:"sample_number"`
	FragmentHash [32]byte `json:"fragment_sha256"`
}

// Window is an inclusive-start, exclusive-end nanosecond query interval.
type Window struct {
	TrackID    uint16
	StartNS    int64
	EndNS      int64
	ExpectHash [32]byte
}

// Index is a time-sorted keyframe set.
type Index struct {
	Keyframes []Keyframe `json:"keyframes"`
}

// Build converts each keyframe timestamp to nanoseconds, assigns MP4 sample
// numbers in input order per track, and sorts entries by time then track.
func Build(frames []*ingest.Frame) (*Index, error) {
	sampleNo := make(map[uint16]uint32)
	keys := make([]Keyframe, 0)
	for _, f := range frames {
		if f == nil {
			continue
		}
		sampleNo[f.TrackID]++
		if !f.KeyFrame {
			continue
		}
		ns, err := f.Timebase.ToNanoseconds(f.PTS)
		if err != nil {
			return nil, fault.New(fault.StageIndex, "invalid_timestamp", f.Offset, int(f.TrackID), err)
		}
		keys = append(keys, Keyframe{
			TrackID: f.TrackID, TimestampNS: ns, Offset: f.Offset,
			SampleNumber: sampleNo[f.TrackID], FragmentHash: f.SHA256,
		})
	}
	sort.SliceStable(keys, func(i, j int) bool {
		if keys[i].TimestampNS != keys[j].TimestampNS {
			return keys[i].TimestampNS < keys[j].TimestampNS
		}
		if keys[i].TrackID != keys[j].TrackID {
			return keys[i].TrackID < keys[j].TrackID
		}
		return keys[i].Offset < keys[j].Offset
	})
	return &Index{Keyframes: keys}, nil
}

// Query returns keyframes for track inside [startNS, endNS).
func (idx *Index) Query(trackID uint16, startNS, endNS int64) []Keyframe {
	if idx == nil || endNS < startNS {
		return nil
	}
	var out []Keyframe
	for _, k := range idx.Keyframes {
		if k.TrackID != trackID {
			continue
		}
		if k.TimestampNS >= startNS && k.TimestampNS < endNS {
			out = append(out, k)
		}
	}
	return out
}

// HitResult quantifies window-query hit rate for legal samples.
type HitResult struct {
	Hits    int
	Total   int
	Missing []Window
}

// HitRate queries every window and reports the fraction with at least one
// matching keyframe. When hash expectations are present they must also match.
func (idx *Index) HitRate(windows []Window) HitResult {
	res := HitResult{Total: len(windows)}
	for _, w := range windows {
		got := idx.Query(w.TrackID, w.StartNS, w.EndNS)
		ok := len(got) > 0
		if ok && w.ExpectHash != [32]byte{} {
			ok = false
			for _, k := range got {
				if k.FragmentHash == w.ExpectHash {
					ok = true
					break
				}
			}
		}
		if ok {
			res.Hits++
		} else {
			res.Missing = append(res.Missing, w)
		}
	}
	return res
}

// Rate returns hits/total as a percentage; empty indexes have rate 0.
func (r HitResult) Rate() float64 {
	if r.Total == 0 {
		return 0
	}
	return 100 * float64(r.Hits) / float64(r.Total)
}
