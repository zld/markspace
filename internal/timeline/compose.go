package timeline

import (
	"fmt"
	"sort"

	"continuity/internal/media"
)

// TrackSample is one normalized sample on an audio/video track.
type TrackSample struct {
	Track        string
	Kind         string
	PTSNS        int64
	DurationNS   int64
	Source       string
	Sequence     uint64
	Switch       bool
	ArrivalIndex int
}

// SwitchBoundary records a primary/backup source transition.
type SwitchBoundary struct {
	AtNS     int64  `json:"at_ns"`
	Sequence uint64 `json:"sequence"`
	Track    string `json:"track"`
	Source   string `json:"source"`
	Reason   string `json:"reason"`
}

// Point is one presentational alignment sample / review frame.
type Point struct {
	Index       int          `json:"index"`
	PTSNS       int64        `json:"pts_ns"`
	Video       *TrackSample `json:"video,omitempty"`
	Audio       *TrackSample `json:"audio,omitempty"`
	SyncErrorNS int64        `json:"sync_error_ns"`
	Switch      bool         `json:"switch"`
	Gap         bool         `json:"gap"`
	Missing     []string     `json:"missing,omitempty"`
	Sources     []string     `json:"sources"`
}

// Result is the unified multi-track timeline.
type Result struct {
	Points           []Point          `json:"points"`
	SwitchBoundaries []SwitchBoundary `json:"switch_boundaries"`
	SyncMaxNS        int64            `json:"sync_max_ns"`
	Duplicates       int              `json:"duplicates"`
	MissingSamples   int              `json:"missing_samples"`
}

// Options controls alignment.
type Options struct {
	SyncToleranceNS int64
	MatchWindowNS   int64
}

// Compose deduplicates, sorts by PTS (not arrival order), aligns audio to
// video, and records source-switch boundaries. Missing tracks are marked
// rather than synthesized.
func Compose(input []TrackSample, opts Options) (*Result, error) {
	tolerance := opts.SyncToleranceNS
	if tolerance <= 0 {
		tolerance = int64(20 * media.Millisecond)
	}
	matchWindow := opts.MatchWindowNS
	if matchWindow <= 0 {
		matchWindow = int64(500 * media.Millisecond)
	}

	unique, duplicates := dedupe(input)
	videos := byKind(unique, "video")
	audios := byKind(unique, "audio")
	sortSamples(videos)
	sortSamples(audios)

	r := &Result{Duplicates: duplicates}
	usedAudio := map[int]bool{}
	var points []Point

	for _, v := range videos {
		idx := nearestAudio(v.PTSNS, audios, usedAudio)
		p := Point{PTSNS: v.PTSNS, Video: &v}
		if idx >= 0 && abs64(audios[idx].PTSNS-v.PTSNS) <= matchWindow {
			a := audios[idx]
			p.Audio = &a
			p.SyncErrorNS = abs64(a.PTSNS - v.PTSNS)
			usedAudio[idx] = true
		} else {
			p.Missing = append(p.Missing, "audio")
			p.Gap = true
		}
		points = append(points, p)
	}
	for i, a := range audios {
		if usedAudio[i] {
			continue
		}
		points = append(points, Point{PTSNS: a.PTSNS, Audio: &a, Missing: []string{"video"}, Gap: true})
	}

	sort.SliceStable(points, func(i, j int) bool { return points[i].PTSNS < points[j].PTSNS })
	for i := range points {
		p := &points[i]
		p.Index = i
		if p.Audio != nil && p.Video == nil {
			p.PTSNS = p.Audio.PTSNS
		}
		if p.SyncErrorNS > r.SyncMaxNS {
			r.SyncMaxNS = p.SyncErrorNS
		}
		if len(p.Missing) > 0 {
			r.MissingSamples++
		}
		collectMeta(p, r)
		r.Points = append(r.Points, *p)
	}
	if r.SyncMaxNS > tolerance {
		return r, &media.Fault{Phase: media.PhaseTimeline, Code: media.CodeAmbiguousJump,
			Detail: fmt.Sprintf("maximum A/V sync error %dns exceeds tolerance %dns", r.SyncMaxNS, tolerance)}
	}
	return r, nil
}

func dedupe(input []TrackSample) ([]TrackSample, int) {
	seen := map[string]bool{}
	var out []TrackSample
	duplicates := 0
	for _, s := range input {
		key := fmt.Sprintf("%s|%s|%d|%d|%s", s.Track, s.Kind, s.PTSNS, s.DurationNS, s.Source)
		if seen[key] {
			duplicates++
			continue
		}
		seen[key] = true
		out = append(out, s)
	}
	return out, duplicates
}

func byKind(in []TrackSample, kind string) []TrackSample {
	var out []TrackSample
	for _, s := range in {
		if s.Kind == kind {
			out = append(out, s)
		}
	}
	return out
}

func sortSamples(s []TrackSample) {
	sort.SliceStable(s, func(i, j int) bool {
		if s[i].PTSNS != s[j].PTSNS {
			return s[i].PTSNS < s[j].PTSNS
		}
		return s[i].Sequence < s[j].Sequence
	})
}

func nearestAudio(pts int64, audios []TrackSample, used map[int]bool) int {
	best := -1
	bestDelta := int64(1 << 62)
	for i, a := range audios {
		if used[i] {
			continue
		}
		d := abs64(a.PTSNS - pts)
		if d < bestDelta || (d == bestDelta && best >= 0 && a.Sequence < audios[best].Sequence) {
			best, bestDelta = i, d
		}
	}
	return best
}

func collectMeta(p *Point, r *Result) {
	for _, s := range []*TrackSample{p.Video, p.Audio} {
		if s == nil {
			continue
		}
		if s.Switch {
			p.Switch = true
			r.SwitchBoundaries = append(r.SwitchBoundaries, SwitchBoundary{
				AtNS: s.PTSNS, Sequence: s.Sequence, Track: s.Track,
				Source: s.Source, Reason: "EXT-X-DISCONTINUITY or source boundary",
			})
		}
		if !contains(p.Sources, s.Source) {
			p.Sources = append(p.Sources, s.Source)
		}
	}
	sort.Strings(p.Sources)
}

func contains(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}

func abs64(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}
