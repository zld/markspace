package ingest

import (
	"fmt"
	"strings"

	"mediad/internal/core"
	"mediad/internal/diagnose"
)

// ParseVTT parses WebVTT. It tolerates a missing header by reporting it, and
// it skips NOTE/STYLE/REGION blocks. Invalid timing lines are reported with
// their byte span and the cue body is skipped so parsing resumes at the next
// cue. WebVTT timestamps may omit the hour component.
func ParseVTT(asset *Asset) (*Subs, error) {
	body, shift, bom, badBOM := stripBOM(asset.Data)
	subs := &Subs{Asset: asset.Name, Format: "vtt", BOM: bom}
	if badBOM != nil {
		subs.Issues.Add(diagnose.Issue{
			Category: diagnose.CatUnsupported, Stage: diagnose.StageIngest,
			Format: "vtt", Asset: asset.Name, Span: badBOM.span,
			Detail: "UTF-16 subtitle requires transcoding to UTF-8",
		})
		return subs, nil
	}
	if off := firstInvalidUTF8(body); off >= 0 {
		subs.Issues.Add(diagnose.Issue{
			Category: diagnose.CatUnsupported, Stage: diagnose.StageIngest,
			Format: "vtt", Asset: asset.Name,
			Span:   core.Span{Start: off + shift, End: off + shift + 1},
			Detail: "invalid UTF-8 in subtitle body", Recoverable: true,
		})
	}
	lines := splitLines(body)
	// Locate the header on the first non-blank line.
	start := 0
	for start < len(lines) && blankLine(lines[start].Text) {
		start++
	}
	if start == len(lines) || !strings.HasPrefix(strings.TrimSpace(lines[start].Text), "WEBVTT") {
		span := core.Span{Start: 0, End: 0}
		if start < len(lines) {
			span = core.Span{Start: lines[start].Start + shift, End: lines[start].End + shift}
		}
		subs.Issues.Add(diagnose.Issue{
			Category: diagnose.CatVTTTime, Stage: diagnose.StageIngest,
			Format: "vtt", Asset: asset.Name, Span: span,
			Detail: "missing WEBVTT header", Recoverable: true,
		})
	} else {
		if rest := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(lines[start].Text), "WEBVTT")); rest != "" && !strings.HasPrefix(rest, "-") && !strings.HasPrefix(rest, " ") && !strings.HasPrefix(rest, "kind:") {
			subs.Issues.Add(diagnose.Issue{
				Category: diagnose.CatVTTTime, Stage: diagnose.StageIngest,
				Format: "vtt", Asset: asset.Name,
				Span:   core.Span{Start: lines[start].Start + shift, End: lines[start].End + shift},
				Detail: "WEBVTT header must be followed by space, tab or end of line", Recoverable: true,
			})
		}
		start++
	}
	for i := start; i < len(lines); i++ {
		ln := lines[i]
		if blankLine(ln.Text) {
			continue
		}
		trimmed := strings.TrimSpace(ln.Text)
		switch {
		case strings.HasPrefix(trimmed, "NOTE"):
			for i+1 < len(lines) && !blankLine(lines[i+1].Text) {
				i++
			}
			continue
		case trimmed == "STYLE", trimmed == "REGION":
			for i+1 < len(lines) && !blankLine(lines[i+1].Text) {
				i++
			}
			continue
		}
		cueStart := ln.Start + shift
		id := ""
		if !strings.Contains(ln.Text, "-->") {
			id = ln.Text
			i++
			if i >= len(lines) {
				subs.Issues.Add(truncatedCue(asset.Name, "vtt", cueStart, int64(len(asset.Data)), "cue identifier without timing"))
				break
			}
			if !strings.Contains(lines[i].Text, "-->") {
				subs.Issues.Add(diagnose.Issue{
					Category: diagnose.CatVTTTime, Stage: diagnose.StageIngest,
					Format: "vtt", Asset: asset.Name,
					Span:   core.Span{Start: lines[i].Start + shift, End: lines[i].End + shift},
					Detail: "cue identifier not followed by a timing line", Recoverable: true,
				})
				continue
			}
			ln = lines[i]
		}
		timing := strings.TrimSpace(ln.Text)
		top, bottom, settings, err := parseVTTTiming(timing)
		timingSpan := core.Span{Start: ln.Start + shift, End: ln.End + shift}
		if err != nil {
			subs.Issues.Add(diagnose.Issue{
				Category: diagnose.CatVTTTime, Stage: diagnose.StageIngest,
				Format: "vtt", Asset: asset.Name, Span: timingSpan,
				Detail: err.Error(), Recoverable: true,
			})
			for i+1 < len(lines) && !blankLine(lines[i+1].Text) {
				i++
			}
			continue
		}
		if bottom.Cmp(top) <= 0 {
			subs.Issues.Add(diagnose.Issue{
				Category: diagnose.CatVTTTime, Stage: diagnose.StageIngest,
				Format: "vtt", Asset: asset.Name, Span: timingSpan,
				Detail: "cue end must be after cue start", Recoverable: true,
			})
			for i+1 < len(lines) && !blankLine(lines[i+1].Text) {
				i++
			}
			continue
		}
		cue := Cue{
			Index:      len(subs.Cues) + 1,
			ID:         id,
			Top:        top,
			Bottom:     bottom,
			Settings:   settings,
			Precision:  "milliseconds",
			Span:       core.Span{Start: cueStart, End: ln.End + shift},
			TimingSpan: timingSpan,
		}
		textStart, textEnd := int64(-1), int64(-1)
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
				subs.Issues.Add(truncatedCue(asset.Name, "vtt", b.Start+shift, b.End+shift, "cue body has no terminating newline"))
			}
		}
		if textStart >= 0 {
			cue.TextSpan = core.Span{Start: textStart, End: textEnd}
		}
		subs.Cues = append(subs.Cues, cue)
	}
	return subs, nil
}

