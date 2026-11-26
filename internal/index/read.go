package index

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"example.com/archiveverify/internal/model"
)

// Range is a half-open microsecond interval [Start,End).
type Range struct {
	Start model.Us `json:"start_us"`
	End   model.Us `json:"end_us"`
}

// ParseRange parses values such as 2s:3s or 1500ms:3500ms.
func ParseRange(s string) (Range, error) {
	parts := strings.Split(s, ":")
	if len(parts) != 2 {
		return Range{}, fmt.Errorf("range must be start:end, got %q", s)
	}
	startD, err := time.ParseDuration(strings.TrimSpace(parts[0]))
	if err != nil {
		return Range{}, fmt.Errorf("invalid range start: %w", err)
	}
	endD, err := time.ParseDuration(strings.TrimSpace(parts[1]))
	if err != nil {
		return Range{}, fmt.Errorf("invalid range end: %w", err)
	}
	r := Range{Start: model.Us(startD.Microseconds()), End: model.Us(endD.Microseconds())}
	if r.Start < 0 || r.End <= r.Start {
		return Range{}, fmt.Errorf("range must satisfy 0 <= start < end: %s", s)
	}
	return r, nil
}

// ReadSample marks a returned sample and any decode-preroll role.
type ReadSample struct {
	Entry
	InRange           bool `json:"in_range"`
	DecodePreroll     bool `json:"decode_preroll,omitempty"`
	RequiredForDecode bool `json:"required_for_decode,omitempty"`
}

// AudioBoundary reports exact sample-frame boundaries.
type AudioBoundary struct {
	TrackID     uint32   `json:"track_id"`
	Timescale   uint32   `json:"timescale"`
	FirstSample int      `json:"first_sample_index"`
	LastSample  int      `json:"last_sample_index"`
	StartUs     model.Us `json:"start_us"`
	EndUs       model.Us `json:"end_us"`
	StartFrame  int64    `json:"start_frame"`
	EndFrame    int64    `json:"end_frame"`
}

// RangeResult contains half-open range samples and decode prefix.
type RangeResult struct {
	Range         Range          `json:"range"`
	Video         []ReadSample   `json:"video"`
	DecodePrefix  []ReadSample   `json:"decode_prefix"`
	Audio         []ReadSample   `json:"audio"`
	AudioBoundary *AudioBoundary `json:"audio_boundary,omitempty"`
}

// ReadRange reads entries from a built index. The interval is [r.Start,r.End).
func ReadRange(idx *Index, r Range) (*RangeResult, error) {
	if idx == nil {
		return nil, fmt.Errorf("nil index")
	}
	if r.End <= r.Start {
		return nil, fmt.Errorf("invalid range %+v", r)
	}
	entries := append([]Entry(nil), idx.Entries...)
	sort.SliceStable(entries, func(i, j int) bool {
		if entries[i].StartUs != entries[j].StartUs {
			return entries[i].StartUs < entries[j].StartUs
		}
		return entries[i].TrackID < entries[j].TrackID
	})

	out := &RangeResult{Range: r}
	var firstVideo *Entry
	for _, e := range entries {
		if e.Role == "vide" || e.TrackID == 1 {
			if e.StartUs >= r.Start && e.StartUs < r.End {
				e2 := e
				out.Video = append(out.Video, ReadSample{Entry: e2, InRange: true})
				if firstVideo == nil {
					firstVideo = &e2
				}
			}
		}
		if e.Role == "soun" || e.TrackID == 2 {
			if e.StartUs >= r.Start && e.StartUs < r.End {
				out.Audio = append(out.Audio, ReadSample{Entry: e, InRange: true})
			}
		}
	}

	if firstVideo != nil {
		var key *Entry
		for i := range entries {
			e := entries[i]
			if (e.Role == "vide" || e.TrackID == 1) && e.KeyFrame && e.StartUs <= firstVideo.StartUs {
				e2 := e
				key = &e2
			}
		}
		if key != nil && key.StartUs < firstVideo.StartUs {
			for _, e := range entries {
				if (e.Role == "vide" || e.TrackID == 1) &&
					e.StartUs >= key.StartUs && e.StartUs < firstVideo.StartUs {
					out.DecodePrefix = append(out.DecodePrefix, ReadSample{
						Entry: e, DecodePreroll: true, RequiredForDecode: true,
					})
				}
			}
			for i := range out.Video {
				out.Video[i].RequiredForDecode = true
			}
		}
	}

	if len(out.Audio) > 0 {
		first := out.Audio[0].Entry
		last := out.Audio[len(out.Audio)-1].Entry
		scale := first.Timescale
		if scale == 0 {
			scale = 48_000
		}
		out.AudioBoundary = &AudioBoundary{
			TrackID: first.TrackID, Timescale: scale,
			FirstSample: first.SampleIndex, LastSample: last.SampleIndex,
			StartUs: first.StartUs, EndUs: last.EndUs,
			StartFrame: int64(first.StartUs) * int64(scale) / 1_000_000,
			EndFrame:   int64(last.EndUs) * int64(scale) / 1_000_000,
		}
	}
	return out, nil
}
