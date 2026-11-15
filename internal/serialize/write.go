// Package serialize renders the unified cue model back into SRT, WebVTT or
// ASS text. Output is deterministically ordered and every format-specific
// escaping or precision rule is handled here.
package serialize

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"subsync/internal/model"
)

// Write renders cues in the requested format. Cue order is stabilized first
// by track, then start time, then original order. When the target cannot
// represent ASS styles, doc.StyleLoss is updated for reporting.
func Write(doc *model.Document, target model.Format) (string, error) {
	cues := append([]model.Cue(nil), doc.Cues...)
	sort.SliceStable(cues, func(i, j int) bool {
		if cues[i].Track != cues[j].Track {
			return cues[i].Track < cues[j].Track
		}
		if cues[i].Start != cues[j].Start {
			return cues[i].Start < cues[j].Start
		}
		return cues[i].Order < cues[j].Order
	})

	switch target {
	case model.FormatSRT:
		return writeSRT(cues), nil
	case model.FormatVTT:
		return writeVTT(cues), nil
	case model.FormatASS:
		return writeASS(doc, cues), nil
	default:
		return "", fmt.Errorf("no serializer registered for format %q", target)
	}
}

// writeSRT emits millisecond precision "HH:MM:SS,mmm" timestamps and blank
// line separated numbered blocks.
func writeSRT(cues []model.Cue) string {
	var b strings.Builder
	for i, c := range cues {
		fmt.Fprintf(&b, "%d\n", i+1)
		fmt.Fprintf(&b, "%s --> %s\n", srtTime(c.Start), srtTime(c.End))
		b.WriteString(c.Text)
		b.WriteString("\n\n")
	}
	return b.String()
}

// writeVTT always starts with the WEBVTT header and uses millisecond
// precision with '.' as the fractional separator.
func writeVTT(cues []model.Cue) string {
	var b strings.Builder
	b.WriteString("WEBVTT\n\n")
	for i, c := range cues {
		if c.ID != "" {
			fmt.Fprintf(&b, "%s\n", escapeVTTCueID(c.ID))
		} else {
			fmt.Fprintf(&b, "%d\n", i+1)
		}
		fmt.Fprintf(&b, "%s --> %s\n", vttTime(c.Start), vttTime(c.End))
		b.WriteString(escapeVTTText(c.Text))
		b.WriteString("\n\n")
	}
	return b.String()
}

// writeASS re-emits a complete script. Style definitions are preserved when
// the source document carried them; otherwise a single Default style is used.
func writeASS(doc *model.Document, cues []model.Cue) string {
	var b strings.Builder
	b.WriteString("[Script Info]\n")
	b.WriteString("ScriptType: v4.00+\n")
	b.WriteString("WrapStyle: 0\n\n")
	b.WriteString("[V4+ Styles]\n")
	b.WriteString("Format: Name, Fontname, Fontsize, PrimaryColour, SecondaryColour, OutlineColour, BackColour, Bold, Italic, Underline, StrikeOut, ScaleX, ScaleY, Spacing, Angle, BorderStyle, Outline, Shadow, Alignment, MarginL, MarginR, MarginV, Encoding\n")
	b.WriteString(styleDefinitions(doc))
	b.WriteString("\n[Events]\n")
	b.WriteString("Format: Layer, Start, End, Style, Name, MarginL, MarginR, MarginV, Effect, Text\n")
	for _, c := range cues {
		style := c.Style
		if style == "" {
			style = "Default"
		}
		fmt.Fprintf(&b, "Dialogue: %d,%s,%s,%s,,0,0,0,,%s\n",
			c.Layer, assTime(c.Start), assTime(c.End), style, encodeASS(c.Text))
	}
	return b.String()
}

// styleDefinitions returns the stored style lines, or a generated Default.
func styleDefinitions(doc *model.Document) string {
	if len(doc.Styles) > 0 {
		names := make([]string, 0, len(doc.Styles))
		for n := range doc.Styles {
			names = append(names, n)
		}
		sort.Strings(names)
		var b strings.Builder
		for _, n := range names {
			fmt.Fprintf(&b, "Style: %s\n", doc.Styles[n])
		}
		return b.String()
	}
	return "Style: Default,Arial,20,&H00FFFFFF,&H000000FF,&H00000000,&H00000000,0,0,0,0,100,100,0,0,1,2,2,2,10,10,10,1\n"
}

// AccountStyleLoss counts ASS-styled cues whenever the target is not ASS.
func AccountStyleLoss(doc *model.Document, target model.Format) {
	if target == model.FormatASS {
		return
	}
	for _, c := range doc.Cues {
		if c.Style != "" && c.Style != "Default" {
			doc.StyleLoss++
		}
	}
}

// --- timestamp formatters -------------------------------------------------

func srtTime(d time.Duration) string {
	h, m, s, ms := parts(d, 1e3)
	return fmt.Sprintf("%02d:%02d:%02d,%03d", h, m, s, ms)
}

func vttTime(d time.Duration) string {
	h, m, s, ms := parts(d, 1e3)
	return fmt.Sprintf("%02d:%02d:%02d.%03d", h, m, s, ms)
}

// assTime renders centisecond precision "H:MM:SS.cc" with an unpadded hour.
func assTime(d time.Duration) string {
	h, m, s, cs := parts(d, 1e2)
	return fmt.Sprintf("%d:%02d:%02d.%02d", h, m, s, cs)
}

// parts decomposes a duration into whole units and a fractional component
// rounded to the requested number of digits (1000 => ms, 100 => cs).
func parts(d time.Duration, fracBase int64) (h, m, s, frac int64) {
	total := int64(d)
	h = total / int64(time.Hour)
	total %= int64(time.Hour)
	m = total / int64(time.Minute)
	total %= int64(time.Minute)
	s = total / int64(time.Second)
	rem := total % int64(time.Second)
	frac = rem * fracBase / int64(time.Second)
	return h, m, s, frac
}

// --- text escaping --------------------------------------------------------

func escapeVTTText(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")
	return r.Replace(s)
}

func escapeVTTCueID(s string) string {
	// A cue id may not contain a newline or contain "-->".
	s = strings.ReplaceAll(s, "\n", " ")
	return strings.ReplaceAll(s, "-->", "→")
}

func encodeASS(s string) string {
	return strings.ReplaceAll(s, "\n", "\\N")
}
