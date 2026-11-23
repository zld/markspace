package model

import "strings"

// JoinRanges merges overlapping/adjacent ranges into a minimal sorted list.
func JoinRanges(in []Range) []Range {
	if len(in) == 0 {
		return nil
	}
	out := make([]Range, 0, len(in))
	out = append(out, in...)
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j-1].Start.Cmp(out[j].Start) > 0; j-- {
			out[j-1], out[j] = out[j], out[j-1]
		}
	}
	merged := out[:1]
	for _, rg := range out[1:] {
		last := &merged[len(merged)-1]
		if rg.Start.Cmp(last.End) <= 0 {
			if rg.End.Cmp(last.End) > 0 {
				last.End = rg.End
			}
			continue
		}
		merged = append(merged, rg)
	}
	return merged
}

// DescribeRanges renders ranges for deterministic JSON/text reports.
func DescribeRanges(rs []Range) string {
	parts := make([]string, len(rs))
	for i, r := range rs {
		parts[i] = r.String()
	}
	return strings.Join(parts, ",")
}
