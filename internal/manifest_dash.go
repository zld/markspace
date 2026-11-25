package continuity

import (
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
)

// DASH support is intentionally a documented subset: static MPDs whose
// representations carry a SegmentTemplate with a SegmentTimeline. SegmentList,
// SegmentBase and duration-only SegmentTemplate are rejected with a positioned
// error rather than silently approximated.

const (
	maxXMLDepth = 64
	maxXMLNodes = 200000
)

type xmlNode struct {
	name     string
	attrs    []xml.Attr
	children []*xmlNode
	text     string
	offset   int64
}

func parseXMLTree(name string, data []byte) (*xmlNode, error) {
	dec := xml.NewDecoder(strings.NewReader(string(data)))
	dec.Strict = true
	root := &xmlNode{name: "#document"}
	stack := []*xmlNode{root}
	nodes := 0
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, NewError(KindParse, Location{File: name, Offset: dec.InputOffset(), Sample: -1}, "malformed XML: %v", err)
		}
		off := dec.InputOffset()
		switch t := tok.(type) {
		case xml.StartElement:
			if len(stack) >= maxXMLDepth {
				return nil, NewError(KindParse, Location{File: name, Offset: off, Sample: -1}, "XML nesting exceeds %d levels", maxXMLDepth)
			}
			nodes++
			if nodes > maxXMLNodes {
				return nil, NewError(KindParse, Location{File: name, Offset: off, Sample: -1}, "XML element count exceeds %d", maxXMLNodes)
			}
			for i := 1; i < len(t.Attr); i++ {
				for j := 0; j < i; j++ {
					if t.Attr[i].Name == t.Attr[j].Name {
						return nil, NewError(KindParse, Location{File: name, Offset: off, Sample: -1}, "duplicate attribute %q on <%s>", t.Attr[i].Name.Local, t.Name.Local)
					}
				}
			}
			nd := &xmlNode{name: t.Name.Local, attrs: t.Attr, offset: off}
			parent := stack[len(stack)-1]
			parent.children = append(parent.children, nd)
			stack = append(stack, nd)
		case xml.EndElement:
			if len(stack) <= 1 {
				return nil, NewError(KindParse, Location{File: name, Offset: off, Sample: -1}, "unexpected closing element </%s>", t.Name.Local)
			}
			stack = stack[:len(stack)-1]
		case xml.CharData:
			stack[len(stack)-1].text += string(t)
		case xml.Directive:
			// DTDs and other directives carry no meaning for this subset.
		}
	}
	if len(stack) != 1 {
		nd := stack[len(stack)-1]
		return nil, NewError(KindParse, Location{File: name, Offset: nd.offset, Sample: -1}, "unterminated element <%s>", nd.name)
	}
	if len(root.children) != 1 || root.children[0].name != "MPD" {
		off := int64(0)
		if len(root.children) > 0 {
			off = root.children[0].offset
		}
		return nil, NewError(KindParse, Location{File: name, Offset: off, Sample: -1}, "root element must be <MPD>")
	}
	return root.children[0], nil
}

func (n *xmlNode) attr(name string) (string, bool) {
	for _, a := range n.attrs {
		if a.Name.Local == name {
			return a.Value, true
		}
	}
	return "", false
}

func (n *xmlNode) attrDefault(name, def string) string {
	if v, ok := n.attr(name); ok {
		return v
	}
	return def
}

func (n *xmlNode) child(name string) *xmlNode {
	for _, c := range n.children {
		if c.name == name {
			return c
		}
	}
	return nil
}

func (n *xmlNode) childrenNamed(name string) []*xmlNode {
	var out []*xmlNode
	for _, c := range n.children {
		if c.name == name {
			out = append(out, c)
		}
	}
	return out
}

