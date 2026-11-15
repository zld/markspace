package parse

import (
	"strings"
	"time"

	"subsync/internal/model"
)

type srtParser struct{}

func (srtParser) Format() model.Format { return model.FormatSRT }

// Parse reads SRT blocks separated by blank lines. Each block is either a
// numeric index followed by a timing line and text, or a timing line with no
// explicit index. A bad block either aborts the batch (ParseFail) or is
// skipped and recorded (ParseSkip).
func (srtParser) Parse(text, track string, mode model.ParseMode) (*model.Document, error) {
	lines := splitLines(text)
	doc := &model.Document{Format: model.FormatSRT}
	var skipped model.ParseErrors

	startLine := 1
	var block []string
	flush := func(endLine int) *model.ParseError {
		if len(block) == 0 {
			return nil
		}
		cue, perr := parseSRTBlock(block, startLine, track, len(doc.Cues))
		if perr != nil {
			if mode == model.ParseFail {
				return perr
			}
			skipped = append(skipped, perr)
			return nil
		}
		doc.Cues = append(doc.Cues, cue)
		return nil
	}

	for i, line := range lines {
		lineNo := i + 1
		if blank(line) {
			if perr := flush(lineNo); perr != nil {
				return nil, perr
			}
			block = nil
			startLine = lineNo + 1
			continue
		}
		if len(block) == 0 {
			startLine = lineNo
		}
		block = append(block, line)
	}
	if perr := flush(len(lines)); perr != nil {
		return nil, perr
	}

	if len(skipped) > 0 {
		return doc, skipped
	}
	return doc, nil
}

// parseSRTBlock parses one blank-line-delimited SRT entry.
func parseSRTBlock(block []string, startLine int, track string, order int) (model.Cue, *model.ParseError) {
	idx := 0
	id := ""
	if !strings.Contains(block[0], "-->") {
		id = strings.TrimSpace(block[0])
		idx = 1
	}
	if idx >= len(block) {
		return model.Cue{}, errf(startLine, "cue missing timing line")
	}
	from, to, perr := parseSRTRange(block[idx], startLine+idx)
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
		Text:  body,
	}, nil
}

// parseSRTRange splits "00:00:01,000 --> 00:00:04,000" and ignores any
// trailing positioning coordinates such as "X1:12 X2:34 Y1:56 Y2:78".
func parseSRTRange(line string, lineNo int) (from, to time.Duration, perr *model.ParseError) {
	pos := strings.Index(line, "-->")
	if pos < 0 {
		return 0, 0, errf(lineNo, "missing --> separator")
	}
	left := strings.TrimSpace(line[:pos])
	right := line[pos+3:]
	if f := strings.Fields(right); len(f) > 0 {
		right = f[0]
	}
	right = strings.TrimSpace(right)
	from, err := parseTimestamp(left)
	if err != nil {
		return 0, 0, errf(lineNo, "%v", err)
	}
	to, err = parseTimestamp(right)
	if err != nil {
		return 0, 0, errf(lineNo, "%v", err)
	}
	return from, to, nil
}
