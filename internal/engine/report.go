package engine

import (
	"reflect"
	"sort"
	"time"

	"mediaregress/internal/media"
	"mediaregress/internal/split"
)

// ResumeStats computes the resumption figures the CLI prints. It is split out
// so the timings never enter the sealed document.
func ResumeStats(res *Result, started time.Time) Stats {
	s := res.Stats
	s.ResumeMillis = time.Since(started).Milliseconds()
	return s
}

// CompareManifests reports whether two runs produced the same sealed document.
// It returns a structured error naming the first differing top-level field so
// a mismatch is diagnosable rather than just "different".
func CompareManifests(a, b *Manifest) error {
	if a == nil || b == nil {
		return media.NewError(media.ClassOutputMismatch, media.StageProvenance, "", -1, "nil manifest")
	}
	if a.OutputSHA256 != b.OutputSHA256 {
		field := firstDiff(a, b)
		return media.Errorf(media.ClassOutputMismatch, media.StageProvenance, "", -1,
			"manifest digests differ (%s vs %s), first differing field: %s",
			a.OutputSHA256, b.OutputSHA256, field)
	}
	return nil
}

// firstDiff names the first top-level field whose value differs.
func firstDiff(a, b *Manifest) string {
	checks := []struct {
		name string
		eq   bool
	}{
		{"input", reflect.DeepEqual(a.Input, b.Input)},
		{"tracks", reflect.DeepEqual(a.Tracks, b.Tracks)},
		{"segments", reflect.DeepEqual(a.Segments, b.Segments)},
		{"silence_ranges", reflect.DeepEqual(a.SilenceRanges, b.SilenceRanges)},
		{"duplicates", reflect.DeepEqual(a.Duplicates, b.Duplicates)},
		{"fingerprints", reflect.DeepEqual(a.Fingerprints, b.Fingerprints)},
		{"errors", reflect.DeepEqual(a.Errors, b.Errors)},
		{"provenance", reflect.DeepEqual(a.Provenance, b.Provenance)},
		{"complete", a.Complete == b.Complete},
	}
	for _, c := range checks {
		if !c.eq {
			return c.name
		}
	}
	return "<seal-only>"
}

// SortedSegments returns segments in manifest order.
func SortedSegments(segs []split.Segment) []split.Segment {
	out := append([]split.Segment(nil), segs...)
	sort.Slice(out, func(i, j int) bool {
		if out[i].Track != out[j].Track {
			return out[i].Track < out[j].Track
		}
		return out[i].Index < out[j].Index
	})
	return out
}

// Summarise builds the terminal lines for a completed run.
func Summarise(res *Result) []string {
	m := res.Manifest
	lines := []string{}
	lines = append(lines, "tracks="+itoa(int64(len(m.Tracks))))
	lines = append(lines, "segments="+itoa(int64(len(m.Segments))))
	lines = append(lines, "silence_ranges="+itoa(int64(len(m.SilenceRanges))))
	lines = append(lines, "duplicates="+itoa(int64(len(m.Duplicates))))
	return lines
}

func itoa(v int64) string {
	if v == 0 {
		return "0"
	}
	neg := v < 0
	if neg {
		v = -v
	}
	var buf [20]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