// ParseDASH parses a static MPD using SegmentTemplate + SegmentTimeline.
func ParseDASH(name string, data []byte) (*Manifest, error) {
	if len(data) > MaxManifestBytes {
		return nil, NewError(KindParse, Location{File: name, Offset: 0, Sample: -1}, "manifest is %d bytes, limit %d", len(data), MaxManifestBytes)
	}
	data = stripBOM(data)
	root, err := parseXMLTree(name, data)
	if err != nil {
		return nil, err
	}
	loc := func(n *xmlNode) Location { return Location{File: name, Offset: n.offset, Sample: -1} }
	if typ, ok := root.attr("type"); ok && typ != "static" {
		return nil, NewError(KindUnsupported, loc(root), "MPD type %q is not supported; only static", typ)
	}
	m := &Manifest{Kind: ManifestDASH, Path: name}
	periods := root.childrenNamed("Period")
	if len(periods) == 0 {
		return nil, NewError(KindParse, loc(root), "MPD has no Period")
	}
	var periodStart Ticks
	globalDuration, hasGlobal := root.attr("mediaPresentationDuration")
	var overall Ticks
	if hasGlobal {
		overall, err = parseISO8601Duration(globalDuration)
		if err != nil {
			return nil, NewError(KindParse, loc(root), "invalid mediaPresentationDuration: %v", err)
		}
	}
	for pi, p := range periods {
		if raw, ok := p.attr("start"); ok {
			periodStart, err = parseISO8601Duration(raw)
			if err != nil {
				return nil, NewError(KindParse, loc(p), "invalid Period@start: %v", err)
			}
		}
		var periodDur Ticks
		if raw, ok := p.attr("duration"); ok {
			periodDur, err = parseISO8601Duration(raw)
			if err != nil {
				return nil, NewError(KindParse, loc(p), "invalid Period@duration: %v", err)
			}
		} else if hasGlobal && len(periods) == 1 {
			periodDur = overall
		}
		base := ""
		if bu := p.child("BaseURL"); bu != nil {
			base, err = joinRef("", strings.TrimSpace(bu.text))
			if err != nil {
				return nil, NewError(KindPathEscape, loc(bu), "BaseURL rejected: %v", err)
			}
		}
		nextStart := periodStart
		for _, as := range p.childrenNamed("AdaptationSet") {
			asBase := base
			if bu := as.child("BaseURL"); bu != nil {
				asBase, err = joinRef(base, strings.TrimSpace(bu.text))
				if err != nil {
					return nil, NewError(KindPathEscape, loc(bu), "BaseURL rejected: %v", err)
				}
			}
			reps := as.childrenNamed("Representation")
			if len(reps) == 0 {
				return nil, NewError(KindParse, loc(as), "AdaptationSet has no Representation")
			}
			for _, rn := range reps {
				rep, repStart, err := parseDASHRepresentation(name, pi, periodStart, periodDur, as, asBase, rn)
				if err != nil {
					return nil, err
				}
				if repStart > nextStart {
					nextStart = repStart
				}
				m.Representations = append(m.Representations, *rep)
			}
		}
		if periodDur > 0 {
			periodStart += periodDur
		} else if len(periods) > 1 {
			return nil, NewError(KindUnsupported, loc(p), "multi-Period MPD requires explicit Period@start and @duration")
		}
	}
	if len(m.Representations) == 0 {
		return nil, NewError(KindParse, loc(root), "MPD defines no playable representations")
	}
	return m, nil
}

func stripBOM(b []byte) []byte {
	if len(b) >= 3 && b[0] == 0xEF && b[1] == 0xBB && b[2] == 0xBF {
		return b[3:]
	}
	return b
}

