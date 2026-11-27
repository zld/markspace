// Package dash parses DASH Media Presentation Documents into the shared
// model.Representation shape. The parser is hand-rolled (instead of
// encoding/xml) so every error can carry the manifest line number and so
// unparsable tags are classified explicitly rather than swallowed.
package dash

import (
	"bytes"
	"strconv"
	"strings"

	"mediarc/internal/model"
)

type tagToken struct {
	line      int
	name      string
	attrs     map[string]string
	closing   bool
	selfClose bool
}

// Parse parses an MPD document. Per-representation problems are recorded on
// the returned representations and never prevent sibling representations from
// being parsed; only top-level structural failure returns an error.
func Parse(fileName string, data []byte) ([]model.Representation, []*model.MediaError, error) {
	tokens, err := tokenize(fileName, data)
	if err != nil {
		return nil, nil, err
	}
	return walk(fileName, tokens)
}

func tokenize(fileName string, data []byte) ([]tagToken, error) {
	var toks []tagToken
	line := 1
	i := 0
	n := len(data)
	for i < n {
		c := data[i]
		switch c {
		case '\n':
			line++
			i++
		case '<':
			// Comments and declarations: skip to closing '>'.
			if i+4 <= n && string(data[i:i+4]) == "<!--" {
				end := bytes.Index(data[i:], []byte("-->"))
				if end < 0 {
					return nil, model.AtLine(model.KindManifestTruncated, fileName, line, "unterminated comment")
				}
				line += bytes.Count(data[i:i+end], []byte{'\n'})
				i += end + 3
				continue
			}
			if i+1 < n && (data[i+1] == '?' || data[i+1] == '!') {
				end := bytes.IndexByte(data[i:], '>')
				if end < 0 {
					return nil, model.AtLine(model.KindManifestSyntax, fileName, line, "unterminated declaration")
				}
				line += bytes.Count(data[i:i+end], []byte{'\n'})
				i += end + 1
				continue
			}
			startLine := line
			end := bytes.IndexByte(data[i:], '>')
			if end < 0 {
				return nil, model.AtLine(model.KindManifestTruncated, fileName, line, "unterminated tag")
			}
			line += bytes.Count(data[i:i+end], []byte{'\n'})
			body := string(data[i+1 : i+end])
			i += end + 1
			tok, perr := parseTag(fileName, startLine, body)
			if perr != nil {
				return nil, perr
			}
			toks = append(toks, tok)
		default:
			i++
		}
	}
	return toks, nil
}

func parseTag(fileName string, line int, body string) (tagToken, error) {
	body = strings.TrimSpace(body)
	tok := tagToken{line: line, attrs: map[string]string{}}
	if strings.HasPrefix(body, "/") {
		tok.closing = true
		tok.name = strings.TrimSpace(body[1:])
		if tok.name == "" || strings.ContainsAny(tok.name, " \t") {
			return tok, model.AtLine(model.KindManifestSyntax, fileName, line, "malformed closing tag")
		}
		return tok, nil
	}
	if strings.HasSuffix(body, "/") {
		tok.selfClose = true
		body = strings.TrimSpace(body[:len(body)-1])
	}
	parts := strings.Fields(body)
	if len(parts) == 0 {
		return tok, model.AtLine(model.KindManifestSyntax, fileName, line, "empty tag")
	}
	tok.name = parts[0]
	for _, p := range parts[1:] {
		eq := strings.IndexByte(p, '=')
		if eq <= 0 {
			return tok, model.AtLine(model.KindManifestSyntax, fileName, line, "attribute without value: "+p)
		}
		key := p[:eq]
		val := strings.Trim(p[eq+1:], "\"'")
		tok.attrs[key] = val
	}
	return tok, nil
}

