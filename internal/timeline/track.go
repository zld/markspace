// Package timeline maps per-track sample times onto one shared timeline and
// merges tracks into a single ordered sequence.
package timeline

import (
	"fmt"

	"mediacore/internal/clock"
	"mediacore/internal/fault"
	"mediacore/internal/ingest"
)

// Sample is one sample with both its original and synthesized time.
type Sample struct {
	TrackID       uint32 `json:"track_id"`
	TrackKind     string `json:"track_kind"`
	FragmentIndex int    `json:"fragment_index"`
	FragmentSeq   uint32 `json:"fragment_seq"`
	SampleIndex   int    `json:"sample_index"`
	File          string `json:"file"`
	FileOffset    int64  `json:"file_offset"`
	ByteLength    int64  `json:"byte_length"`
	// FragmentOffset and TfdtOffset are the raw input positions of the
	// fragment header and its decode-time field, so repairs can be reported
	// at the exact byte they corrected.
	FragmentOffset   int64 `json:"fragment_offset"`
	TfdtOffset       int64 `json:"tfdt_offset"`
	SourceTime       int64 `json:"source_time"`
	SourceDuration   int64 `json:"source_duration"`
	TimelineTime     int64 `json:"timeline_time"`
	TimelineDuration int64 `json:"timeline_duration"`
	// Repaired marks samples whose synthesized time differs from the plain
	// linear mapping because a fault was repaired.
	Repaired bool   `json:"repaired,omitempty"`
	Repair   string `json:"repair,omitempty"`
}

// End returns the exclusive timeline end of the sample.
func (s Sample) End() int64 { return s.TimelineTime + s.TimelineDuration }

// Track is one mapped media track.
type Track struct {
	ID        uint32        `json:"track_id"`
	Kind      string        `json:"kind"`
	Timescale uint32        `json:"timescale"`
	Mapping   clock.Mapping `json:"mapping"`
	Samples   []Sample      `json:"samples"`
	// Mergeable is false when a defect cannot be repaired without guessing.
	Mergeable bool           `json:"mergeable"`
	Reason    string         `json:"reason,omitempty"`
	Errors    []*fault.Error `json:"-"`
	// Discontinuities records sequence gaps and regressions found at mapping.
	Discontinuities []Discontinuity `json:"discontinuities,omitempty"`
}

// Discontinuity is a structural break in the input fragment sequence.
type Discontinuity struct {
	Kind       fault.Code `json:"kind"`
	Fragment   int        `json:"fragment_index"`
	Sequence   uint32     `json:"sequence"`
	Offset     int64      `json:"offset"`
	Detail     string     `json:"detail,omitempty"`
	Resolvable bool       `json:"resolvable"`
}

// BuildTracks maps every fragment onto the shared timeline. The source start
// of the first fragment is recorded in the mapping and never shifted to zero.
func BuildTracks(files []*ingest.InitInfo, fragments []ingest.Fragment, timelineTimescale uint32) ([]*Track, []*fault.Error) {
	byID := make(map[uint32]*Track)
	var order []uint32
	var errs []*fault.Error
	declared := make(map[uint32]ingest.InitTrack)
	for _, info := range files {
		for _, t := range info.Tracks {
			declared[t.TrackID] = t
		}
	}
	for _, frag := range fragments {
		info, ok := declared[frag.TrackID]
		if !ok {
			errs = append(errs, fault.New(fault.CodeUnmergeableTrack, frag.File, frag.MoofOffset, "tfhd",
				fmt.Sprintf("track %d is not declared by any initialization segment", frag.TrackID)))
			continue
		}
		tr, ok := byID[frag.TrackID]
		if !ok {
			mapping, err := clock.NewMapping(frag.TrackID, info.Timescale, timelineTimescale, int64(frag.Tfdt))
			if err != nil {
				errs = append(errs, fault.New(fault.CodeOverflow, frag.File, frag.TfdtOffset, "tfdt", err.Error()))
				continue
			}
			tr = &Track{
				ID:        frag.TrackID,
				Kind:      info.Kind,
				Timescale: info.Timescale,
				Mapping:   mapping,
				Mergeable: true,
			}
			byID[frag.TrackID] = tr
			order = append(order, frag.TrackID)
		}
		samples, f := mapFragment(tr, frag, timelineTimescale)
		if f != nil {
			errs = append(errs, f)
			tr.Mergeable = false
			tr.Reason = f.Error()
			tr.Errors = append(tr.Errors, f)
			continue
		}
		tr.Samples = append(tr.Samples, samples...)
	}
	tracks := make([]*Track, 0, len(order))
	for _, id := range order {
		tracks = append(tracks, byID[id])
	}
	return tracks, errs
}

func mapFragment(tr *Track, frag ingest.Fragment, timelineTimescale uint32) ([]Sample, *fault.Error) {
	if len(frag.Samples) == 0 {
		return nil, fault.New(fault.CodeMissingField, frag.File, frag.MoofOffset, "moof", "fragment without samples").WithFragment(frag.Sequence)
	}
	out := make([]Sample, 0, len(frag.Samples))
	// Sample starts accumulate from tfdt using exact source ticks. The
	// mapping converts each absolute start; no scaled value is accumulated.
	var sourceCursor = int64(frag.Tfdt)
	for _, s := range frag.Samples {
		if s.Duration < 0 {
			return nil, fault.New(fault.CodeNegativeDuration, frag.File, frag.TrunOffset, "trun",
				fmt.Sprintf("sample %d has negative duration", s.Index)).WithFragment(frag.Sequence)
		}
		timelineTime, err := tr.Mapping.ToTimeline(sourceCursor)
		if err != nil {
			return nil, fault.New(fault.CodeOverflow, frag.File, frag.TfdtOffset, "tfdt", err.Error()).WithFragment(frag.Sequence)
		}
		timelineDuration, err := tr.Mapping.DurationToTimeline(s.Duration)
		if err != nil {
			return nil, fault.New(fault.CodeOverflow, frag.File, frag.TrunOffset, "trun", err.Error()).WithFragment(frag.Sequence)
		}
		out = append(out, Sample{
			TrackID:          tr.ID,
			TrackKind:        tr.Kind,
			FragmentIndex:    frag.Index,
			FragmentSeq:      frag.Sequence,
			SampleIndex:      s.Index,
			File:             frag.File,
			FileOffset:       s.Offset,
			ByteLength:       s.Size,
			FragmentOffset:   frag.MoofOffset,
			TfdtOffset:       frag.TfdtOffset,
			SourceTime:       sourceCursor,
			SourceDuration:   s.Duration,
			TimelineTime:     timelineTime,
			TimelineDuration: timelineDuration,
		})
		sourceCursor += s.Duration
	}
	return out, nil
}
