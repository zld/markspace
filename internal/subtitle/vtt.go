package subtitle

import (
	"strings"

	"media.rebuild/internal/model"
)

// parseVTT reads WebVTT. The signature line must open the file; NOTE, STYLE
// and REGION blocks are skipped; each cue is an optional identifier line, a
// timing line, and text up to a blank line. Cue text may contain markup, kept
// verbatim. A cue ending exactly when the next starts is legal in WebVTT.
func parseVTT(source, text string) *Track {
	t := &Track{Source: source, Kind: KindVTT}
	lines := splitLines(text)
	i := 0
	if len(lines) == 0 || !strings.HasPrefix(strings.TrimSpace(strings.TrimPrefix(lines[0], "\ufeff")), "WEBVTT") {
		t.addError(model.New(model.CodeSubtitleSyntax, model.AtLine(source, 1), "missing WEBVTT signature", nil))
		// Keep parsing the remainder as cues: a missing header should not
		// discard otherwise valid captions.
	} else {
		i = 1
		// The signature may be followed by header metadata on the same line
		// or on header lines up to the first blank line.
		if i < len(lines) && strings.TrimSpace(lines[i]) != "" && !strings.Contains(lines[i], "-->") {
			for i < len(lines) && strings.TrimSpace(lines[i]) != "" && !strings.Contains(lines[i], "-->") {
				i++
			}
		}
	}
	for i < len(lines) {
		if strings.TrimSpace(lines[i]) == "" {
			i++
			continue
		}
		line := strings.TrimSpace(lines[i])
		if strings.HasPrefix(line, "NOTE") || line == "STYLE" || line == "REGION" {
			i = skipCue(lines, i+1)
			continue
		}
		cueLine := i + 1
		cursor := i
		if !strings.Contains(lines[cursor], "-->") {
			cursor++ // identifier line
			if cursor >= len(lines) {
				t.addError(model.New(model.CodeSubtitleSyntax, model.AtLine(source, cueLine), "cue identifier has no timing line", nil))
				break
			}
		}
		start, end, err := parseTimingLine(lines[cursor], model.AtLine(source, cursor+1))
		if err != nil {
			t.addError(err)
			i = skipCue(lines, cursor+1)
			continue
		}
		cursor++
		bodyStart := cursor
		for cursor < len(lines) && strings.TrimSpace(lines[cursor]) != "" {
			if strings.Contains(lines[cursor], "-->") {
				break // a new cue without a separating blank line
			}
			cursor++
		}
		body := strings.TrimSpace(strings.Join(lines[bodyStart:cursor], "\n"))
		if body == "" {
			t.addError(model.New(model.CodeSubtitleSyntax, model.AtLine(source, cueLine), "cue has no text", nil))
			i = cursor
			continue
		}
		if end < start {
			t.addError(model.Newf(model.CodeSubtitleTiming, model.AtLine(source, cueLine),
				"cue [%d,%d) ends before it starts", start, end))
			i = cursor
			continue
		}
		if end == start {
			// WebVTT permits zero-duration cues but they convey no visible
			// interval; report them so the review is not silently distorted.
			t.addError(model.Newf(model.CodeSubtitleTiming, model.AtLine(source, cueLine),
				"cue [%d,%d) has zero duration", start, end))
			i = cursor
			continue
		}
		t.Cues = append(t.Cues, Cue{
			Start: start, End: end, Text: body,
			Source: source, Line: cueLine, LineEnd: max(cueLine, cursor),
			Kind: KindVTT,
		})
		i = cursor
	}
	t.finalize()
	return t
}

// Overlap reports pairs of cues whose intervals intersect. It is used by the
// timeline stage to flag captions that cannot be shown together.
func Overlap(cues []Cue) [][2]int {
	var pairs [][2]int
	for i := 1; i < len(cues); i++ {
		// Sorted by start: overlap can only happen with a recent predecessor
		// whose end is after this start.
		for j := i - 1; j >= 0 && cues[j].End > cues[i].Start; j-- {
			if cues[i].End > cues[j].Start {
				pairs = append(pairs, [2]int{j, i})
			}
		}
	}
	return pairs
}
