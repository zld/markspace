package subtitle

import (
	"strings"

	"mediapack/internal/fault"
)

// ParseVTT parses WebVTT data. The raw digest is retained and all times use
// the shared integer timebase.
func ParseVTT(source string, raw []byte) (*Document, error) {
	text := string(raw)
	if !strings.HasPrefix(strings.TrimSpace(text), "WEBVTT") {
		return nil, fault.At(fault.SubtitleParse, source, 0, "missing WEBVTT magic header")
	}
	doc := &Document{Format: "vtt", Source: source, RawSHA256: Digest(raw)}
	blocks := splitBlocks(text)
	for bi, b := range blocks {
		if bi == 0 && strings.HasPrefix(b.lines[0], "WEBVTT") {
			continue
		}
		if strings.HasPrefix(b.lines[0], "NOTE") || strings.HasPrefix(b.lines[0], "STYLE") ||
			strings.HasPrefix(b.lines[0], "REGION") {
			continue
		}

		timingIdx := -1
		for i, line := range b.lines {
			if strings.Contains(line, "-->") {
				timingIdx = i
				break
			}
		}
		if timingIdx < 0 {
			return nil, fault.At(fault.SubtitleParse, source, int64(b.startOffset),
				"WebVTT block at line %d lacks timing", b.startLine)
		}

		id := ""
		if timingIdx == 1 {
			id = strings.TrimSpace(b.lines[0])
		} else if timingIdx != 0 {
			return nil, fault.At(fault.SubtitleParse, source, int64(b.startOffset),
				"WebVTT cue has malformed identifier/timing order")
		}

		start, end, _, err := parseTimingLine(source, b, b.lines[timingIdx], "WebVTT")
		if err != nil {
			return nil, err
		}
		textLines := b.lines[timingIdx+1:]
		if len(textLines) == 0 {
			return nil, fault.At(fault.SubtitleParse, source, int64(b.startOffset),
				"WebVTT cue %q has no text", id)
		}
		doc.Cues = append(doc.Cues, Cue{
			Index: len(doc.Cues) + 1,
			ID:    id,
			Start: start,
			End:   end,
			Text:  strings.Join(textLines, "\n"),
			Line:  b.startLine,
		})
	}
	doc.CueCount = len(doc.Cues)
	if err := ValidateCues(source, doc.Cues); err != nil {
		return nil, err
	}
	return doc, nil
}

// ToSRT renders cues in deterministic SRT form.
func (d *Document) ToSRT() []byte {
	var b strings.Builder
	for i, c := range d.Cues {
		if i > 0 {
			b.WriteString("\n")
		}
		b.WriteString(itoa(i + 1))
		b.WriteString("\n")
		b.WriteString(formatSRTStamp(c.Start))
		b.WriteString(" --> ")
		b.WriteString(formatSRTStamp(c.End))
		b.WriteString("\n")
		b.WriteString(c.Text)
		b.WriteString("\n")
	}
	return []byte(b.String())
}

// ToVTT renders cues in deterministic WebVTT form.
func (d *Document) ToVTT() []byte {
	var b strings.Builder
	b.WriteString("WEBVTT\n\n")
	for _, c := range d.Cues {
		b.WriteString(formatVTTStamp(c.Start))
		b.WriteString(" --> ")
		b.WriteString(formatVTTStamp(c.End))
		b.WriteString("\n")
		b.WriteString(c.Text)
		b.WriteString("\n\n")
	}
	return []byte(b.String())
}
