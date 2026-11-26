package index

import "github.com/zld/medcheck/pkg/media"

// AnnotateIssues marks segments containing corruption evidence. It does not
// delete the segment or its hash: damaged ranges remain auditable, while
// callers can exclude Corrupt segments from playable output.
func AnnotateIssues(segments []Segment, issues media.Issues) []Segment {
	out := append([]Segment(nil), segments...)
	for _, e := range issues {
		if e == nil || e.Offset < 0 {
			continue
		}
		for i := range out {
			if e.Offset >= out[i].Offset && e.Offset < out[i].End {
				out[i].Corrupt = true
				cat := string(e.Category)
				if !containsString(out[i].IssueCategories, cat) {
					out[i].IssueCategories = append(out[i].IssueCategories, cat)
				}
			}
		}
	}
	return out
}

// CountCorrupt returns the number of marked segments.
func CountCorrupt(segments []Segment) int {
	n := 0
	for _, s := range segments {
		if s.Corrupt {
			n++
		}
	}
	return n
}

func containsString(xs []string, want string) bool {
	for _, x := range xs {
		if x == want {
			return true
		}
	}
	return false
}
