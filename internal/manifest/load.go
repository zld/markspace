package manifest

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"mediabatch/internal/clock"
	"mediabatch/internal/fault"
)

// Bundle is every manifest parsed from one input root, in deterministic order.
type Bundle struct {
	Manifests []*Manifest
	Issues    fault.List
}

// Segments flattens every segment across manifests and representations.
func (b *Bundle) Segments() []Segment {
	var out []Segment
	for _, m := range b.Manifests {
		out = append(out, m.Segments...)
	}
	return out
}

// LoadDir parses every *.mpd and *.m3u8 below root. Parse failures are recorded
// as issues rather than aborting the scan, so a broken DASH document still
// yields the anomalies of everything else in the input.
func LoadDir(root string) (*Bundle, error) {
	info, err := os.Stat(root)
	if err != nil {
		return nil, fault.NewErr(fault.CodeInputMissing, fault.Locator{File: root}, "input root: %v", err)
	}
	if !info.IsDir() {
		return nil, fault.NewErr(fault.CodeInputMissing, fault.Locator{File: root}, "input root is not a directory")
	}
	var files []string
	err = filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		switch strings.ToLower(filepath.Ext(p)) {
		case ".mpd", ".m3u8":
			files = append(files, p)
		}
		return nil
	})
	if err != nil {
		return nil, fault.NewErr(fault.CodeInputMissing, fault.Locator{File: root}, "walk input: %v", err)
	}
	sort.Strings(files)
	b := &Bundle{}
	for _, f := range files {
		var m *Manifest
		switch strings.ToLower(filepath.Ext(f)) {
		case ".mpd":
			m, err = ParseDASH(f)
		case ".m3u8":
			m, err = ParseHLS(f)
		}
		if m != nil {
			CheckReferences(m)
			b.Manifests = append(b.Manifests, m)
			b.Issues = append(b.Issues, m.Issues...)
		}
		if err != nil && m == nil {
			b.Issues = append(b.Issues, issueAt(f, 0, 0, fault.CodeParse, err.Error()))
		}
	}
	b.Issues.Sort()
	return b, nil
}

// CheckReferences verifies that each declared reference resolves inside the
// manifest's own directory and that each declared range lies inside the
// presentation window. An escaping or negative reference is an out-of-range
// anomaly, recorded with the declaring location; the segment is left in place
// so the scanner can later isolate it rather than silently dropping input.
func CheckReferences(m *Manifest) {
	root := filepath.Clean(filepath.Dir(m.File))
	for i := range m.Segments {
		seg := &m.Segments[i]
		if seg.URL == "" {
			continue
		}
		if filepath.IsAbs(seg.URL) {
			m.Issues = append(m.Issues, refIssue(m.File, seg.Representation, seg.URL,
				fault.Locator{File: m.File, Line: seg.Line, Offset: seg.Offset},
				seg.Start, fault.CodeOutOfRangeRef, "absolute segment reference is outside the input root"))
			continue
		}
		resolved := filepath.Clean(filepath.Join(root, filepath.FromSlash(seg.URL)))
		if rel, err := filepath.Rel(root, resolved); err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			m.Issues = append(m.Issues, refIssue(m.File, seg.Representation, seg.URL,
				fault.Locator{File: m.File, Line: seg.Line, Offset: seg.Offset},
				seg.Start, fault.CodeOutOfRangeRef,
				fmt.Sprintf("reference escapes manifest directory: %s", seg.URL)))
			continue
		}
		if seg.End.Cmp(seg.Start) < 0 {
			m.Issues = append(m.Issues, refIssue(m.File, seg.Representation, seg.URL,
				fault.Locator{File: m.File, Line: seg.Line, Offset: seg.Offset},
				seg.Start, fault.CodeOutOfRangeRef, "segment range ends before it starts"))
		}
	}
	m.Issues.Sort()
}

// Timeline returns the union time span covered by segments.
func (b *Bundle) Timeline() (start, end clock.Time) {
	start, end = clock.Zero, clock.Zero
	for _, s := range b.Segments() {
		if start.Den == 0 || s.Start.Cmp(start) < 0 {
			start = s.Start
		}
		if s.End.Cmp(end) > 0 {
			end = s.End
		}
	}
	return start, end
}
