// Package issue defines the shared diagnostic model used by every module.
// Locations are either source lines (subtitle cues, timecode entries) or byte
// offsets (MP4 boxes, RTP packets); categories are stable strings so reports
// and digests never depend on Go error formatting.
package issue

import (
	"fmt"
	"sort"
	"strings"
)

// Category is a stable machine-readable problem class.
type Category string

const (
	// Subtitle and timecode diagnostics.
	InvalidTime          Category = "invalid_time"
	MalformedSubtitle    Category = "malformed_subtitle"
	NegativeDuration     Category = "negative_duration"
	OverlappingCue       Category = "overlapping_cue"
	CueOutOfRange        Category = "cue_out_of_range"
	InvalidDFDrop        Category = "invalid_df_drop"
	UnsupportedFrameRate Category = "unsupported_frame_rate"

	// Stream diagnostics.
	MP4Truncated     Category = "mp4_sample_table_truncated"
	MP4Structure     Category = "mp4_structure_invalid"
	MP4SampleTable   Category = "mp4_sample_table_mismatch"
	UnsupportedInput Category = "unsupported_input"
	RTPCorrupt       Category = "rtp_packet_corrupt"

	// Timestamp diagnostics.
	TimestampDuplicate Category = "timestamp_duplicate"
	TimestampRegress   Category = "timestamp_regress"
	TimestampMissing   Category = "timestamp_missing"

	// Timeline and clock diagnostics.
	LateUnrecoverable Category = "late_event_unrecoverable"
	DriftExceeded     Category = "clock_drift_exceeded"
)

// Location identifies where a problem was observed. Zero values mean absent.
type Location struct {
	Input      string
	Line       int   // 1-based source line
	ByteOffset int64 // 0-based byte offset in the input; -1 when absent
	Packet     int   // 1-based packet/sample number when meaningful
	Ref        string
}

// Issue is one diagnostic. Detail is descriptive and may be shown, but digest
// routines include it, so it must never contain clock time or randomness.
type Issue struct {
	Category    Category
	Loc         Location
	Detail      string
	Recoverable bool
}

// New starts an issue at an input without a precise position yet.
func New(cat Category, input, detail string) Issue {
	return Issue{Category: cat, Loc: Location{Input: input, ByteOffset: -1}, Detail: detail}
}

func (i Issue) WithLine(line int) Issue  { i.Loc.Line = line; return i }
func (i Issue) WithByte(off int64) Issue { i.Loc.ByteOffset = off; return i }
func (i Issue) WithPacket(n int) Issue   { i.Loc.Packet = n; return i }
func (i Issue) WithRef(ref string) Issue { i.Loc.Ref = ref; return i }
func (i Issue) AsRecoverable() Issue     { i.Recoverable = true; return i }
func (i Issue) String() string           { return i.describe() }
func (i Issue) describe() string {
	pos := i.Loc.Input
	if i.Loc.Line > 0 {
		pos = fmt.Sprintf("%s:%d", pos, i.Loc.Line)
	}
	if i.Loc.ByteOffset >= 0 {
		pos = fmt.Sprintf("%s@%d", pos, i.Loc.ByteOffset)
	}
	if i.Loc.Packet > 0 {
		pos = fmt.Sprintf("%s#%d", pos, i.Loc.Packet)
	}
	return fmt.Sprintf("%s: %s: %s", pos, i.Category, i.Detail)
}

// List is a deterministic, sortable set of issues.
type List []Issue

func (l *List) Add(items ...Issue) { *l = append(*l, items...) }

func (l List) Sorted() List {
	out := append(List(nil), l...)
	sort.Sort(out)
	return out
}

func (l List) Len() int      { return len(l) }
func (l List) Swap(i, j int) { l[i], l[j] = l[j], l[i] }
func (l List) Less(i, j int) bool {
	a, b := l[i], l[j]
	if a.Loc.Input != b.Loc.Input {
		return a.Loc.Input < b.Loc.Input
	}
	if a.Loc.ByteOffset != b.Loc.ByteOffset {
		// An absent offset (-1) sorts after a real offset.
		if a.Loc.ByteOffset < 0 {
			return false
		}
		if b.Loc.ByteOffset < 0 {
			return true
		}
		return a.Loc.ByteOffset < b.Loc.ByteOffset
	}
	if a.Loc.Line != b.Loc.Line {
		return a.Loc.Line < b.Loc.Line
	}
	if a.Loc.Packet != b.Loc.Packet {
		return a.Loc.Packet < b.Loc.Packet
	}
	if a.Category != b.Category {
		return a.Category < b.Category
	}
	if a.Loc.Ref != b.Loc.Ref {
		return a.Loc.Ref < b.Loc.Ref
	}
	return a.Detail < b.Detail
}

// Counts returns category counts; iteration order is not relied upon.
func (l List) Counts() map[Category]int {
	m := make(map[Category]int, len(l))
	for _, it := range l {
		m[it.Category]++
	}
	return m
}

// Categories returns unique categories sorted by name.
func (l List) Categories() []string {
	seen := map[Category]bool{}
	var out []string
	for _, it := range l {
		if !seen[it.Category] {
			seen[it.Category] = true
			out = append(out, string(it.Category))
		}
	}
	sort.Strings(out)
	return out
}

// First returns the earliest issue after deterministic sorting.
func (l List) First() (Issue, bool) {
	if len(l) == 0 {
		return Issue{}, false
	}
	return l.Sorted()[0], true
}

// ByteRange is an inclusive-start, exclusive-end damaged byte interval.
type ByteRange struct {
	Input      string
	Start, End int64
	Categories []Category
}

func (r ByteRange) String() string {
	cats := make([]string, len(r.Categories))
	for i, c := range r.Categories {
		cats[i] = string(c)
	}
	return fmt.Sprintf("%s:[%d,%d) %s", r.Input, r.Start, r.End, strings.Join(cats, ","))
}

// MergeRanges sorts and coalesces ranges from the same input when the gap
// between them is at most maxGap bytes. Adjacent damaged packets are separated
// by their 4-byte length prefixes, so callers usually pass maxGap=4.
func MergeRanges(in []ByteRange, maxGap int64) []ByteRange {
	if len(in) == 0 {
		return nil
	}
	ranges := append([]ByteRange(nil), in...)
	sort.SliceStable(ranges, func(i, j int) bool {
		if ranges[i].Input != ranges[j].Input {
			return ranges[i].Input < ranges[j].Input
		}
		if ranges[i].Start != ranges[j].Start {
			return ranges[i].Start < ranges[j].Start
		}
		return ranges[i].End < ranges[j].End
	})
	out := make([]ByteRange, 0, len(ranges))
	for _, r := range ranges {
		if r.End < r.Start {
			r.Start, r.End = r.End, r.Start
		}
		if len(out) > 0 {
			last := &out[len(out)-1]
			if last.Input == r.Input && r.Start <= last.End+maxGap {
				if r.End > last.End {
					last.End = r.End
				}
				last.Categories = mergeCategories(last.Categories, r.Categories)
				continue
			}
		}
		r.Categories = mergeCategories(nil, r.Categories)
		out = append(out, r)
	}
	return out
}

func mergeCategories(a, b []Category) []Category {
	seen := map[Category]bool{}
	out := append([]Category(nil), a...)
	for _, c := range a {
		seen[c] = true
	}
	for _, c := range b {
		if !seen[c] {
			seen[c] = true
			out = append(out, c)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}
