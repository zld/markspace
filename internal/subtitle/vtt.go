package subtitle

import (
	"strconv"
	"strings"

	"mediaaudit/internal/model"
)

// parseTimestamp recognises HH:MM:SS,mmm (SRT), HH:MM:SS.mmm and MM:SS.mmm
// (WebVTT). It returns an exact seconds value, the trimmed original token and
// a positional diagnostic on failure.
func parseTimestamp(s string, col int, pos model.InputPos, vtt bool) (model.Rat, string, *model.Error) {
	orig := strings.TrimSpace(s)
	fail := func(format string, args ...any) (model.Rat, string, *model.Error) {
		p := pos
		if col > 0 {
			p.Column = col
		}
		return model.Rat{}, "", model.NewError(model.CodeSubtitleMalformed, "subtitle", "", p, format, args...)
	}
	if orig == "" {
		return fail("empty timestamp")
	}
	sep := byte(',')
	if vtt {
		sep = '.'
	}
	main, frac, ok := strings.Cut(orig, string(sep))
	if !ok {
		return fail("timestamp %q is missing the %q millisecond separator", orig, string(sep))
	}
	if len(frac) != 3 {
		return fail("timestamp %q must have exactly 3 millisecond digits", orig)
	}
	fields := strings.Split(main, ":")
	want := 3
	if vtt {
		want = 2
	}
	if len(fields) < want || len(fields) > 3 {
		if vtt {
			return fail("timestamp %q must be HH:MM:SS or MM:SS", orig)
		}
		return fail("timestamp %q must be HH:MM:SS", orig)
	}
	for i, f := range fields {
		if len(f) != 2 && !(i == 0 && len(fields) == 3 && len(f) > 2) {
			return fail("timestamp %q component %q must be two digits", orig, f)
		}
	}
	nums := [3]int64{}
	off := 3 - len(fields)
	for i, f := range fields {
		v, err := strconv.ParseInt(f, 10, 64)
		if err != nil || v < 0 || strings.HasPrefix(f, "+") {
			return fail("timestamp %q has a non-numeric component %q", orig, f)
		}
		nums[i+off] = v
	}
	ms, err := strconv.ParseInt(frac, 10, 64)
	if err != nil {
		return fail("timestamp %q has invalid milliseconds %q", orig, frac)
	}
	h, m, sec := nums[0], nums[1], nums[2]
	if m >= 60 || sec >= 60 {
		return fail("timestamp %q has minutes/seconds out of range", orig)
	}
	totalMS := ((h*60+m)*60+sec)*1000 + ms
	r, rerr := model.NewRat(totalMS, 1000)
	if rerr != nil {
		return model.Rat{}, "", model.NewError(model.CodeInternal, "subtitle", "", pos, "%v", rerr)
	}
	return r, orig, nil
}

// vttHeader describes the optional WebVTT header block.
type vttHeader struct {
	present bool
	pos     model.InputPos
	// offset is MPEGTS/90000 - LOCAL, i.e. the amount added to cue times to
	// place them on the programme clock.
	offset model.Rat
	mapped bool
}

// parseHeader validates the WEBVTT signature and reads X-TIMESTAMP-MAP.
func parseHeader(data []byte, file string) (vttHeader, *model.Error) {
	text := strings.TrimPrefix(string(data), "\ufeff")
	first := text
	if i := strings.IndexByte(first, '\n'); i >= 0 {
		first = first[:i]
	}
	first = strings.TrimSuffix(first, "\r")
	h := vttHeader{present: strings.HasPrefix(first, "WEBVTT") &&
		(len(first) == 6 || first[6] == ' ' || first[6] == '\t')}
	if !h.present {
		return h, model.NewError(model.CodeSubtitleMalformed, "subtitle", "",
			model.InputPos{File: file, Line: 1}, "WebVTT file must begin with WEBVTT")
	}
	h.pos = model.InputPos{File: file, Line: 1}
	lines := strings.Split(text, "\n")
	for i := 1; i < len(lines); i++ {
		line := strings.TrimSuffix(lines[i], "\r")
		if strings.TrimSpace(line) == "" {
			break
		}
		key, val, ok := strings.Cut(line, "=")
		if !ok || !strings.EqualFold(strings.TrimSpace(key), "X-TIMESTAMP-MAP") {
			continue
		}
		off, err := parseTimestampMap(val, model.InputPos{File: file, Line: i + 1})
		if err != nil {
			return h, err
		}
		h.offset = off
		h.mapped = true
	}
	return h, nil
}

