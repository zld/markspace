// Package manifest parses DASH MPD documents and HLS playlists into one
// normalized description of media segments. Parsers never invent timing: a
// segment whose duration is missing keeps a zero-length range and is reported
// as an isolated anomaly instead of being extrapolated.
package manifest

import (
	"path/filepath"
	"sort"

	"mediabatch/internal/clock"
	"mediabatch/internal/fault"
)

// Segment is one media range inside a representation.
type Segment struct {
	Representation string
	// URL is the reference exactly as declared by the manifest.
	URL   string
	Start clock.Time
	End   clock.Time
	// Duration is the declared duration. When Declared is false the manifest
	// omitted it and Duration is zero.
	Duration clock.Time
	Declared bool
	// Index is the 0-based position within the representation.
	Index int
	// Line and Offset locate the declaration in the manifest file.
	Line   int
	Offset int64
	// Discontinuity reports an EXT-X-DISCONTINUITY before this segment or a
	// DASH period boundary at this segment.
	Discontinuity bool
	// Period is the DASH period id (empty for HLS).
	Period string
	// Isolated marks a segment that must never be published: a reference that
	// escapes the input root, a duplicate URI or a range that cannot be
	// validated. The segment is kept in the manifest so the report can point
	// at it, but the scanner skips it.
	Isolated bool
}

// Manifest is the normalized parse output.
type Manifest struct {
	Kind     string // "dash" or "hls"
	File     string
	Segments []Segment
	Issues   fault.List
	// presentationDuration is the MPD-level mediaPresentationDuration when
	// declared, used as the last-resort window for period-less resources.
	presentationDuration string
}

// Discontinuities returns the indices of segments that start a discontinuity.
func (m *Manifest) Discontinuities() []int {
	var out []int
	for i, s := range m.Segments {
		if s.Discontinuity {
			out = append(out, i)
		}
	}
	return out
}

// SortIssues orders the anomaly list deterministically.
func (m *Manifest) SortIssues() {
	m.Issues.Sort()
}

// IssueAt builds an anomaly with a byte/line locator on the manifest.
func issueAt(file string, line int, offset int64, code fault.Code, msg string) fault.Issue {
	return fault.Issue{
		Code: code, Severity: fault.SeverityError, Action: fault.ActionIsolated,
		Message: msg,
		Source: fault.Source{
			Manifest: file,
			Loc:      fault.Locator{File: file, Line: line, Offset: offset},
		},
	}
}

func (m *Manifest) add(code fault.Code, sev fault.Severity, action fault.Action, seg Segment, msg string) {
	m.Issues = append(m.Issues, fault.Issue{
		Code: code, Severity: sev, Action: action,
		Message: msg,
		Source: fault.Source{
			Manifest: m.File, Representation: seg.Representation, Segment: seg.URL,
			Loc:   fault.Locator{File: m.File, Line: seg.Line, Offset: seg.Offset},
			Start: seg.Start, End: seg.End,
		},
	})
}

func sortSegments(in []Segment) []Segment {
	out := make([]Segment, len(in))
	copy(out, in)
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if c := a.Start.Cmp(b.Start); c != 0 {
			return c < 0
		}
		if a.Representation != b.Representation {
			return a.Representation < b.Representation
		}
		return a.Index < b.Index
	})
	return out
}

func lineAt(data []byte, offset int) int {
	line := 1
	if offset > len(data) {
		offset = len(data)
	}
	if offset < 0 {
		return 0
	}
	for i := 0; i < offset; i++ {
		if data[i] == '\n' {
			line++
		}
	}
	return line
}

// Resolve joins a segment reference to the directory holding the manifest. An
// absolute reference or one escaping the work directory is reported by callers.
func Resolve(manifestPath, ref string) string {
	if filepath.IsAbs(ref) {
		return ref
	}
	return filepath.Join(filepath.Dir(manifestPath), filepath.FromSlash(ref))
}
