package metadata

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"

	"replaypipe/internal/timebase"
)

// subtitleCue is an intermediate representation before validation.
type subtitleCue struct {
	id      string
	start   string
	end     string
	text    []string
	line    int
	byteOff int64
}

var srtTime = regexp.MustCompile(`^\s*(\S+)\s*-->\s*(\S+)\s*$`)
var vttCue = regexp.MustCompile(`^\s*(?:(\S+)\s+)?(\S+)\s*-->\s*(\S+)(?:\s+.*)?$`)
var vttTime = regexp.MustCompile(`^(\d{2,}):(\d{2}):(\d{2})[.,](\d{3})$`)
var srtTimeOnly = regexp.MustCompile(`^(\d{2}):(\d{2}):(\d{2}),(\d{3})$`)

// ParseSRT reads SubRip cues. It accepts both CRLF and LF, tolerates a BOM
// and refuses overlapping-regressing timelines as TIME_REGRESSION.
func ParseSRT(r io.Reader, opts ParseOptions) ([]Event, *timebase.Error) {
	cues, err := scanSubtitle(r, false)
	if err != nil {
		return nil, err
	}
	return cuesToEvents(cues, KindSubtitle, "srt", opts)
}

// ParseWebVTT reads WebVTT cues. The WEBVTT header, NOTE blocks, STYLE and
// REGION blocks are skipped. Cue settings after the end timestamp are ignored.
func ParseWebVTT(r io.Reader, opts ParseOptions) ([]Event, *timebase.Error) {
	header := make([]byte, 3)
	n, _ := io.ReadFull(r, header)
	if n < 3 || !bytes.Equal(header, []byte("WEB")) {
		return nil, timebase.Fail(timebase.CodeUnsupportedFormat, opts.TrackID, opts.BaseOffset).
			Wrap(nil, "missing WEBVTT signature")
	}
	rest, _ := io.ReadAll(r)
	combined := append(append([]byte{}, header...), rest...)
	cues, err := scanVTT(bytes.NewReader(combined))
	if err != nil {
		return nil, err
	}
	return cuesToEvents(cues, KindSubtitle, "webvtt", opts)
}

func scanSubtitle(r io.Reader, isVTT bool) ([]subtitleCue, *timebase.Error) {
	br := bufio.NewReader(r)
	var cues []subtitleCue
	var cur *subtitleCue
	var pos int64
	for {
		line, err := br.ReadString('\n')
		if len(line) > 0 {
			raw := strings.TrimRight(line, "\r\n")
			if pos == 0 {
				raw = strings.TrimPrefix(raw, "\uFEFF")
			}
			lineStart := pos
			pos += int64(len(line))
			switch {
			case strings.TrimSpace(raw) == "":
				if cur != nil {
					cues = append(cues, *cur)
					cur = nil
				}
			case cur == nil:
				cur = &subtitleCue{line: len(cues) + 1, byteOff: lineStart}
				if m := srtTime.FindStringSubmatch(raw); m != nil {
					cur.start, cur.end = m[1], m[2]
					continue
				}
				if m := vttCue.FindStringSubmatch(raw); m != nil {
					cur.id, cur.start, cur.end = m[1], m[2], m[3]
					continue
				}
				if isVTT {
					cur.id = strings.TrimSpace(raw)
					cur.line = len(cues) + 1
					cur.byteOff = lineStart
					// hold as cue-id; next line is the timing line.
					if cur.id == "" {
						cur = nil
					}
					continue
				}
				return nil, timebase.Newf(timebase.CodeCorruptField, 0, lineStart,
					"srt line %d: expected cue timing, got %q", len(cues)+1, raw)
			default:
				if m := srtTime.FindStringSubmatch(raw); m != nil && cur.start == "" {
					cur.start, cur.end = m[1], m[2]
					continue
				}
				if m := vttCue.FindStringSubmatch(raw); m != nil && cur.start == "" {
					cur.id = firstNonEmpty(m[1], cur.id)
					cur.start, cur.end = m[2], m[3]
					continue
				}
				if cur.start == "" {
					cur.id = strings.TrimSpace(raw)
					continue
				}
				cur.text = append(cur.text, raw)
			}
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, timebase.Fail(timebase.CodeCorruptField, 0, pos).Wrap(err, "read subtitle")
		}
	}
	if cur != nil {
		cues = append(cues, *cur)
	}
	return cues, nil
}