func parseDASHRepresentation(mpdName string, periodIndex int, periodStart, periodDur Ticks, as *xmlNode, base string, rn *xmlNode) (*Representation, Ticks, error) {
	loc := func(n *xmlNode) Location { return Location{File: mpdName, Offset: n.offset, Sample: -1} }
	repID, ok := rn.attr("id")
	if !ok || strings.TrimSpace(repID) == "" {
		return nil, 0, NewError(KindParse, loc(rn), "Representation@id is required")
	}
	kind, mime := inferMediaKind(as, rn)
	_ = mime
	if kind == MediaUnknown {
		return nil, 0, NewError(KindUnsupported, loc(rn), "representation %q has no video/audio contentType or mimeType", repID)
	}

	tplNode := rn.child("SegmentTemplate")
	if tplNode == nil {
		tplNode = as.child("SegmentTemplate")
	}
	if tplNode == nil {
		return nil, 0, NewError(KindUnsupported, loc(rn), "representation %q has no SegmentTemplate; only SegmentTemplate with SegmentTimeline is supported", repID)
	}
	timeline := tplNode.child("SegmentTimeline")
	if timeline == nil {
		return nil, 0, NewError(KindUnsupported, loc(tplNode), "representation %q uses SegmentTemplate without SegmentTimeline", repID)
	}
	for _, unsupported := range []string{"SegmentList", "SegmentBase"} {
		if rn.child(unsupported) != nil || as.child(unsupported) != nil {
			return nil, 0, NewError(KindUnsupported, loc(rn), "representation %q uses %s; only SegmentTemplate is supported", repID, unsupported)
		}
	}

	rsBase := base
	if bu := rn.child("BaseURL"); bu != nil {
		var err error
		rsBase, err = joinRef(base, strings.TrimSpace(bu.text))
		if err != nil {
			return nil, 0, NewError(KindPathEscape, loc(bu), "BaseURL rejected: %v", err)
		}
	}

	timescale, err := uintAttr(tplNode, "timescale", 1)
	if err != nil {
		return nil, 0, NewError(KindParse, loc(tplNode), "invalid SegmentTemplate@timescale: %v", err)
	}
	if timescale == 0 || timescale > uint64(^uint32(0)) {
		return nil, 0, NewError(KindParse, loc(tplNode), "SegmentTemplate@timescale %d out of range", timescale)
	}
	mediaTpl, ok := tplNode.attr("media")
	if !ok || mediaTpl == "" {
		return nil, 0, NewError(KindParse, loc(tplNode), "SegmentTemplate@media is required")
	}
	initTpl := tplNode.attrDefault("initialization", "")
	startNumber := int64(1)
	if raw, ok := tplNode.attr("startNumber"); ok {
		startNumber, err = strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
		if err != nil || startNumber < 0 {
			return nil, 0, NewError(KindParse, loc(tplNode), "invalid SegmentTemplate@startNumber %q", raw)
		}
	}
	var pto int64
	if raw, ok := tplNode.attr("presentationTimeOffset"); ok {
		pto, err = strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
		if err != nil || pto < 0 {
			return nil, 0, NewError(KindParse, loc(tplNode), "invalid SegmentTemplate@presentationTimeOffset %q", raw)
		}
	}
	bandwidth := int64(0)
	if raw, ok := rn.attr("bandwidth"); ok {
		bandwidth, err = strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
		if err != nil || bandwidth < 0 {
			return nil, 0, NewError(KindParse, loc(rn), "invalid Representation@bandwidth %q", raw)
		}
	}
	width := 0
	if raw, ok := firstAttr([]*xmlNode{rn, as}, "width"); ok {
		width, _ = strconv.Atoi(raw)
	}
	height := 0
	if raw, ok := firstAttr([]*xmlNode{rn, as}, "height"); ok {
		height, _ = strconv.Atoi(raw)
	}
	codecs, ok := rn.attr("codecs")
	if !ok {
		codecs, _ = as.attr("codecs")
	}

	rep := &Representation{
		ID:        repID,
		Kind:      kind,
		Timescale: uint32(timescale),
		Codecs:    codecs,
		Width:     width,
		Height:    height,
		Bandwidth: bandwidth,
		Line:      lineOf(loc(rn)),
	}

	if initTpl != "" {
		uri, err := formatTemplate(initTpl, repID, bandwidth, startNumber, 0)
		if err != nil {
			return nil, 0, NewError(KindParse, loc(tplNode), "invalid initialization template: %v", err)
		}
		rep.InitURI, err = joinRef(rsBase, uri)
		if err != nil {
			return nil, 0, NewError(KindPathEscape, loc(tplNode), "initialization URI rejected: %v", err)
		}
		if rep.InitURI != "" {
			if err := validateRelativeRef(rep.InitURI); err != nil {
				return nil, 0, NewError(KindPathEscape, loc(tplNode), "initialization URI rejected: %v", err)
			}
		}
	}

	entries := timeline.childrenNamed("S")
	if len(entries) == 0 {
		return nil, 0, NewError(KindParse, loc(timeline), "SegmentTimeline contains no <S> entries")
	}
	var (
		cur        int64
		haveCur    bool
		number     = startNumber
		firstStart Ticks
		lastEnd    Ticks
	)
	appendSeg := func(startMedia, durMedia int64, entry *xmlNode) error {
		if durMedia <= 0 {
			return NewError(KindParse, loc(entry), "segment duration %d must be positive", durMedia)
		}
		if len(rep.Segments) >= MaxSegmentsPerRepresentation {
			return NewError(KindParse, loc(entry), "segment count exceeds limit %d", MaxSegmentsPerRepresentation)
		}
		relMedia := startMedia - pto
		startBase, err := Rescale(relMedia, rep.Timescale, BaseTimescale)
		if err != nil {
			return NewError(KindParse, loc(entry), "segment start does not fit the 90 kHz base: %v", err)
		}
		durBase, err := Rescale(durMedia, rep.Timescale, BaseTimescale)
		if err != nil {
			return NewError(KindParse, loc(entry), "segment duration does not fit the 90 kHz base: %v", err)
		}
		uri, err := formatTemplate(mediaTpl, repID, bandwidth, number, Ticks(startMedia))
		if err != nil {
			return NewError(KindParse, loc(entry), "invalid media template: %v", err)
		}
		joined, err := joinRef(rsBase, uri)
		if err != nil {
			return NewError(KindPathEscape, loc(entry), "segment URI rejected: %v", err)
		}
		if err := validateRelativeRef(joined); err != nil {
			return NewError(KindPathEscape, loc(entry), "segment URI rejected: %v", err)
		}
		start := periodStart + Ticks(startBase)
		if len(rep.Segments) == 0 {
			firstStart = start
		} else if start < lastEnd {
			// Overlap inside a manifest is a hard fault: the mux would repeat.
			return NewError(KindBadReference, loc(entry), "segment %d starts at %d before previous end %d", number, start, lastEnd)
		}
		rep.Segments = append(rep.Segments, SegmentRef{
			Number:   number,
			URI:      joined,
			InitURI:  rep.InitURI,
			Start:    start,
			Duration: Ticks(durBase),
			Line:     lineOf(loc(entry)),
		})
		lastEnd = start + Ticks(durBase)
		number++
		return nil
	}
	for _, entry := range entries {
		durMedia, err := intAttrRequired(entry, "d")
		if err != nil || durMedia <= 0 {
			return nil, 0, NewError(KindParse, loc(entry), "invalid S@d %q", entry.attrDefault("d", ""))
		}
		if raw, ok := entry.attr("t"); ok {
			cur, err = strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
			if err != nil || cur < 0 {
				return nil, 0, NewError(KindParse, loc(entry), "invalid S@t %q", raw)
			}
			haveCur = true
		} else if !haveCur {
			cur = 0
			haveCur = true
		}
		repeat, err := intAttrOptional(entry, "r", 0)
		if err != nil || repeat < -1 {
			return nil, 0, NewError(KindParse, loc(entry), "invalid S@r %q", entry.attrDefault("r", ""))
		}
		reps := repeat + 1
		if repeat == -1 {
			if periodDur <= 0 {
				return nil, 0, NewError(KindUnsupported, loc(entry), "S@r=-1 requires a bounded Period duration")
			}
			// Repeat until the period end; at least one segment.
			periodEndMedia := int64(periodDur)*int64(rep.Timescale)/int64(BaseTimescale) + pto
			reps = 1
			for cur+int64(reps)*durMedia < periodEndMedia {
				reps++
				if reps > MaxSegmentsPerRepresentation {
					return nil, 0, NewError(KindParse, loc(entry), "S@r=-1 expansion exceeds limit %d", MaxSegmentsPerRepresentation)
				}
			}
		}
		for i := int64(0); i < reps; i++ {
			if err := appendSeg(cur, durMedia, entry); err != nil {
				return nil, 0, err
			}
			cur += durMedia
		}
	}
	if len(rep.Segments) == 0 {
		return nil, 0, NewError(KindParse, loc(timeline), "SegmentTimeline produced no segments")
	}
	return rep, firstStart, nil
}