// parseTimestampMap parses "LOCAL:00:00:00.000,MPEGTS:900000".
func parseTimestampMap(val string, pos model.InputPos) (model.Rat, *model.Error) {
	var local, mpegts *string
	for _, part := range strings.Split(val, ",") {
		k, v, ok := strings.Cut(strings.TrimSpace(part), ":")
		if !ok {
			continue
		}
		switch strings.ToUpper(strings.TrimSpace(k)) {
		case "LOCAL":
			s := strings.TrimSpace(v)
			local = &s
		case "MPEGTS":
			s := strings.TrimSpace(v)
			mpegts = &s
		}
	}
	if local == nil || mpegts == nil {
		return model.Rat{}, model.NewError(model.CodeSubtitleMalformed, "subtitle", "", pos,
			"X-TIMESTAMP-MAP must contain LOCAL and MPEGTS (%q)", strings.TrimSpace(val))
	}
	localT, _, err := parseTimestamp(*local, 0, pos, true)
	if err != nil {
		return model.Rat{}, err
	}
	n, perr := strconv.ParseInt(*mpegts, 10, 64)
	if perr != nil || n < 0 {
		return model.Rat{}, model.NewError(model.CodeSubtitleMalformed, "subtitle", "", pos,
			"X-TIMESTAMP-MAP MPEGTS value %q is not a non-negative integer", *mpegts)
	}
	mpegT, rerr := model.NewRat(n, 90000)
	if rerr != nil {
		return model.Rat{}, model.NewError(model.CodeInternal, "subtitle", "", pos, "%v", rerr)
	}
	return mpegT.Sub(localT), nil
}

// parseVTT parses cues. Blocks beginning NOTE, STYLE or REGION are skipped;
// an optional cue identifier may precede the timing line.
func parseVTT(data []byte, file string) ([]Cue, model.Errors) {
	es := model.Errors{}
	blocks := splitBlocks(data, file)
	if len(blocks) > 0 && blocks[0].startLine == 1 {
		blocks = blocks[1:] // header block
	}
	var cues []Cue
	for _, b := range blocks {
		if len(b.lines) == 0 {
			continue
		}
		head := strings.TrimSpace(b.lines[0])
		upper := strings.ToUpper(head)
		if upper == "NOTE" || strings.HasPrefix(upper, "NOTE ") || upper == "STYLE" || upper == "REGION" {
			continue
		}
		timingIdx := 0
		if findArrow(b.lines[0]) < 0 {
			if len(b.lines) < 2 || findArrow(b.lines[1]) < 0 {
				es.Add(model.NewError(model.CodeSubtitleMalformed, "subtitle", "", model.InputPos{
					File: file, Line: b.startLine}, "cue block has no timing line (first line %q)", b.lines[0]))
				continue
			}
			timingIdx = 1 // cue identifier
		}
		pos := model.InputPos{File: file, Line: b.startLine + timingIdx}
		tl, err := timingLine(b.lines[timingIdx], pos.Line, file, true)
		if err != nil {
			es.Add(err)
			continue
		}
		cue, cerr := buildCue(len(cues)+1, tl, b.lines[timingIdx+1:], pos)
		if cerr != nil {
			es.Add(cerr)
			continue
		}
		cues = append(cues, cue)
	}
	return cues, es
}

// DetectFormat infers the format from the file extension or the WEBVTT magic.
func DetectFormat(name string, data []byte) Format {
	if strings.HasSuffix(strings.ToLower(name), ".vtt") {
		return FormatVTT
	}
	if strings.HasSuffix(strings.ToLower(name), ".srt") {
		return FormatSRT
	}
	if strings.HasPrefix(strings.TrimPrefix(string(data), "\ufeff"), "WEBVTT") {
		return FormatVTT
	}
	return FormatSRT
}
