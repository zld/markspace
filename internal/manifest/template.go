package manifest

import (
	"fmt"
	"strconv"
	"strings"

	"medtrace/internal/core"
)

// timelineEntry is one concrete segment slot produced from a template.
type timelineEntry struct {
	number    int64
	timeTicks int64
	duration  core.Nanos
}

// template holds a resolved SegmentTemplate. Representations may override
// attributes set on the AdaptationSet, so a template can be partially filled.
type template struct {
	init      string
	media     string
	timescale uint32
	startNum  int64
	mediaDur  core.Nanos // nominal segment duration, zero when timeline-driven
	entries   []timelineEntry
	line      int
	set       bool // whether any media template was declared
}

// setTemplate resolves the AdaptationSet-level SegmentTemplate.
func setTemplate(rel string, set *node) (*template, *core.Fault) {
	n := set.child("SegmentTemplate")
	if n == nil {
		return &template{startNum: 1, line: set.line}, nil
	}
	return readTemplate(rel, n)
}

// readTemplate parses one SegmentTemplate element, expanding its timeline.
func readTemplate(rel string, n *node) (*template, *core.Fault) {
	t := &template{startNum: 1, line: n.line, set: true}
	if v, ok := n.attr("initialization"); ok {
		t.init = v
	}
	if v, ok := n.attr("media"); ok {
		t.media = v
	}
	if v, ok := n.attr("timescale"); ok {
		ts, err := parseUint(v)
		if err != nil || ts == 0 || ts > 1<<31 {
			return nil, dashFault(rel, n.line, fmt.Sprintf("invalid SegmentTemplate timescale %q", v))
		}
		t.timescale = uint32(ts)
	}
	if v, ok := n.attr("startNumber"); ok {
		num, err := parseUint(v)
		if err != nil {
			return nil, dashFault(rel, n.line, fmt.Sprintf("invalid startNumber %q", v))
		}
		t.startNum = int64(num)
	}
	if v, ok := n.attr("duration"); ok {
		dur, err := parseUint(v)
		if err != nil {
			return nil, dashFault(rel, n.line, fmt.Sprintf("invalid duration %q", v))
		}
		if t.timescale == 0 {
			return nil, dashFault(rel, n.line, "SegmentTemplate duration requires timescale")
		}
		d, err := core.FromTicks(int64(dur), t.timescale)
		if err != nil {
			return nil, dashFault(rel, n.line, err.Error())
		}
		t.mediaDur = d
	}
	if err := expandTimeline(rel, n, t); err != nil {
		return nil, err
	}
	return t, nil
}

// expandTimeline flattens a SegmentTimeline into concrete entries. A negative
// repeat count is not resolvable without a following entry, so it is rejected.
func expandTimeline(rel string, n *node, t *template) *core.Fault {
	tl := n.child("SegmentTimeline")
	if tl == nil {
		return nil
	}
	if t.timescale == 0 {
		return dashFault(rel, tl.line, "SegmentTimeline requires a timescale")
	}
	var cursor int64
	haveCursor := false
	firstDuration := int64(0)
	for _, s := range tl.childrenNamed("S") {
		if v, ok := s.attr("t"); ok {
			raw, err := parseUint(v)
			if err != nil {
				return dashFault(rel, s.line, fmt.Sprintf("invalid timeline t %q", v))
			}
			cursor, haveCursor = int64(raw), true
		} else if !haveCursor {
			cursor, haveCursor = 0, true
		}
		dStr, ok := s.attr("d")
		if !ok {
			return dashFault(rel, s.line, "SegmentTimeline S element has no d")
		}
		d, err := parseUint(dStr)
		if err != nil || d == 0 {
			return dashFault(rel, s.line, fmt.Sprintf("invalid timeline d %q", dStr))
		}
		if firstDuration == 0 {
			firstDuration = int64(d)
		}
		repeat := uint64(0)
		if v, ok := s.attr("r"); ok {
			repeat, err = parseUint(v)
			if err != nil {
				return dashFault(rel, s.line, fmt.Sprintf("invalid timeline r %q", v))
			}
			if strings.HasPrefix(strings.TrimSpace(v), "-") {
				return dashFault(rel, s.line, "negative timeline r is not supported")
			}
		}
		dur, err := core.FromTicks(int64(d), t.timescale)
		if err != nil {
			return dashFault(rel, s.line, err.Error())
		}
		for i := uint64(0); i <= repeat; i++ {
			t.entries = append(t.entries, timelineEntry{
				number:    t.startNum + int64(len(t.entries)),
				timeTicks: cursor,
				duration:  dur,
			})
			cursor += int64(d)
		}
	}
	if t.mediaDur == 0 && firstDuration > 0 {
		if d, err := core.FromTicks(firstDuration, t.timescale); err == nil {
			t.mediaDur = d
		}
	}
	return nil
}