func lineOf(Location) int { return 0 }

func uintAttr(n *xmlNode, name string, def uint64) (uint64, error) {
	raw, ok := n.attr(name)
	if !ok || strings.TrimSpace(raw) == "" {
		return def, nil
	}
	v, err := strconv.ParseUint(strings.TrimSpace(raw), 10, 64)
	if err != nil {
		return 0, err
	}
	return v, nil
}

func intAttrRequired(n *xmlNode, name string) (int64, error) {
	raw, ok := n.attr(name)
	if !ok {
		return 0, fmt.Errorf("missing attribute %s", name)
	}
	return strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
}

func intAttrOptional(n *xmlNode, name string, def int64) (int64, error) {
	raw, ok := n.attr(name)
	if !ok || strings.TrimSpace(raw) == "" {
		return def, nil
	}
	return strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
}

// firstAttr returns the first value of name found on the given nodes.
func firstAttr(nodes []*xmlNode, name string) (string, bool) {
	for _, n := range nodes {
		if n == nil {
			continue
		}
		if raw, ok := n.attr(name); ok {
			return strings.TrimSpace(raw), true
		}
	}
	return "", false
}

// joinRef joins a relative reference onto a manifest-relative base directory.
// Both components are validated; escapes are rejected.
func joinRef(base, ref string) (string, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return "", nil
	}
	if strings.HasPrefix(ref, "/") {
		return "", errors.New("absolute paths are not allowed")
	}
	if i := strings.IndexByte(ref, ':'); i >= 0 {
		slash := strings.IndexByte(ref, '/')
		if slash < 0 || i < slash {
			return "", errors.New("URI schemes are not allowed")
		}
	}
	if base != "" {
		if err := validateRefAgainst("", base); err != nil {
			return "", err
		}
	}
	parts := []string{}
	if base != "" {
		parts = append(parts, strings.Split(base, "/")...)
	}
	for _, seg := range strings.Split(ref, "/") {
		if seg == "" || seg == "." {
			continue
		}
		if seg == ".." {
			if len(parts) == 0 {
				return "", errors.New("path escapes the manifest directory")
			}
			parts = parts[:len(parts)-1]
			continue
		}
		if err := validateRefSyntax(seg); err != nil {
			return "", err
		}
		parts = append(parts, seg)
	}
	out := strings.Join(parts, "/")
	if out == "" {
		return "", nil
	}
	return out, nil
}

