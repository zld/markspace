package ingest

import (
	"fmt"
	"strconv"
	"strings"

	"mediad/internal/core"
	"mediad/internal/diagnose"
)

// Cue is one parsed subtitle record. Top and Bottom are exact rational
// media-clock seconds derived from the cue's own timing line.
type Cue struct {
	Index      int           `json:"index"`
	ID         string        `json:"id,omitempty"`
	Top        core.Rational `json:"start"`
	Bottom     core.Rational `json:"end"`
	Lines      []string      `json:"lines"`
	Settings   []string      `json:"settings,omitempty"`
	Span       core.Span     `json:"span"`
	TimingSpan core.Span     `json:"timing_span"`
	TextSpan   core.Span     `json:"text_span"`
	Precision  string        `json:"precision"`
}

// Duration returns Bottom-Top.
func (c Cue) Duration() core.Rational { return c.Bottom.Sub(c.Top) }

// Subs holds all cues from one subtitle asset plus non-fatal issues.
type Subs struct {
	Asset  string          `json:"asset"`
	Format string          `json:"format"`
	Cues   []Cue           `json:"cues"`
	Issues diagnose.Issues `json:"-"`
	BOM    string          `json:"bom,omitempty"`
}

// ParseSRT parses SRT subtitles. A cue whose timing line is invalid is
// reported at its byte span and skipped; known-length cue bodies are skipped
// as well so the following cue is still parsed. A truncated final body is
// reported and its text retained.
func ParseSRT(asset *Asset) (*Subs, error) {
	body, shift, bom, badBOM := stripBOM(asset.Data)
	subs := &Subs{Asset: asset.Name, Format: "srt", BOM: bom}
	if badBOM != nil {
		subs.Issues.Add(diagnose.Issue{
			Category: diagnose.CatUnsupported, Stage: diagnose.StageIngest,
			Format: "srt", Asset: asset.Name, Span: badBOM.span,
			Detail: "UTF-16 subtitle requires transcoding to UTF-8",
		})
		return subs, nil
	}
	if off := firstInvalidUTF8(body); off >= 0 {
		subs.Issues.Add(diagnose.Issue{
			Category: diagnose.CatUnsupported, Stage: diagnose.StageIngest,
			Format: "srt", Asset: asset.Name,
			Span:   core.Span{Start: off + shift, End: off + shift + 1},
			Detail: "invalid UTF-8 in subtitle body", Recoverable: true,
		})
	}
	lines := splitLines(body)
	for i := 0; i < len(lines); i++ {
		ln := lines[i]
		if blankLine(ln.Text) {
			continue
		}
		cueStart := ln.Start + shift
		// Optional numeric counter line.
		if _, err := strconv.Atoi(strings.TrimSpace(ln.Text)); err == nil {
			i++
			if i >= len(lines) {
				subs.Issues.Add(truncatedCue(asset.Name, "srt", cueStart, int64(len(asset.Data)), "cue index without timing"))
				break
			}
			ln = lines[i]
		}
		if !strings.Contains(ln.Text, "-->") {
			subs.Issues.Add(diagnose.Issue{
				Category: diagnose.CatSRTTime, Stage: diagnose.StageIngest,
				Format: "srt", Asset: asset.Name,
				Span:   core.Span{Start: ln.Start + shift, End: ln.End + shift},
				Detail: "missing '-->' timing arrow", Recoverable: true,
			})
			continue
		}
		top, bottom, precision, err := parseSRTTiming(ln.Text)
		if err != nil {
			subs.Issues.Add(diagnose.Issue{
				Category: diagnose.CatSRTTime, Stage: diagnose.StageIngest,
				Format: "srt", Asset: asset.Name,
				Span:   core.Span{Start: ln.Start + shift, End: ln.End + shift},
				Detail: err.Error(), Recoverable: true,
			})
			// Skip the body so its text cannot be mistaken for a new cue.
			for i+1 < len(lines) && !blankLine(lines[i+1].Text) {
				i++
			}
			continue
		}
		if bottom.Cmp(top) < 0 {
			subs.Issues.Add(diagnose.Issue{
				Category: diagnose.CatSRTTime, Stage: diagnose.StageIngest,
				Format: "srt", Asset: asset.Name,
				Span:   core.Span{Start: ln.Start + shift, End: ln.End + shift},
				Detail: "end time precedes start time", Recoverable: true,
			})
			for i+1 < len(lines) && !blankLine(lines[i+1].Text) {
				i++
			}
			continue
		}
		cue := Cue{
			Index:      len(subs.Cues) + 1,
			Top:        top,
			Bottom:     bottom,
			Precision:  precision,
			Span:       core.Span{Start: cueStart, End: ln.End + shift},
			TimingSpan: core.Span{Start: ln.Start + shift, End: ln.End + shift},
		}
		var textStart, textEnd int64 = -1, -1
		for i+1 < len(lines) && !blankLine(lines[i+1].Text) {
			i++
			b := lines[i]
			if textStart < 0 {
				textStart = b.Start + shift
			}
			textEnd = b.End + shift
			cue.Lines = append(cue.Lines, b.Text)
			cue.Span.End = b.End + shift
			if !b.Terminated && i == len(lines)-1 {
				subs.Issues.Add(truncatedCue(asset.Name, "srt", b.Start+shift, b.End+shift, "cue body has no terminating newline"))
			}
		}
		if textStart >= 0 {
			cue.TextSpan = core.Span{Start: textStart, End: textEnd}
		}
		subs.Cues = append(subs.Cues, cue)
	}
	return subs, nil
}

