// Package subtitle parses SRT and WebVTT caption files into a deterministic,
// sorted cue track.
//
// Both parsers share one policy for damaged input: a malformed cue is recorded
// as a typed error carrying the source line number and parsing continues with
// the next cue. Nothing is silently dropped: every byte of the input either
// belongs to a reported cue or to a reported error's line.
package subtitle

import (
	"sort"
	"strings"

	"media.rebuild/internal/model"
)

// Kind identifies the caption container format.
type Kind string

const (
	KindSRT Kind = "srt"
	KindVTT Kind = "vtt"
)

// Cue is one caption interval. Start and End are int64 nanoseconds, EndExclusive
// is true when the source uses WebVTT's "start --> end" inclusive end. Source
// line numbers are 1-based; LineEnd is the last line of the cue payload.
type Cue struct {
	Index   int    `json:"index"`
	Start   int64  `json:"start_ns"`
	End     int64  `json:"end_ns"`
	Text    string `json:"text"`
	Source  string `json:"source"`
	Line    int    `json:"line"`
	LineEnd int    `json:"line_end"`
	Kind    Kind   `json:"kind"`
}

// Track is the result of parsing one caption file.
type Track struct {
	Source string         `json:"source"`
	Kind   Kind           `json:"kind"`
	Cues   []Cue          `json:"cues"`
	Errors []*model.Error `json:"errors"`
}

// Parsed reports whether at least one usable cue survived.
func (t *Track) Parsed() bool { return len(t.Cues) > 0 }

// finalize sorts cues deterministically and orders errors by line. Cues sort
// by start, then end, then original file line, so two runs over the same bytes
// always produce an identical track.
func (t *Track) finalize() {
	sort.SliceStable(t.Cues, func(i, j int) bool {
		a, b := t.Cues[i], t.Cues[j]
		if a.Start != b.Start {
			return a.Start < b.Start
		}
		if a.End != b.End {
			return a.End < b.End
		}
		return a.Line < b.Line
	})
	for i := range t.Cues {
		t.Cues[i].Index = i
	}
	model.SortErrors(t.Errors)
}

// addError appends a source-named error.
func (t *Track) addError(err *model.Error) {
	t.Errors = append(t.Errors, err.WithSourceName(t.Source))
}

// splitLines normalizes CRLF and CR to LF and drops a trailing newline so line
// numbers count content lines exactly as an editor shows them.
func splitLines(text string) []string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	text = strings.TrimSuffix(text, "\n")
	if text == "" {
		return nil
	}
	return strings.Split(text, "\n")
}

// Detect guesses the caption format from content. A WebVTT signature wins;
// otherwise the file is treated as SRT.
func Detect(text string) Kind {
	for _, line := range splitLines(text) {
		line = strings.TrimSpace(strings.TrimPrefix(line, "\ufeff"))
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "WEBVTT") {
			return KindVTT
		}
		break
	}
	return KindSRT
}

// Parse dispatches on Kind.
func Parse(kind Kind, source, text string) (*Track, error) {
	if kind != KindSRT && kind != KindVTT {
		return nil, model.Newf(model.CodeInvalidArgument, model.Source{Name: source, Offset: -1}, "unknown subtitle kind %q", kind)
	}
	if strings.TrimSpace(text) == "" {
		t := &Track{Source: source, Kind: kind}
		t.addError(model.New(model.CodeSubtitleSyntax, model.AtLine(source, 1), "empty caption file", nil))
		return t, nil
	}
	if kind == KindSRT {
		return parseSRT(source, text), nil
	}
	return parseVTT(source, text), nil
}

// clockTime parses "HH:MM:SS,mmm" / "HH:MM:SS.mmm" / "MM:SS.mmm" into ns.
// It returns the bytes consumed and a typed error on malformed input.
func clockTime(s string, src model.Source) (int64, int, *model.Error) {
	i := 0
	var parts [3]int64
	nparts := 0
	for nparts < 3 {
		start := i
		for i < len(s) && s[i] >= '0' && s[i] <= '9' {
			i++
		}
		if i == start {
			return 0, i, model.Newf(model.CodeSubtitleTiming, src, "expected digit at column %d of %q", start+1, s)
		}
		if i-start > 4 {
			return 0, i, model.Newf(model.CodeSubtitleTiming, src, "numeric field too long in %q", s)
		}
		v := int64(0)
		for _, c := range s[start:i] {
			v = v*10 + int64(c-'0')
		}
		parts[nparts] = v
		nparts++
		if nparts == 3 {
			break
		}
		if i >= len(s) || s[i] != ':' {
			// Allow the SRT millisecond separator to be ',' or '.'.
			return 0, i, model.Newf(model.CodeSubtitleTiming, src, "expected ':' in timestamp %q", s)
		}
		i++
	}
	if i >= len(s) || (s[i] != ',' && s[i] != '.') {
		return 0, i, model.Newf(model.CodeSubtitleTiming, src, "missing millisecond separator in %q", s)
	}
	i++
	mstart := i
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		i++
	}
	if m := i - mstart; m != 3 {
		return 0, i, model.Newf(model.CodeSubtitleTiming, src, "milliseconds must be exactly three digits in %q", s)
	}
	ms := int64(s[mstart]-'0')*100 + int64(s[mstart+1]-'0')*10 + int64(s[mstart+2]-'0')
	h, m, sec := parts[0], parts[1], parts[2]
	if m > 59 || sec > 59 {
		return 0, i, model.Newf(model.CodeSubtitleTiming, src, "minute/second out of range in %q", s)
	}
	ns := ((h*3600+m*60+sec)*1_000_000_000 + ms*1_000_000)
	return ns, i, nil
}
