package parse

import (
	"strings"
	"time"

	"subsync/internal/model"
)

type vttParser struct{}

func (vttParser) Format() model.Format { return model.FormatVTT }

// Parse walks the WebVTT block structure. Header, NOTE, REGION and STYLE
// blocks are consumed without becoming cues; cue blocks carry an optional
// identifier line ahead of the timing line.
func (vttParser) Parse(text, track string, mode model.ParseMode) (*model.Document, error) {
	lines := splitLines(text)
	doc := &model.Document{Format: model.FormatVTT}
	var skipped model.ParseErrors

	i := 0
	for i < len(lines) {
		for i < len(lines) && blank(lines[i]) {
			i++
		}
		if i >= len(lines) {
			break
		}
		blockStart := i
		var block []string
		for i < len(lines) && !blank(lines[i]) {
			block = append(block, lines[i])
			i++
		}
		if isVTTControlBlock(block) {
			continue
		}
		cue, perr := parseVTTBlock(block, blockStart+1, track, len(doc.Cues))
		if perr != nil {
			if mode == model.ParseFail {
				return nil, perr
			}
			skipped = append(skipped, perr)
			continue
		}
		doc.Cues = append(doc.Cues, cue)
	}

	if len(skipped) > 0 {
		return doc, skipped
	}
	return doc, nil
}

func isVTTControlBlock(block []string) bool {
	if len(block) == 0 {
		return true
	}
	head := block[0]
	return head == "WEBVTT" ||
		strings.HasPrefix(head, "WEBVTT ") ||
		strings.HasPrefix(head, "NOTE") ||
		strings.HasPrefix(head, "REGION") ||
		strings.HasPrefix(head, "STYLE")
}

func parseVTTBlock(block []string, startLine int, track string, order int) (model.Cue, *model.ParseError) {
	idx := 0
	id := ""
	if !strings.Contains(block[0], "-->") {
		id = strings.TrimSpace(block[0])
		idx = 1
	}
	if idx >= len(block) {
		return model.Cue{}, errf(startLine, "cue missing timing line")
	}
	from, to, perr := parseVTTRange(block[idx], startLine+idx)
	if perr != nil {
		return model.Cue{}, perr
	}
	body := strings.TrimSpace(strings.Join(block[idx+1:], "\n"))
	if body == "" {
		return model.Cue{}, errf(startLine+idx, "cue has empty text")
	}
	if !(from < to) {
		return model.Cue{}, errf(startLine+idx, "cue end must be after start")
	}
	return model.Cue{
		ID:    id,
		Track: track,
		Order: order,
		Start: from,
		End:   to,
		Text:  unescapeVTT(body),
	}, nil
}

// parseVTTRange accepts timing settings after the right timestamp
// (e.g. "align:start position:0%") and strips them.
func parseVTTRange(line string, lineNo int) (from, to time.Duration, perr *model.ParseError) {
	pos := strings.Index(line, "-->")
	if pos < 0 {
		return 0, 0, errf(lineNo, "missing --> separator")
	}
	left := strings.TrimSpace(line[:pos])
	rest := strings.Fields(strings.TrimSpace(line[pos+3:]))
	if len(rest) == 0 {
		return 0, 0, errf(lineNo, "missing end timestamp")
	}
	from, err := parseTimestamp(left)
	if err != nil {
		return 0, 0, errf(lineNo, "%v", err)
	}
	to, err = parseTimestamp(rest[0])
	if err != nil {
		return 0, 0, errf(lineNo, "%v", err)
	}
	return from, to, nil
}

// unescapeVTT reverses the three mandatory WebVTT escape sequences.
func unescapeVTT(s string) string {
	r := strings.NewReplacer("&amp;", "&", "&lt;", "<", "&gt;", ">")
	return r.Replace(s)
}

// escapeVTT is used by the serializer; exported through the parse package so
// both directions share one definition.
func escapeVTT(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")
	return r.Replace(s)
}
