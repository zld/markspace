package parse

import (
	"strings"

	"subsync/internal/model"
)

type assParser struct{}

func (assParser) Format() model.Format { return model.FormatASS }

// Parse reads an ASS/SSA script. Section headers ([Script Info],
// [V4+ Styles], [Events]) drive the state machine; only Dialogue events
// become cues while style definitions are retained for lossless re-emission.
func (assParser) Parse(text, track string, mode model.ParseMode) (*model.Document, error) {
	lines := splitLines(text)
	doc := &model.Document{
		Format: model.FormatASS,
		Styles: map[string]string{},
	}
	var skipped model.ParseErrors
	section := ""
	hasFormat := map[string]bool{}
	var styleCols, eventCols []string

	for i, raw := range lines {
		lineNo := i + 1
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, ";") {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			section = strings.ToLower(line[1 : len(line)-1])
			continue
		}
		colon := strings.IndexByte(line, ':')
		if colon < 0 {
			continue
		}
		kind := strings.TrimSpace(line[:colon])
		payload := line[colon+1:]

		switch {
		case section == "script info":
			// Script Info is informational for conversion; nothing to retain.
		case section == "v4+ styles" || section == "v4 styles":
			if cols, perr := assColumns(kind, payload, lineNo); perr != nil {
				if mode == model.ParseFail {
					return nil, perr
				}
				skipped = append(skipped, perr)
			} else if cols != nil {
				styleCols = cols
				hasFormat["style"] = true
			} else if kind == "Style" {
				recordStyle(doc, styleCols, payload)
			}
		case section == "events":
			if cols, perr := assColumns(kind, payload, lineNo); perr != nil {
				if mode == model.ParseFail {
					return nil, perr
				}
				skipped = append(skipped, perr)
			} else if cols != nil {
				eventCols = cols
				hasFormat["event"] = true
			} else if kind == "Dialogue" {
				cue, perr := parseDialogue(payload, eventCols, lineNo, track, len(doc.Cues))
				if perr != nil {
					if mode == model.ParseFail {
						return nil, perr
					}
					skipped = append(skipped, perr)
					continue
				}
				doc.Cues = append(doc.Cues, cue)
			}
		}
	}

	if !hasFormat["event"] {
		return nil, errf(0, "ASS script has no [Events] Format definition")
	}
	if len(skipped) > 0 {
		return doc, skipped
	}
	return doc, nil
}

// assColumns returns the column list for a Format line, nil for non-format
// lines, or an error when a format line is malformed.
func assColumns(kind, payload string, lineNo int) ([]string, *model.ParseError) {
	if !strings.EqualFold(kind, "Format") {
		return nil, nil
	}
	cols := strings.Split(payload, ",")
	for i := range cols {
		cols[i] = strings.TrimSpace(cols[i])
	}
	if len(cols) < 8 {
		return nil, errf(lineNo, "ASS Format must declare at least 8 columns, got %d", len(cols))
	}
	return cols, nil
}

// recordStyle stores the raw Style line keyed by its Name column.
func recordStyle(doc *model.Document, cols []string, payload string) {
	if len(cols) < 2 {
		return
	}
	nameIdx := indexOf(cols, "Name")
	if nameIdx < 0 {
		nameIdx = 0
	}
	fields := splitASSFields(payload, len(cols))
	if nameIdx >= len(fields) {
		return
	}
	name := strings.TrimSpace(fields[nameIdx])
	if name != "" {
		doc.Styles[name] = strings.TrimSpace(payload)
	}
}

// parseDialogue maps one Dialogue line onto a Cue using the section's
// Format column order, preserving the full Text field including commas.
func parseDialogue(payload string, cols []string, lineNo int, track string, order int) (model.Cue, *model.ParseError) {
	if len(cols) == 0 {
		return model.Cue{}, errf(lineNo, "Dialogue without preceding Format line")
	}
	fields := splitASSFields(payload, len(cols))
	if len(fields) < len(cols) {
		return model.Cue{}, errf(lineNo, "Dialogue has %d fields, Format declares %d", len(fields), len(cols))
	}
	get := func(name string) string {
		if i := indexOf(cols, name); i >= 0 && i < len(fields) {
			return strings.TrimSpace(fields[i])
		}
		return ""
	}
	from, err := parseTimestamp(get("Start"))
	if err != nil {
		return model.Cue{}, errf(lineNo, "%v", err)
	}
	to, err := parseTimestamp(get("End"))
	if err != nil {
		return model.Cue{}, errf(lineNo, "%v", err)
	}
	if !(from < to) {
		return model.Cue{}, errf(lineNo, "dialogue end must be after start")
	}
	text := get("Text")
	if strings.TrimSpace(text) == "" {
		return model.Cue{}, errf(lineNo, "dialogue has empty text")
	}
	layer := 0
	if v := get("Layer"); v != "" {
		// Non-numeric layers fall back to zero rather than failing the batch.
		for _, r := range v {
			if r < '0' || r > '9' {
				layer = 0
				break
			}
			layer = layer*10 + int(r-'0')
		}
	}
	style := get("Style")
	if style == "" {
		style = "Default"
	}
	return model.Cue{
		ID:    assLineID(order),
		Track: track,
		Order: order,
		Start: from,
		End:   to,
		Text:  decodeASSLines(text),
		Style: style,
		Layer: layer,
	}, nil
}

// splitASSFields splits on commas but keeps the final Text column whole even
// when it itself contains commas.
func splitASSFields(payload string, ncols int) []string {
	parts := strings.SplitN(payload, ",", ncols)
	return parts
}

func indexOf(cols []string, want string) int {
	for i, c := range cols {
		if strings.EqualFold(c, want) {
			return i
		}
	}
	return -1
}

// decodeASSLines turns hard ASS line breaks into normalized newlines.
func decodeASSLines(s string) string {
	s = strings.ReplaceAll(s, "\\N", "\n")
	s = strings.ReplaceAll(s, "\\n", "\n")
	return s
}

// encodeASSLines is the serializer-side inverse.
func encodeASSLines(s string) string {
	return strings.ReplaceAll(s, "\n", "\\N")
}

func assLineID(order int) string {
	return "ass-" + itoa(order+1)
}
