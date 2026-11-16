// Package mpd renders manifest models to XML and expands DASH segment
// templates into concrete media paths.
package mpd

import (
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
)

// SegmentRef carries the substitution values for one media segment.
type SegmentRef struct {
	RepID  string
	Number int64
	Time   int64
}

// ExpandedSegment pairs a segment reference with its rendered relative path.
type ExpandedSegment struct {
	RepID  string
	Number int64
	Time   int64
	Path   string
}

// supported placeholders, per DASH ISO/IEC 23009-1 $...$ template syntax.
const (
	phRepID = "RepresentationID"
	phNum   = "Number"
	phTime  = "Time"
)

// Expand renders one media path from template. Supported tokens are
// $RepresentationID$, $Number$, $Number%0Nd$, $Time$ and the escaped $$.
// Any other $...$ token or an unterminated placeholder is an error.
func Expand(template string, ref SegmentRef) (string, error) {
	if template == "" {
		return "", fmt.Errorf("template: empty media template")
	}
	var b strings.Builder
	for i := 0; i < len(template); {
		c := template[i]
		if c != '$' {
			b.WriteByte(c)
			i++
			continue
		}
		if i+1 < len(template) && template[i+1] == '$' {
			b.WriteByte('$')
			i += 2
			continue
		}
		end := strings.IndexByte(template[i+1:], '$')
		if end < 0 {
			return "", fmt.Errorf("template: unterminated placeholder in %q", template)
		}
		token := template[i+1 : i+1+end]
		value, err := substitute(token, ref)
		if err != nil {
			return "", err
		}
		b.WriteString(value)
		i += end + 2
	}
	return SafePath(b.String())
}

func substitute(token string, ref SegmentRef) (string, error) {
	name, width, hasWidth, err := parseToken(token)
	if err != nil {
		return "", err
	}
	switch name {
	case phRepID:
		if hasWidth {
			return "", fmt.Errorf("template: %s does not accept a width format", phRepID)
		}
		return ref.RepID, nil
	case phNum:
		return formatWidth(phNum, ref.Number, width, hasWidth), nil
	case phTime:
		return formatWidth(phTime, ref.Time, width, hasWidth), nil
	default:
		return "", fmt.Errorf("template: unknown placeholder $%s$", token)
	}
}

// parseToken splits "Number%05d" into ("Number", 5, true, nil).
func parseToken(token string) (name string, width int, hasWidth bool, err error) {
	pct := strings.IndexByte(token, '%')
	if pct < 0 {
		return token, 0, false, nil
	}
	name = token[:pct]
	spec := token[pct:]
	if len(spec) < 4 || spec[0] != '%' || spec[1] != '0' || spec[len(spec)-1] != 'd' {
		return "", 0, false, fmt.Errorf("template: invalid width spec %q (want %%0Nd)", spec)
	}
	digits := spec[2 : len(spec)-1]
	if digits == "" {
		return "", 0, false, fmt.Errorf("template: empty width in %q", token)
	}
	for _, c := range digits {
		if c < '0' || c > '9' {
			return "", 0, false, fmt.Errorf("template: width must be digits in %q", token)
		}
	}
	w, err := strconv.Atoi(digits)
	if err != nil || w <= 0 || w > 18 {
		return "", 0, false, fmt.Errorf("template: width out of range 1..18 in %q", token)
	}
	return name, w, true, nil
}

func formatWidth(name string, v int64, width int, hasWidth bool) string {
	if !hasWidth {
		return strconv.FormatInt(v, 10)
	}
	return fmt.Sprintf("%0*d", width, v)
}

// SafePath rejects absolute paths, NUL bytes and any "."/".." component so a
// template or representation ID can never escape the output directory.
func SafePath(p string) (string, error) {
	if p == "" {
		return "", fmt.Errorf("template: expanded path is empty")
	}
	if strings.ContainsRune(p, 0) {
		return "", fmt.Errorf("template: NUL byte in path %q", p)
	}
	if filepath.IsAbs(p) || strings.HasPrefix(p, "/") || strings.HasPrefix(p, `\`) {
		return "", fmt.Errorf("template: absolute paths are not allowed: %q", p)
	}
	for _, comp := range strings.FieldsFunc(p, func(r rune) bool { return r == '/' || r == '\\' }) {
		if comp == "." || comp == ".." {
			return "", fmt.Errorf("template: path traversal component in %q", p)
		}
	}
	return p, nil
}

// ExpandAll renders every reference and guarantees no two segments map to the
// same output path. Results preserve the input order for stable reporting.
func ExpandAll(template string, refs []SegmentRef) ([]ExpandedSegment, error) {
	out := make([]ExpandedSegment, 0, len(refs))
	seen := make(map[string]SegmentRef, len(refs))
	for _, ref := range refs {
		path, err := Expand(template, ref)
		if err != nil {
			return nil, fmt.Errorf("template: rep %q segment %d: %w", ref.RepID, ref.Number, err)
		}
		if first, dup := seen[path]; dup {
			return nil, fmt.Errorf("template: duplicate output path %q (rep %q segment %d and rep %q segment %d)",
				path, first.RepID, first.Number, ref.RepID, ref.Number)
		}
		seen[path] = ref
		out = append(out, ExpandedSegment{RepID: ref.RepID, Number: ref.Number, Time: ref.Time, Path: path})
	}
	return out, nil
}