// appendRepresentation expands one Representation into ordered segments.
func appendRepresentation(pl *Playlist, rel string, set, rep *node, base *template, kind core.TrackKind, mpdDur core.Nanos) *core.Fault {
	id, ok := rep.attr("id")
	if !ok || strings.TrimSpace(id) == "" {
		return dashFault(rel, rep.line, "Representation has no id")
	}
	rp := Representation{ID: id, Track: kind}
	var f *core.Fault
	if rp.Bandwidth, f = intAttr(rel, rep, "bandwidth"); f != nil {
		return f
	}
	if v, ok := rep.attr("codecs"); ok {
		rp.Codecs = v
	} else if v, ok := set.attr("codecs"); ok {
		rp.Codecs = v
	}
	if w, f := intAttr(rel, rep, "width"); f != nil {
		return f
	} else if w > 0 {
		rp.Width = uint32(w)
	}
	if h, f := intAttr(rel, rep, "height"); f != nil {
		return f
	} else if h > 0 {
		rp.Height = uint32(h)
	}
	tpl, f := mergeTemplate(rel, base, rep)
	if f != nil {
		return f
	}
	if tpl.media == "" {
		return dashFault(rel, rep.line, fmt.Sprintf("Representation %q has no media template", id))
	}
	rp.Timescale = tpl.timescale
	if tpl.init != "" {
		s, f := resolveTemplate(rel, tpl.init, tpl, id, 0, 0)
		if f != nil {
			return f
		}
		rp.Init = s
	}
	// Validate the media template's containment before deriving segment
	// counts, so a template that escapes the media root is reported as such
	// rather than as a missing duration.
	if _, f := resolveTemplate(rel, tpl.media, tpl, id, tpl.startNum, 0); f != nil {
		return f
	}
	entries := tpl.entries
	if len(entries) == 0 {
		if tpl.mediaDur <= 0 {
			return dashFault(rel, rep.line,
				fmt.Sprintf("Representation %q has neither SegmentTimeline nor a positive duration", id))
		}
		if mpdDur <= 0 {
			return dashFault(rel, rep.line,
				fmt.Sprintf("Representation %q needs mediaPresentationDuration or a SegmentTimeline", id))
		}
		count := int64((mpdDur + tpl.mediaDur - 1) / tpl.mediaDur)
		for i := int64(0); i < count; i++ {
			ticks, err := core.ToTicks(tpl.mediaDur*core.Nanos(i), tpl.timescale)
			if err != nil {
				return dashFault(rel, rep.line, err.Error())
			}
			entries = append(entries, timelineEntry{
				number:    tpl.startNum + i,
				timeTicks: ticks,
				duration:  tpl.mediaDur,
			})
		}
	}
	pl.Representations = append(pl.Representations, rp)
	for _, e := range entries {
		uri, f := resolveTemplate(rel, tpl.media, tpl, id, e.number, e.timeTicks)
		if f != nil {
			return f
		}
		pl.Segments = append(pl.Segments, Segment{
			URI:            uri,
			Line:           tpl.line,
			Representation: id,
			Init:           rp.Init,
			Duration:       e.duration,
			Sequence:       e.number,
		})
	}
	return nil
}