func parseISO8601Duration(s string) (Ticks, error) {
	orig := s
	s = strings.TrimSpace(s)
	if s == "" || s[0] != 'P' {
		return 0, fmt.Errorf("expected ISO-8601 duration, got %q", orig)
	}
	s = s[1:]
	var total Ticks
	inTime := false
	consumed := false
	seen := map[byte]bool{}
	for s != "" {
		if s[0] == 'T' {
			if inTime {
				return 0, fmt.Errorf("duplicate T in %q", orig)
			}
			inTime = true
			s = s[1:]
			continue
		}
		end := 0
		for end < len(s) && (s[end] >= '0' && s[end] <= '9' || s[end] == '.') {
			end++
		}
		if end == 0 || end == len(s) {
			return 0, fmt.Errorf("invalid component in %q", orig)
		}
		num := s[:end]
		unit := s[end]
		s = s[end+1:]
		consumed = true
		key := unit
		if unit == 'M' && !inTime {
			key = 'm' // months, rejected below, never collides with minutes
		}
		if seen[key] {
			return 0, fmt.Errorf("duplicate %q component in %q", string(unit), orig)
		}
		seen[key] = true
		switch unit {
		case 'D':
			if inTime {
				return 0, fmt.Errorf("days inside time part in %q", orig)
			}
			secs, err := ParseDecimalTicks(num, BaseTimescale)
			if err != nil {
				return 0, err
			}
			total += secs * 86400
		case 'H':
			if !inTime {
				return 0, fmt.Errorf("hours outside time part in %q", orig)
			}
			secs, err := ParseDecimalTicks(num, BaseTimescale)
			if err != nil {
				return 0, err
			}
			total += secs * 3600
		case 'M':
			if !inTime {
				return 0, fmt.Errorf("months are not supported in %q", orig)
			}
			secs, err := ParseDecimalTicks(num, BaseTimescale)
			if err != nil {
				return 0, err
			}
			total += secs * 60
		case 'S':
			if !inTime {
				return 0, fmt.Errorf("seconds outside time part in %q", orig)
			}
			secs, err := ParseDecimalTicks(num, BaseTimescale)
			if err != nil {
				return 0, err
			}
			total += secs
		default:
			return 0, fmt.Errorf("unsupported duration unit %q in %q", unit, orig)
		}
	}
	if !consumed {
		return 0, fmt.Errorf("empty duration %q", orig)
	}
	return total, nil
}