func scanVTT(r io.Reader) ([]subtitleCue, *timebase.Error) {
	br := bufio.NewReader(r)
	// Consume header line.
	if _, err := br.ReadString('\n'); err != nil && err != io.EOF {
		return nil, timebase.Fail(timebase.CodeCorruptField, 0, 0).Wrap(err, "read webvtt header")
	}
	var cues []subtitleCue
	var cur *subtitleCue
	var pos int64
	inBlock := false
	for {
		line, err := br.ReadString('\n')
		if len(line) > 0 {
			raw := strings.TrimRight(line, "\r\n")
			lineStart := pos
			pos += int64(len(line))
			trimmed := strings.TrimSpace(raw)
			if trimmed == "" {
				if cur != nil {
					cues = append(cues, *cur)
					cur = nil
				}
				inBlock = false
			} else if !inBlock && strings.HasPrefix(trimmed, "NOTE") && len(trimmed) >= 4 {
				inBlock = true
				cur = nil
			} else if !inBlock && (strings.HasPrefix(trimmed, "STYLE") || strings.HasPrefix(trimmed, "REGION")) && len(trimmed) >= 5 {
				inBlock = true
				cur = nil
			} else if inBlock {
				// NOTE/STYLE/REGION blocks carry no cues; skip their bodies.
				continue
			} else if cur == nil {
				cur = &subtitleCue{line: len(cues) + 1, byteOff: lineStart}
				if m := vttCue.FindStringSubmatch(raw); m != nil {
					cur.id, cur.start, cur.end = m[1], m[2], m[3]
				} else {
					cur.id = trimmed
				}
			} else if cur.start == "" {
				if m := vttCue.FindStringSubmatch(raw); m != nil {
					cur.id = firstNonEmpty(m[1], cur.id)
					cur.start, cur.end = m[2], m[3]
				} else {
					return nil, timebase.Newf(timebase.CodeCorruptField, 0, lineStart,
						"webvtt cue %q: expected timing, got %q", cur.id, raw)
				}
			} else {
				cur.text = append(cur.text, raw)
			}
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, timebase.Fail(timebase.CodeCorruptField, 0, pos).Wrap(err, "read webvtt")
		}
	}
	if cur != nil {
		cues = append(cues, *cur)
	}
	return cues, nil
}

func firstNonEmpty(a, b string) string {
	if strings.TrimSpace(a) != "" {
		return a
	}
	return b
}

func cuesToEvents(cues []subtitleCue, kind Kind, source string, opts ParseOptions) ([]Event, *timebase.Error) {
	base, err := timebase.New(1000, 1)
	if err != nil {
		return nil, timebase.Fail(timebase.CodeInternal, opts.TrackID, opts.BaseOffset).Wrap(err, "subtitle timebase")
	}
	events := make([]Event, 0, len(cues))
	for i, c := range cues {
		if c.start == "" || c.end == "" {
			return nil, timebase.Newf(timebase.CodeCorruptField, opts.TrackID, opts.BaseOffset+c.byteOff,
				"cue %d (line %d) has no timing", i+1, c.line)
		}
		startMs, e := parseClock(c.start)
		if e != nil {
			return nil, e.WithTrack(opts.TrackID).WithOffset(opts.BaseOffset + c.byteOff)
		}
		endMs, e := parseClock(c.end)
		if e != nil {
			return nil, e.WithTrack(opts.TrackID).WithOffset(opts.BaseOffset + c.byteOff)
		}
		startNS, e := base.ToNanos(startMs, opts.TrackID, opts.BaseOffset+c.byteOff)
		if e != nil {
			return nil, e
		}
		endNS, e := base.ToNanos(endMs, opts.TrackID, opts.BaseOffset+c.byteOff)
		if e != nil {
			return nil, e
		}
		text := strings.Join(c.text, "\n")
		id := fmt.Sprintf("%s-%04d", slug(source+"-"+firstNonEmpty(c.id, text)), i+1)
		ev := Event{
			ID:           id,
			Kind:         kind,
			TrackID:      opts.TrackID,
			Source:       source,
			StartNS:      startNS,
			EndNS:        endNS,
			Timebase:     base.String(),
			Text:         text,
			SourceOffset: opts.BaseOffset + c.byteOff,
		}
		if e := ev.Validate(opts); e != nil {
			return nil, e
		}
		events = append(events, ev)
	}
	if e := ValidateOrder(events, opts); e != nil {
		return nil, e
	}
	return events, nil
}

// parseClock parses SRT/WebVTT timestamps (HH:MM:SS,mmm or HH:MM:SS.mmm)
// into milliseconds. Negative or malformed components are rejected.
func parseClock(s string) (int64, *timebase.Error) {
	m := vttTime.FindStringSubmatch(s)
	if m == nil {
		m = srtTimeOnly.FindStringSubmatch(s)
	}
	// WebVTT also permits HH:MM:SS.mmm without leading hours padding.
	if m == nil {
		parts := strings.FieldsFunc(s, func(r rune) bool { return r == ':' })
		if len(parts) == 3 {
			joined := fmt.Sprintf("%02d:%s:%s", atoiSafe(parts[0]), parts[1], parts[2])
			return parseClock(joined)
		}
		return 0, timebase.Newf(timebase.CodeCorruptField, 0, -1, "invalid timestamp %q", s)
	}
	h, _ := strconv.ParseInt(m[1], 10, 64)
	min, _ := strconv.ParseInt(m[2], 10, 64)
	sec, _ := strconv.ParseInt(m[3], 10, 64)
	ms, _ := strconv.ParseInt(m[4], 10, 64)
	if min > 59 || sec > 59 {
		return 0, timebase.Newf(timebase.CodeCorruptField, 0, -1, "timestamp %q has out-of-range minutes/seconds", s)
	}
	return h*3_600_000 + min*60_000 + sec*1_000 + ms, nil
}

func atoiSafe(s string) int64 {
	v, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0
	}
	return v
}