func truncatedCue(asset, format string, start, end int64, detail string) diagnose.Issue {
	return diagnose.Issue{
		Category: diagnose.CatTruncatedCue, Stage: diagnose.StageIngest,
		Format: format, Asset: asset, Span: core.Span{Start: start, End: end},
		Detail: detail, Recoverable: true,
	}
}

// parseSRTTiming accepts "HH:MM:SS,mmm --> HH:MM:SS,mmm" and the
// microsecond form, with optional trailing cue settings.
func parseSRTTiming(line string) (top, bottom core.Rational, precision string, err error) {
	parts := strings.SplitN(line, "-->", 2)
	if len(parts) != 2 {
		return top, bottom, "", fmt.Errorf("no timing arrow")
	}
	left := strings.TrimSpace(parts[0])
	rest := strings.Fields(strings.TrimSpace(parts[1]))
	if len(rest) == 0 {
		return top, bottom, "", fmt.Errorf("missing end time")
	}
	right := rest[0]
	top, precision, err = parseSRTStamp(left, true)
	if err != nil {
		return top, bottom, "", fmt.Errorf("start: %w", err)
	}
	bottom, p2, err := parseSRTStamp(right, false)
	if err != nil {
		return top, bottom, "", fmt.Errorf("end: %w", err)
	}
	if p2 == "microseconds" {
		precision = "microseconds"
	}
	return top, bottom, precision, nil
}

func parseSRTStamp(s string, isStart bool) (core.Rational, string, error) {
	if s == "" {
		return core.Zero(), "", fmt.Errorf("empty timestamp")
	}
	fields := strings.Split(s, ":")
	if len(fields) != 3 {
		return core.Zero(), "", fmt.Errorf("timestamp %q needs HH:MM:SS", s)
	}
	h, err := parseFixedInt(fields[0], 2)
	if err != nil {
		return core.Zero(), "", fmt.Errorf("bad hours in %q", s)
	}
	m, err := parseFixedInt(fields[1], 2)
	if err != nil || m > 59 {
		return core.Zero(), "", fmt.Errorf("bad minutes in %q", s)
	}
	sec, frac, precision, err := splitSeconds(fields[2])
	if err != nil || sec > 59 {
		return core.Zero(), "", fmt.Errorf("bad seconds in %q", s)
	}
	total := (int64(h)*3600+int64(m)*60+int64(sec))*1000 + frac
	if precision == "microseconds" {
		return core.FromMicros((int64(h)*3600+int64(m)*60+int64(sec))*1000000 + frac), precision, nil
	}
	return core.FromMillis(total), precision, nil
}

func splitSeconds(s string) (int, int64, string, error) {
	comma := strings.IndexAny(s, ",.")
	if comma < 0 {
		return 0, 0, "", fmt.Errorf("missing fractional seconds")
	}
	sec, err := parseFixedInt(s[:comma], 2)
	if err != nil {
		return 0, 0, "", err
	}
	frac := s[comma+1:]
	if len(frac) != 3 && len(frac) != 6 {
		return 0, 0, "", fmt.Errorf("fraction needs 3 or 6 digits")
	}
	for i := 0; i < len(frac); i++ {
		if frac[i] < '0' || frac[i] > '9' {
			return 0, 0, "", fmt.Errorf("non-digit fraction")
		}
	}
	v, err := parseFixedInt(frac, len(frac))
	if err != nil {
		return 0, 0, "", err
	}
	precision := "milliseconds"
	if len(frac) == 6 {
		precision = "microseconds"
	}
	return sec, int64(v), precision, nil
}

func parseFixedInt(s string, maxDigits int) (int, error) {
	if s == "" || len(s) > maxDigits {
		return 0, fmt.Errorf("want at most %d digits", maxDigits)
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return 0, fmt.Errorf("non-digit")
		}
	}
	return strconv.Atoi(s)
}