// walk is a small stack machine over the token stream.
func walk(fileName string, toks []tagToken) ([]model.Representation, []*model.MediaError, error) {
	var reps []model.Representation
	var globalErrs []*model.MediaError
	var stack []string
	var cur *model.Representation
	var mediaTpl string
	var startNumber int
	var presentationMs int64
	haveDuration := false
	known := map[string]bool{
		"MPD": true, "Period": true, "AdaptationSet": true, "Representation": true,
		"SegmentTemplate": true, "SegmentTimeline": true, "S": true, "BaseURL": true,
	}
	seenURL := map[string]int{}

	closeRep := func() {
		if cur == nil {
			return
		}
		reps = append(reps, *cur)
		cur = nil
		mediaTpl, startNumber = "", 0
	}

	for _, t := range toks {
		if t.closing {
			if len(stack) == 0 || stack[len(stack)-1] != t.name {
				return nil, nil, model.AtLine(model.KindManifestSyntax, fileName, t.line,
					"unexpected closing tag </"+t.name+">, open stack: "+strings.Join(stack, "/"))
			}
			stack = stack[:len(stack)-1]
			if t.name == "Representation" {
				closeRep()
			}
			continue
		}
		if !known[t.name] {
			globalErrs = append(globalErrs, model.AtLine(model.KindManifestSyntax, fileName, t.line,
				"unparsable/unknown tag <"+t.name+">"))
			if !t.selfClose {
				stack = append(stack, t.name)
			}
			continue
		}
		switch t.name {
		case "MPD":
			if d, ok := t.attrs["mediaPresentationDuration"]; ok {
				presentationMs = isoDurationMillis(d)
				haveDuration = presentationMs >= 0
				if !haveDuration {
					globalErrs = append(globalErrs, model.AtLine(model.KindMissingDuration, fileName, t.line,
						"invalid mediaPresentationDuration "+d))
				}
			} else {
				globalErrs = append(globalErrs, model.AtLine(model.KindMissingDuration, fileName, t.line,
					"MPD missing mediaPresentationDuration"))
			}
		case "Representation":
			cur = &model.Representation{
				ID:        attrOr(t.attrs, "id", "rep-"+strconv.Itoa(len(reps))),
				Format:    "dash",
				Timescale: 1000,
			}
			if ts, err := strconv.ParseUint(attrOr(t.attrs, "timescale", "1000"), 10, 32); err == nil {
				cur.Timescale = uint32(ts)
			}
		case "SegmentTemplate":
			mediaTpl = t.attrs["media"]
			startNumber = atoiDefault(t.attrs["startNumber"], 1)
		case "BaseURL":
			// BaseURL text is not captured by the tokenizer (text nodes are
			// skipped); the attribute form is the supported one.
			if u := t.attrs["url"]; u != "" {
				addSegmentRef(cur, fileName, t.line, u, 0, 0, seenURL)
			}
		case "S":
			d, ok := parseUint(t.attrs["d"])
			if !ok {
				if cur != nil {
					cur.ManifestErrors = append(cur.ManifestErrors,
						model.AtLine(model.KindMissingDuration, fileName, t.line, "S element missing duration"))
				} else {
					globalErrs = append(globalErrs, model.AtLine(model.KindMissingDuration, fileName, t.line, "S element missing duration"))
				}
				break
			}
			start, _ := parseUint(attrOr(t.attrs, "t", "0"))
			durMs := int64(d) * 1000 / int64(timescaleOf(cur))
			startMs := int64(start) * 1000 / int64(timescaleOf(cur))
			if haveDuration && startMs+durMs > presentationMs {
				me := model.AtLine(model.KindRangeOutOfBounds, fileName, t.line,
					"segment range "+strconv.FormatInt(startMs, 10)+"+"+strconv.FormatInt(durMs, 10)+
						" ms exceeds presentation duration "+strconv.FormatInt(presentationMs, 10)+" ms")
				if cur != nil {
					cur.ManifestErrors = append(cur.ManifestErrors, me)
				} else {
					globalErrs = append(globalErrs, me)
				}
			}
			url := expandTemplate(mediaTpl, startNumber+segmentCount(cur))
			addSegmentRef(cur, fileName, t.line, url, startMs, durMs, seenURL)
		}
		if !t.selfClose {
			stack = append(stack, t.name)
		}
	}
	if len(stack) != 0 {
		return nil, nil, model.AtLine(model.KindManifestTruncated, fileName, lastLine(toks),
			"manifest ends with unclosed tags: "+strings.Join(stack, "/"))
	}
	closeRep()
	return reps, globalErrs, nil
}

func addSegmentRef(cur *model.Representation, file string, line int, url string, start, dur int64, seen map[string]int) {
	if cur == nil || url == "" {
		return
	}
	if firstLine, dup := seen[url]; dup {
		cur.ManifestErrors = append(cur.ManifestErrors, model.AtLine(model.KindDuplicateReference, file, line,
			"segment URL "+url+" already referenced at line "+strconv.Itoa(firstLine)))
		return
	}
	seen[url] = line
	cur.Segments = append(cur.Segments, model.SegmentRef{
		URL: url, Index: len(cur.Segments), Line: line, StartMs: start, DurationMs: dur,
	})
}

func segmentCount(cur *model.Representation) int {
	if cur == nil {
		return 0
	}
	return len(cur.Segments)
}

func timescaleOf(cur *model.Representation) uint32 {
	if cur != nil && cur.Timescale > 0 {
		return cur.Timescale
	}
	return 1000
}

func expandTemplate(tpl string, number int) string {
	if tpl == "" {
		return ""
	}
	return strings.ReplaceAll(tpl, "$Number$", strconv.Itoa(number))
}

func attrOr(m map[string]string, k, def string) string {
	if v, ok := m[k]; ok {
		return v
	}
	return def
}

func atoiDefault(s string, def int) int {
	if v, err := strconv.Atoi(s); err == nil {
		return v
	}
	return def
}

func parseUint(s string) (uint64, bool) {
	v, err := strconv.ParseUint(s, 10, 64)
	return v, err == nil
}

func lastLine(toks []tagToken) int {
	if len(toks) == 0 {
		return 1
	}
	return toks[len(toks)-1].line
}

// isoDurationMillis parses the subset PTnS / PTn.nS of ISO 8601 durations.
func isoDurationMillis(s string) int64 {
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, "PT") {
		return -1
	}
	rest := strings.TrimSuffix(s[2:], "S")
	parts := strings.Split(rest, ".")
	sec, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		return -1
	}
	ms := sec * 1000
	if len(parts) == 2 {
		frac := parts[1]
		if len(frac) > 3 {
			frac = frac[:3]
		}
		f, err := strconv.ParseInt(frac, 10, 64)
		if err != nil {
			return -1
		}
		for i := len(frac); i < 3; i++ {
			f *= 10
		}
		ms += f
	}
	return ms
}