// parseVTTTiming parses "MM:SS.mmm" or "HH:MM:SS.mmm" around "-->", plus
// trailing cue settings such as "align:start position:10%".
func parseVTTTiming(line string) (top, bottom core.Rational, settings []string, err error) {
	parts := strings.SplitN(line, "-->", 2)
	if len(parts) != 2 {
		return top, bottom, nil, fmt.Errorf("no timing arrow")
	}
	left := strings.TrimSpace(parts[0])
	rest := strings.Fields(strings.TrimSpace(parts[1]))
	if len(rest) == 0 {
		return top, bottom, nil, fmt.Errorf("missing end time")
	}
	top, err = parseVTTStamp(left)
	if err != nil {
		return top, bottom, nil, fmt.Errorf("start: %w", err)
	}
	bottom, err = parseVTTStamp(rest[0])
	if err != nil {
		return top, bottom, nil, fmt.Errorf("end: %w", err)
	}
	for _, s := range rest[1:] {
		if !strings.Contains(s, ":") {
			return top, bottom, nil, fmt.Errorf("malformed cue setting %q", s)
		}
		settings = append(settings, s)
	}
	return top, bottom, settings, nil
}

func parseVTTStamp(s string) (core.Rational, error) {
	if s == "" {
		return core.Zero(), fmt.Errorf("empty timestamp")
	}
	if strings.ContainsAny(s, ",;") {
		return core.Zero(), fmt.Errorf("WebVTT timestamps use '.' before milliseconds")
	}
	fields := strings.Split(s, ":")
	if len(fields) != 2 && len(fields) != 3 {
		return core.Zero(), fmt.Errorf("timestamp %q needs MM:SS.mmm or HH:MM:SS.mmm", s)
	}
	secField := fields[len(fields)-1]
	sec, ms, err := splitVTTSeconds(secField)
	if err != nil {
		return core.Zero(), fmt.Errorf("%q: %w", s, err)
	}
	if sec > 59 {
		return core.Zero(), fmt.Errorf("seconds out of range in %q", s)
	}
	minIdx := 0
	var h int
	if len(fields) == 3 {
		h, err = parseFixedInt(fields[0], 4)
		if err != nil {
			return core.Zero(), fmt.Errorf("bad hours in %q", s)
		}
		minIdx = 1
	}
	m, err := parseFixedInt(fields[minIdx], 2)
	if err != nil || m > 59 {
		return core.Zero(), fmt.Errorf("bad minutes in %q", s)
	}
	return core.FromMillis((int64(h)*3600+int64(m)*60+int64(sec))*1000 + ms), nil
}

func splitVTTSeconds(s string) (int, int64, error) {
	dot := strings.IndexByte(s, '.')
	if dot < 0 {
		return 0, 0, fmt.Errorf("missing milliseconds")
	}
	sec, err := parseFixedInt(s[:dot], 2)
	if err != nil {
		return 0, 0, err
	}
	frac := s[dot+1:]
	if len(frac) != 3 {
		return 0, 0, fmt.Errorf("milliseconds need exactly 3 digits")
	}
	v, err := parseFixedInt(frac, 3)
	if err != nil {
		return 0, 0, err
	}
	return sec, int64(v), nil
}