// intAttr reads an optional unsigned integer attribute.
func intAttr(rel string, n *node, name string) (int64, *core.Fault) {
	v, ok := n.attr(name)
	if !ok {
		return 0, nil
	}
	num, err := parseUint(v)
	if err != nil {
		return 0, dashFault(rel, n.line, fmt.Sprintf("invalid %s %q", name, v))
	}
	return int64(num), nil
}

// mergeTemplate overlays a Representation's own SegmentTemplate onto the
// AdaptationSet template, letting a timeline declared at either level win.
func mergeTemplate(rel string, base *template, rep *node) (*template, *core.Fault) {
	own := rep.child("SegmentTemplate")
	if own == nil {
		cp := *base
		return &cp, nil
	}
	merged, f := readTemplate(rel, own)
	if f != nil {
		return nil, f
	}
	if merged.init == "" {
		merged.init = base.init
	}
	if merged.media == "" {
		merged.media = base.media
	}
	if merged.timescale == 0 {
		merged.timescale = base.timescale
	}
	if len(merged.entries) == 0 {
		merged.entries = base.entries
	}
	if merged.mediaDur == 0 {
		merged.mediaDur = base.mediaDur
	}
	if merged.startNum == 1 {
		merged.startNum = base.startNum
	}
	return merged, nil
}

// resolveTemplate expands a segment URL template ($RepresentationID$,
// $Number$, $Time$ with optional %0Nd widths, $Bandwidth$), then validates
// that the result stays inside the media root. timeTicks must already be in
// the template timescale.
func resolveTemplate(rel, tpl string, t *template, id string, num, timeTicks int64) (string, *core.Fault) {
	out := strings.ReplaceAll(tpl, "$RepresentationID$", id)
	out = strings.ReplaceAll(out, "$Bandwidth$", "")
	var err error
	out, err = expandTokens(out, map[string]int64{
		"Number": num,
		"Time":   timeTicks,
	})
	if err != nil {
		return "", dashFault(rel, t.line, fmt.Sprintf("template %q: %v", tpl, err))
	}
	if strings.Contains(out, "$") {
		return "", dashFault(rel, t.line, fmt.Sprintf("unsupported template token in %q", tpl))
	}
	return resolveRel(rel, ref{URIPath: out, Line: t.line})
}

// expandTokens replaces $Name$ and $Name%0Nd$ tokens with their values.
func expandTokens(s string, values map[string]int64) (string, error) {
	for {
		start := strings.IndexByte(s, '$')
		if start < 0 {
			return s, nil
		}
		endRel := strings.IndexByte(s[start+1:], '$')
		if endRel < 0 {
			return "", fmt.Errorf("unterminated $ token")
		}
		end := start + 1 + endRel
		body := s[start+1 : end]
		name, widthSpec := body, ""
		if p := strings.IndexByte(body, '%'); p >= 0 {
			name, widthSpec = body[:p], body[p:]
		}
		v, ok := values[name]
		if !ok {
			return "", fmt.Errorf("unknown token $%s$", name)
		}
		val := strconv.FormatInt(v, 10)
		if widthSpec != "" {
			w, err := parseWidth(widthSpec)
			if err != nil {
				return "", err
			}
			if len(val) < w {
				val = strings.Repeat("0", w-len(val)) + val
			}
		}
		s = s[:start] + val + s[end+1:]
	}
}

// parseWidth parses a %0Nd width specifier.
func parseWidth(s string) (int, error) {
	if len(s) < 3 || s[0] != '%' || s[1] != '0' || s[len(s)-1] != 'd' {
		return 0, fmt.Errorf("invalid width specifier %q", s)
	}
	n, err := strconv.Atoi(s[2 : len(s)-1])
	if err != nil || n <= 0 || n > 32 {
		return 0, fmt.Errorf("invalid width specifier %q", s)
	}
	return n, nil
}
