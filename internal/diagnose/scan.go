package diagnose

import (
	"fmt"
	"sort"
	"strings"

	"mediad/internal/core"
)

// Range is a merged half-open damaged interval in one input asset. Multiple
// findings that overlap or touch are folded together while their categories
// remain individually auditable.
type Range struct {
	Asset       string    `json:"asset"`
	Format      string    `json:"format"`
	Span        core.Span `json:"span"`
	Categories  []string  `json:"categories"`
	Stages      []string  `json:"stages"`
	Issues      int       `json:"issues"`
	Recoverable bool      `json:"recoverable"`
}

// Ranges merges damage findings into non-overlapping ranges. Findings that
// only describe a benign boundary (for example an RTP wrap) are included when
// they overlap a damaged range but never create a range on their own.
func Ranges(issues Issues) []Range {
	sorted := make(Issues, len(issues))
	copy(sorted, issues)
	sorted.Sort()

	var out []Range
	index := make(map[string]int)
	for _, is := range sorted {
		if !is.IsDamage() {
			continue
		}
		key := is.Asset + "\x00" + is.Format
		if at, ok := index[key]; ok {
			cur := &out[at]
			if is.Span.Start <= cur.Span.End && is.Span.End >= cur.Span.Start {
				cur.Span = cur.Span.Union(is.Span)
				cur.Categories = appendUnique(cur.Categories, string(is.Category))
				cur.Stages = appendUnique(cur.Stages, string(is.Stage))
				cur.Issues++
				cur.Recoverable = cur.Recoverable && is.Recoverable
				continue
			}
		}
		index[key] = len(out)
		out = append(out, Range{
			Asset:       is.Asset,
			Format:      is.Format,
			Span:        is.Span,
			Categories:  []string{string(is.Category)},
			Stages:      []string{string(is.Stage)},
			Issues:      1,
			Recoverable: is.Recoverable,
		})
	}
	sort.SliceStable(out, func(a, b int) bool {
		if out[a].Asset != out[b].Asset {
			return out[a].Asset < out[b].Asset
		}
		if out[a].Span.Start != out[b].Span.Start {
			return out[a].Span.Start < out[b].Span.Start
		}
		return out[a].Span.End < out[b].Span.End
	})
	return out
}

func appendUnique(xs []string, v string) []string {
	for _, x := range xs {
		if x == v {
			return xs
		}
	}
	xs = append(xs, v)
	sort.Strings(xs)
	return xs
}

// Validate checks ranges against the asset sizes and each other.
func Validate(ranges []Range, sizes map[string]int64) error {
	seen := make(map[string]core.Span)
	for n, r := range ranges {
		if r.Asset == "" || r.Format == "" {
			return fmt.Errorf("diagnose: range %d missing asset/format", n)
		}
		size, ok := sizes[r.Asset]
		if !ok {
			return fmt.Errorf("diagnose: range %d references unknown asset %q", n, r.Asset)
		}
		if !r.Span.Within(size) {
			return fmt.Errorf("diagnose: range %s [%d,%d) outside asset size %d", r.Asset, r.Span.Start, r.Span.End, size)
		}
		key := r.Asset + "\x00" + r.Format
		if prev, ok := seen[key]; ok && r.Span.Start <= prev.End {
			return fmt.Errorf("diagnose: overlapping ranges for %s", r.Asset)
		}
		seen[key] = r.Span
		if len(r.Categories) == 0 || len(r.Stages) == 0 {
			return fmt.Errorf("diagnose: range %d missing categories/stages", n)
		}
	}
	return nil
}

// FormatCategories joins range categories for a stable report field.
func FormatCategories(cats []string) string { return strings.Join(cats, ",") }
