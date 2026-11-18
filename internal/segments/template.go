// Package segments expands SegmentTemplate placeholders and verifies the
// expanded references: sequence continuity, unique timestamps and containment
// inside the configured segment root (no path traversal).
package segments

import (
	"fmt"
	"path"
	"path/filepath"
	"strconv"
	"strings"

	"dashforge/internal/model"
)

// ExpandedSegment pairs one normalized fragment with its resolved file path.
type ExpandedSegment struct {
	Number int    `json:"number"`
	Time   int64  `json:"time"`
	Path   string `json:"path"`
}

// RenditionResult is the full expansion of one representation.
type RenditionResult struct {
	RenditionID string            `json:"rendition_id"`
	Count       int               `json:"count"`
	First       string            `json:"first_segment"`
	Last        string            `json:"last_segment"`
	Segments    []ExpandedSegment `json:"segments"`
}

// Issue is one expansion/reference problem. All issues are returned at once so
// the operator gets a complete picture instead of one failure per run.
type Issue struct {
	Rendition string `json:"rendition"`
	Kind      string `json:"kind"`
	Detail    string `json:"detail"`
}

func (i Issue) Error() string {
	return fmt.Sprintf("rendition %q: %s: %s", i.Rendition, i.Kind, i.Detail)
}

// allowed placeholders per ISO/IEC 23009-1 SegmentTemplate.
var validPlaceholders = map[string]bool{
	"$$": true, "$RepresentationID$": true, "$Number$": true, "$Time$": true,
	"$Bandwidth$": true,
}

// ValidateTemplate parses a template without expanding it, rejecting empty
// templates and unknown/malformed placeholders such as "$Oops$" or "$Number".
func ValidateTemplate(tpl string) error {
	if strings.TrimSpace(tpl) == "" {
		return fmt.Errorf("empty segment template")
	}
	i := 0
	for i < len(tpl) {
		open := strings.IndexByte(tpl[i:], '$')
		if open < 0 {
			return nil
		}
		open += i
		if open == len(tpl)-1 {
			return fmt.Errorf("malformed placeholder in template %q: dangling '$'", tpl)
		}
		close := strings.IndexByte(tpl[open+1:], '$')
		if close < 0 {
			return fmt.Errorf("malformed placeholder in template %q: unclosed '$'", tpl)
		}
		inner := tpl[open : open+1+close+1]
		if !validPlaceholders[inner] && !isFormattedNumber(inner) {
			return fmt.Errorf("illegal placeholder %s in template %q", inner, tpl)
		}
		i = open + 1 + close + 1
	}
	return nil
}

// isFormattedNumber recognizes $Number%0nd$ style width specifiers.
func isFormattedNumber(token string) bool {
	if !strings.HasPrefix(token, "$Number%") || !strings.HasSuffix(token, "d$") {
		return false
	}
	body := strings.TrimSuffix(strings.TrimPrefix(token, "$Number%"), "d$")
	if len(body) < 2 || body[0] != '0' {
		return false
	}
	_, err := strconv.Atoi(body[1:])
	return err == nil
}

// Expand resolves every representation's templates and runs all reference
// checks. It returns partial results together with the full issue list so
// callers can report per-rendition summaries even when validation fails.
func Expand(man *model.Manifest) ([]RenditionResult, []Issue) {
	var results []RenditionResult
	var issues []Issue

	for _, rm := range man.Renditions {
		res := RenditionResult{RenditionID: rm.ID}
		if err := ValidateTemplate(rm.Media); err != nil {
			issues = append(issues, Issue{rm.ID, "template", err.Error()})
			results = append(results, res)
			continue
		}
		if rm.Initialization != "" {
			if err := ValidateTemplate(rm.Initialization); err != nil {
				issues = append(issues, Issue{rm.ID, "init_template", err.Error()})
			}
		}

		seenTime := make(map[int64]bool, len(rm.Segments))
		prevNumber := 0
		for _, seg := range rm.Segments {
			if prevNumber != 0 && seg.Number != prevNumber+1 {
				issues = append(issues, Issue{
					rm.ID, "number_gap",
					fmt.Sprintf("expected number %d after %d, got %d", prevNumber+1, prevNumber, seg.Number),
				})
			}
			prevNumber = seg.Number
			if seenTime[seg.Start] {
				issues = append(issues, Issue{
					rm.ID, "duplicate_timestamp",
					fmt.Sprintf("segment %d reuses timestamp %d", seg.Number, seg.Start),
				})
			}
			seenTime[seg.Start] = true

			ref, err := expandOne(rm.Media, rm, seg.Number, seg.Start)
			if err != nil {
				issues = append(issues, Issue{rm.ID, "template", err.Error()})
				continue
			}
			if err := checkContained(rm.SegmentRoot, ref); err != nil {
				issues = append(issues, Issue{rm.ID, "path_traversal", err.Error()})
			}
			res.Segments = append(res.Segments, ExpandedSegment{
				Number: seg.Number, Time: seg.Start, Path: ref,
			})
		}
		res.Count = len(res.Segments)
		if res.Count > 0 {
			res.First = res.Segments[0].Path
			res.Last = res.Segments[res.Count-1].Path
		}
		results = append(results, res)
	}
	return results, issues
}

func expandOne(tpl string, rm *model.RenditionManifest, number int, time int64) (string, error) {
	var b strings.Builder
	i := 0
	for i < len(tpl) {
		open := strings.IndexByte(tpl[i:], '$')
		if open < 0 {
			b.WriteString(tpl[i:])
			break
		}
		b.WriteString(tpl[i : i+open])
		open += i
		closeRel := strings.IndexByte(tpl[open+1:], '$')
		close := open + 1 + closeRel
		token := tpl[open : close+1]
		switch {
		case token == "$$":
			b.WriteByte('$')
		case token == "$RepresentationID$":
			b.WriteString(rm.ID)
		case token == "$Bandwidth$":
			b.WriteString(strconv.Itoa(rm.Bandwidth))
		case token == "$Number$":
			b.WriteString(strconv.Itoa(number))
		case token == "$Time$":
			b.WriteString(strconv.FormatInt(time, 10))
		case isFormattedNumber(token):
			width, _ := strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(token, "$Number%0"), "d$"))
			b.WriteString(fmt.Sprintf("%0*d", width, number))
		default:
			return "", fmt.Errorf("illegal placeholder %s", token)
		}
		i = close + 1
	}
	return b.String(), nil
}

// checkContained ensures ref resolves to a location inside root after cleaning
// ".." segments. Absolute references and escapes are rejected outright.
func checkContained(root, ref string) error {
	if filepath.IsAbs(ref) || path.IsAbs(ref) {
		return fmt.Errorf("reference %q must be relative to segment root %q", ref, root)
	}
	cleanRef := path.Clean(ref)
	if cleanRef == ".." || strings.HasPrefix(cleanRef, "../") {
		return fmt.Errorf("reference %q escapes segment root %q", ref, root)
	}
	joined := path.Clean(path.Join(root, cleanRef))
	rootClean := path.Clean(root)
	if joined != rootClean && !strings.HasPrefix(joined, rootClean+"/") {
		return fmt.Errorf("reference %q resolves outside segment root %q", ref, root)
	}
	return nil
}
