package parser

import (
	"strings"

	"subtitle/internal/cue"
)

// ASS section markers.
const (
	assEventsHeader = "[Events]"
	assFormatPrefix = "Format:"
	assDialogPrefix = "Dialogue:"
)

// ParseASS parses a SubStation Alpha file. The [Events] Format line is read
// dynamically so non-standard column layouts work; dialogue text is the last
// field and may itself contain commas and newlines (\N / \n).
func ParseASS(path string, text string) (*cue.Document, error) {
	lines := splitLines(text)
	doc := &cue.Document{Format: cue.FormatASS}
	inEvents := false
	var columns []string
	textCol := -1
	startCol, endCol := -1, -1

	for lineNo, raw := range lines {
		line := lineNo + 1
		trimmed := strings.TrimSpace(raw)
		if strings.HasPrefix(trimmed, "[") {
			inEvents = trimmed == assEventsHeader
			continue
		}
		if !inEvents || trimmed == "" {
			continue
		}
		if strings.HasPrefix(trimmed, assFormatPrefix) {
			columns = splitASSFields(trimmed[len(assFormatPrefix):])
			textCol, startCol, endCol = -1, -1, -1
			for i, c := range columns {
				switch strings.TrimSpace(c) {
				case "Text":
					textCol = i
				case "Start":
					startCol = i
				case "End":
					endCol = i
				}
			}
			if textCol < 0 || startCol < 0 || endCol < 0 {
				return nil, fail(path, line, "Events Format must define Start, End and Text columns")
			}
			continue
		}
		if !strings.HasPrefix(trimmed, assDialogPrefix) {
			continue // Comments and other event kinds are ignored.
		}
		if textCol < 0 {
			return nil, fail(path, line, "Dialogue before any [Events] Format line")
		}
		fields := splitASSFieldsN(trimmed[len(assDialogPrefix):], textCol)
		if len(fields) <= textCol {
			return nil, fail(path, line, "Dialogue has %d fields, Format expects %d", len(fields), len(columns))
		}
		start, err := parseASSTime(strings.TrimSpace(fields[startCol]))
		if err != nil {
			return nil, fail(path, line, "%v", err)
		}
		end, err := parseASSTime(strings.TrimSpace(fields[endCol]))
		if err != nil {
			return nil, fail(path, line, "%v", err)
		}
		body := strings.TrimSpace(fields[textCol])
		// Normalise hard line breaks to the model's newline convention while
		// preserving ASS override tags verbatim.
		body = strings.ReplaceAll(body, "\\N", "\n")
		body = strings.ReplaceAll(body, "\\n", "\n")
		doc.Cues = append(doc.Cues, cue.Cue{
			ID:         strings.TrimSpace(fields[0]),
			Start:      start,
			End:        end,
			Text:       body,
			SourceLine: line,
		})
	}
	return doc, nil
}

// splitASSFields splits on commas, trimming every field.
func splitASSFields(s string) []string {
	parts := strings.Split(s, ",")
	for i := range parts {
		parts[i] = strings.TrimSpace(parts[i])
	}
	return parts
}

// splitASSFieldsN keeps everything at and after column textCol as one field so
// commas inside dialogue text survive.
func splitASSFieldsN(s string, textCol int) []string {
	parts := strings.SplitN(s, ",", textCol+1)
	for i := 0; i < len(parts) && i < textCol; i++ {
		parts[i] = strings.TrimSpace(parts[i])
	}
	return parts
}