func inferMediaKind(as, rn *xmlNode) (MediaKind, string) {
	for _, n := range []*xmlNode{rn, as} {
		if v, ok := n.attr("contentType"); ok {
			switch strings.ToLower(v) {
			case "video":
				return MediaVideo, v
			case "audio":
				return MediaAudio, v
			}
		}
	}
	for _, n := range []*xmlNode{rn, as} {
		if v, ok := n.attr("mimeType"); ok {
			switch {
			case strings.HasPrefix(strings.ToLower(v), "video/"):
				return MediaVideo, v
			case strings.HasPrefix(strings.ToLower(v), "audio/"):
				return MediaAudio, v
			}
		}
	}
	return MediaUnknown, ""
}

// Primary returns the deterministic rendition per media kind: lowest bandwidth,
// then lexicographic representation ID.
func (m *Manifest) Primary() []Representation {
	best := map[MediaKind]*Representation{}
	var order []MediaKind
	for i := range m.Representations {
		r := &m.Representations[i]
		cur, ok := best[r.Kind]
		if !ok {
			best[r.Kind] = r
			order = append(order, r.Kind)
			continue
		}
		if r.Bandwidth < cur.Bandwidth || (r.Bandwidth == cur.Bandwidth && r.ID < cur.ID) {
			best[r.Kind] = r
		}
	}
	sort.SliceStable(order, func(i, j int) bool { return order[i] < order[j] })
	out := make([]Representation, 0, len(order))
	for _, k := range order {
		out = append(out, *best[k])
	}
	return out
}

func formatTemplate(tpl string, repID string, bandwidth int64, number int64, timeTicks Ticks) (string, error) {
	var b strings.Builder
	for i := 0; i < len(tpl); i++ {
		c := tpl[i]
		if c != '$' {
			b.WriteByte(c)
			continue
		}
		end := strings.IndexByte(tpl[i+1:], '$')
		if end < 0 {
			return "", errors.New("unterminated $ placeholder")
		}
		body := tpl[i+1 : i+1+end]
		i += end + 1
		if body == "" {
			b.WriteByte('$')
			continue
		}
		name, format := body, "%d"
		if p := strings.IndexByte(body, '%'); p >= 0 {
			name, format = body[:p], body[p:]
		}
		var val int64
		switch name {
		case "RepresentationID":
			if format != "%d" {
				return "", fmt.Errorf("$RepresentationID$ cannot carry a format specifier")
			}
			b.WriteString(repID)
			continue
		case "Number":
			val = number
		case "Time":
			val = int64(timeTicks)
		case "Bandwidth":
			val = bandwidth
		default:
			return "", fmt.Errorf("unsupported placeholder $%s$", body)
		}
		formatted, err := formatDecimal(format, val)
		if err != nil {
			return "", err
		}
		b.WriteString(formatted)
	}
	return b.String(), nil
}

func formatDecimal(format string, v int64) (string, error) {
	if format == "%d" {
		return strconv.FormatInt(v, 10), nil
	}
	if len(format) < 3 || format[0] != '%' || format[len(format)-1] != 'd' {
		return "", fmt.Errorf("unsupported format specifier %q", format)
	}
	spec := format[1 : len(format)-1]
	width := 0
	if spec[0] == '0' {
		spec = spec[1:]
	}
	if spec == "" {
		return "", fmt.Errorf("invalid format specifier %q", format)
	}
	for i := 0; i < len(spec); i++ {
		if spec[i] < '0' || spec[i] > '9' {
			return "", fmt.Errorf("invalid format specifier %q", format)
		}
		width = width*10 + int(spec[i]-'0')
	}
	if width > 20 {
		return "", fmt.Errorf("format width %d exceeds 20", width)
	}
	if v < 0 {
		return "", fmt.Errorf("negative template value %d", v)
	}
	return fmt.Sprintf("%0*d", width, v), nil
}
