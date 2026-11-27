package subtitle

import (
	"strings"

	"media.rebuild/internal/model"
)

// parseSRT reads SubRip text. A well-formed cue is an optional numeric index
// line, a timing line "start --> end" (settings after the end time are
// ignored), then one or more text lines terminated by a blank line. Blank
// caption bodies are reported as errors rather than turning into empty cues.
func parseSRT(source, text string) *Track {
	t := &Track{Source: source, Kind: KindSRT}
	lines := splitLines(text)
	for i := 0; i < len(lines); {
		// Skip blank separators between cues.
		if strings.TrimSpace(lines[i]) == "" {
			i++
			continue
		}
		cueLine := i + 1
		idx := -1
		cursor := i
		if isAllDigits(strings.TrimSpace(lines[cursor])) {
			var ok bool
			idx, ok = parseInt(strings.TrimSpace(lines[cursor]))
			if !ok {
				t.addError(model.New(model.CodeSubtitleSyntax, model.Source{Line: cursor + 1}, "cue index out of range", nil))
				i = skipCue(lines, cursor+1)
				continue
			}
			cursor++
		}
		if cursor >= len(lines) {
			t.addError(model.New(model.CodeSubtitleSyntax, model.Source{Line: cueLine}, "cue has an index but no timing line", nil))
			break
		}
		start, end, err := parseTimingLine(lines[cursor], model.Source{Name: source, Line: cursor + 1})
		if err != nil {
			t.addError(err)
			i = skipCue(lines, cursor+1)
			continue
		}
		cursor++
		bodyStart := cursor
		for cursor < len(lines) && strings.TrimSpace(lines[cursor]) != "" {
			cursor++
		}
		body := strings.TrimSpace(strings.Join(lines[bodyStart:cursor], "\n"))
		if body == "" {
			t.addError(model.New(model.CodeSubtitleSyntax, model.Source{Line: cueLine}, "cue has no text", nil))
			i = cursor
			continue
		}
		if end <= start {
			t.addError(model.Newf(model.CodeSubtitleTiming, model.Source{Line: cueLine},
				"cue [%d,%d) has non-positive duration", start, end))
			i = cursor
			continue
		}
		t.Cues = append(t.Cues, Cue{
			Start: start, End: end, Text: body,
			Source: source, Line: cueLine, LineEnd: max(cueLine, cursor),
			Kind: KindSRT, Index: idx,
		})
		i = cursor
	}
	t.finalize()
	return t
}

// skipCue advances to the first blank line after from, so a malformed cue does
// not swallow subsequent valid cues.
func skipCue(lines []string, from int) int {
	for from < len(lines) && strings.TrimSpace(lines[from]) != "" {
		from++
	}
	return from
}

// parseTimingLine splits "start --> end [settings]".
func parseTimingLine(line string, src model.Source) (int64, int64, *model.Error) {
	arrow := strings.Index(line, "-->")
	if arrow < 0 {
		return 0, 0, model.Newf(model.CodeSubtitleSyntax, src, "missing '-->' in timing line %q", strings.TrimSpace(line))
	}
	left := strings.TrimSpace(line[:arrow])
	right := strings.TrimSpace(line[arrow+3:])
	start, n, err := clockTime(left, src)
	if err != nil {
		return 0, 0, err
	}
	if left[n:] != "" {
		return 0, 0, model.Newf(model.CodeSubtitleTiming, src, "unexpected trailing text in start time %q", left)
	}
	end, n, err := clockTime(right, src)
	if err != nil {
		return 0, 0, err
	}
	if rest := strings.TrimSpace(right[n:]); rest != "" && !isVTTTimingSettings(rest) {
		return 0, 0, model.Newf(model.CodeSubtitleTiming, src, "unexpected trailing text in end time %q", right)
	}
	return start, end, nil
}

// isVTTTimingSettings accepts WebVTT cue settings that may follow the end time.
func isVTTTimingSettings(rest string) bool {
	for _, f := range strings.Fields(rest) {
		key, _, ok := strings.Cut(f, ":")
		if !ok {
			return false
		}
		switch key {
		case "line", "position", "size", "align", "region", "vertical":
		default:
			return false
		}
	}
	return true
}

func isAllDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

func parseInt(s string) (int, bool) {
	if len(s) > 9 {
		return 0, false
	}
	v := 0
	for _, c := range s {
		v = v*10 + int(c-'0')
	}
	return v, true
}
