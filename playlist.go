package mediacore

import (
	"bytes"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
)

// Format identifies a manifest syntax.
type Format string

const (
	// FormatHLS is an extended M3U playlist with X-MEDIACORE-* tags.
	FormatHLS Format = "hls"
	// FormatDASH is a minimal MPD-like XML document.
	FormatDASH Format = "dash"
)

// SegmentRef is one fragment reference parsed from a manifest: its media
// sequence number, declared duration and source position.
type SegmentRef struct {
	URI       string
	Seq       uint32
	Duration  Nano
	Timescale uint32
	Line      int    // 1-based source line (HLS); 1 for DASH elements
	ByteOff   int64  // 0-based byte offset of the declaring line/element
	Kind      string // "media" or "rtp"
	Attrs     map[string]string
}

// TrackDecl is one playable track as declared by a manifest.
type TrackDecl struct {
	ID         uint32
	Kind       string // audio|video
	Timescale  uint32
	Version    string
	DependsOn  []uint32
	TargetRate uint32 // target sample rate for layout/rate conversion; 0 = native
	Target     string // target channel layout name; "" = native
	ColorPrio  uint32 // explicit color metadata priority; 0 = lowest
	Segments   []*SegmentRef
	Line       int
	ByteOff    int64
}

// Playlist is the parsed, position-annotated view of one manifest.
type Playlist struct {
	AssetID string
	Format  Format
	Tracks  []*TrackDecl
}

// ParsePlaylist sniffs the syntax from the first non-blank line and parses r.
// Every returned rejection is a *Error anchored at the offending position.
func ParsePlaylist(asset string, r io.Reader) (*Playlist, error) {
	raw, err := io.ReadAll(r)
	if err != nil {
		return nil, Wrap(err, asset, 0, StagePlaylist, -1)
	}
	head := strings.TrimSpace(string(raw[:min(len(raw), 256)]))
	switch {
	case strings.HasPrefix(head, "#EXTM3U"):
		return parseHLS(asset, raw)
	case strings.HasPrefix(head, "<?xml") || strings.HasPrefix(head, "<MPD"):
		return parseDASH(asset, raw)
	default:
		return nil, WrapMsg(ErrIllegalField, asset, 0, StagePlaylist, 0,
			"unrecognized manifest header")
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// parseDecimalSeconds turns an EXTINF duration ("9.0", "0.02") into exact
// nanoseconds. More than nine fractional digits would lose precision and are
// rejected; so are empty, non-numeric or overflowing values.
func parseDecimalSeconds(asset string, tok string, line int, off int64) (Nano, error) {
	if tok == "" {
		return 0, illegal(asset, "empty EXTINF duration", line, off)
	}
	neg := false
	s := tok
	if s[0] == '-' || s[0] == '+' {
		neg = s[0] == '-'
		s = s[1:]
	}
	parts := strings.SplitN(s, ".", 2)
	whole, err := strconv.ParseUint(parts[0], 10, 64)
	if err != nil || parts[0] == "" {
		return 0, illegal(asset, "bad EXTINF duration: "+tok, line, off)
	}
	var frac uint64
	if len(parts) == 2 {
		f := parts[1]
		if f == "" || len(f) > 9 {
			return 0, illegal(asset, "EXTINF fraction must have 1..9 digits: "+tok, line, off)
		}
		frac, err = strconv.ParseUint(f, 10, 64)
		if err != nil {
			return 0, illegal(asset, "bad EXTINF fraction: "+tok, line, off)
		}
		for i := len(f); i < 9; i++ {
			frac *= 10
		}
	}
	if whole > uint64(maxNano)/uint64(NanosPerSecond) {
		return 0, WrapMsg(ErrOverflow, asset, 0, StagePlaylist, off, "duration overflow")
	}
	ns := Nano(whole)*NanosPerSecond + Nano(frac)
	if neg {
		return 0, illegal(asset, "negative EXTINF duration: "+tok, line, off)
	}
	if ns <= 0 {
		return 0, illegal(asset, "zero EXTINF duration: "+tok, line, off)
	}
	return ns, nil
}

func illegal(asset, msg string, line int, off int64) *Error {
	e := WrapMsg(ErrIllegalField, asset, 0, StagePlaylist, off, msg)
	e.Msg = fmt.Sprintf("line %d: %s", line, msg)
	return e
}

// parseTagAttrs parses a comma-free KEY=VALUE attribute list ("A=1,B=x").
func parseTagAttrs(s string) map[string]string {
	out := map[string]string{}
	for _, kv := range strings.Split(s, ",") {
		kv = strings.TrimSpace(kv)
		if kv == "" {
			continue
		}
		if i := strings.IndexByte(kv, '='); i > 0 {
			out[strings.TrimSpace(kv[:i])] = strings.Trim(strings.TrimSpace(kv[i+1:]), `"`)
		}
	}
	return out
}

// splitLines returns text lines plus the byte offset where each line begins.
func splitLines(raw []byte) ([]string, []int64) {
	var lines []string
	var offs []int64
	var start int64
	for i := 0; i < len(raw); i++ {
		if raw[i] == '\n' {
			lines = append(lines, string(raw[start:i]))
			offs = append(offs, start)
			start = int64(i + 1)
		}
	}
	if start < int64(len(raw)) {
		lines = append(lines, string(raw[start:]))
		offs = append(offs, start)
	}
	return lines, offs
}

func parseHLS(asset string, raw []byte) (*Playlist, error) {
	lines, offs := splitLines(raw)
	pl := &Playlist{AssetID: asset, Format: FormatHLS}
	var cur *TrackDecl
	var pending *SegmentRef
	for idx, line := range lines {
		no, off := idx+1, offs[idx]
		trim := strings.TrimSpace(line)
		if trim == "" || trim == "#EXTM3U" {
			continue
		}
		switch {
		case strings.HasPrefix(trim, "#EXT-X-MEDIACORE-ASSET:"):
			pl.AssetID = attrOne(parseTagAttrs(trim[len("#EXT-X-MEDIACORE-ASSET:"):]), "ID", asset)
		case strings.HasPrefix(trim, "#EXT-X-MEDIACORE-TRACK:"):
			t, err := buildTrack(asset, parseTagAttrs(trim[len("#EXT-X-MEDIACORE-TRACK:"):]), no, off)
			if err != nil {
				return nil, err
			}
			pl.Tracks = append(pl.Tracks, t)
			cur = t
		case strings.HasPrefix(trim, "#EXTINF:"):
			if cur == nil {
				return nil, illegal(asset, "EXTINF without a track", no, off)
			}
			body := trim[len("#EXTINF:"):]
			// Standard form is "duration,optional-title"; keep attrs in title.
			durTok := body
			title := ""
			if i := strings.IndexByte(body, ','); i >= 0 {
				durTok, title = body[:i], body[i+1:]
			}
			ns, err := parseDecimalSeconds(asset, strings.TrimSpace(durTok), no, off)
			if err != nil {
				return nil, err
			}
			pending = &SegmentRef{Duration: ns, Line: no, ByteOff: off,
				Timescale: cur.Timescale, Attrs: parseTagAttrs(title)}
		case strings.HasPrefix(trim, "#"):
			// Unknown tags are ignored to stay tolerant of standard HLS tags.
			continue
		default:
			if cur == nil {
				return nil, illegal(asset, "segment URI without a track: "+trim, no, off)
			}
			if pending == nil {
				return nil, illegal(asset, "segment URI without EXTINF: "+trim, no, off)
			}
			pending.URI = trim
			applyRefAttrs(pending)
			cur.Segments = append(cur.Segments, pending)
			pending = nil
		}
	}
	if len(pl.Tracks) == 0 {
		return nil, illegal(asset, "no track declarations", 1, 0)
	}
	sort.SliceStable(pl.Tracks, func(i, j int) bool { return pl.Tracks[i].ID < pl.Tracks[j].ID })
	return pl, nil
}

func attrOne(attrs map[string]string, key, fallback string) string {
	if v, ok := attrs[key]; ok && v != "" {
		return v
	}
	return fallback
}

func buildTrack(asset string, a map[string]string, line int, off int64) (*TrackDecl, error) {
	id, err := reqUint(asset, a, "ID", line, off)
	if err != nil {
		return nil, err
	}
	kind := attrOne(a, "KIND", "video")
	if kind != "audio" && kind != "video" {
		return nil, illegal(asset, "track KIND must be audio|video", line, off)
	}
	ts, err := reqUint(asset, a, "TIMESCALE", line, off)
	if err != nil {
		return nil, err
	}
	if ts == 0 {
		return nil, WrapMsg(ErrZeroTimescale, asset, id, StagePlaylist, off, "line: zero timescale")
	}
	t := &TrackDecl{ID: id, Kind: kind, Timescale: ts, Version: attrOne(a, "VERSION", "v1"),
		Target: attrOne(a, "TARGET-LAYOUT", ""), TargetRate: optUint(a, "TARGET-RATE"),
		ColorPrio: optUint(a, "COLOR-PRIO"), Line: line, ByteOff: off}
	if d := attrOne(a, "DEPENDS", ""); d != "" {
		for _, part := range strings.Split(d, "/") {
			dep, perr := strconv.ParseUint(strings.TrimSpace(part), 10, 32)
			if perr != nil {
				return nil, illegal(asset, "bad DEPENDS list: "+d, line, off)
			}
			t.DependsOn = append(t.DependsOn, uint32(dep))
		}
	}
	return t, nil
}

func reqUint(asset string, a map[string]string, key string, line int, off int64) (uint32, error) {
	v, ok := a[key]
	if !ok || v == "" {
		return 0, illegal(asset, "missing required attribute "+key, line, off)
	}
	n, err := strconv.ParseUint(v, 10, 32)
	if err != nil {
		return 0, illegal(asset, "bad integer attribute "+key+"="+v, line, off)
	}
	return uint32(n), nil
}

func optUint(a map[string]string, key string) uint32 {
	v, ok := a[key]
	if !ok {
		return 0
	}
	n, err := strconv.ParseUint(v, 10, 32)
	if err != nil {
		return 0
	}
	return uint32(n)
}

func applyRefAttrs(ref *SegmentRef) {
	ref.Seq = optUint(ref.Attrs, "SEQ")
	ref.Kind = attrOne(ref.Attrs, "KIND", "media")
	if ts := optUint(ref.Attrs, "TIMESCALE"); ts != 0 {
		ref.Timescale = ts
	}
}

// parseDASH parses the small MPD subset used by generated fixtures. The XML
// decoder's InputOffset anchors every element at a byte offset.
func parseDASH(asset string, raw []byte) (*Playlist, error) {
	dec := newXMLDecoder(bytes.NewReader(raw))
	pl := &Playlist{AssetID: asset, Format: FormatDASH}
	var cur *TrackDecl
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, WrapMsg(ErrIllegalField, asset, 0, StagePlaylist, dec.InputOffset(),
				"malformed XML: "+err.Error())
		}
		se, ok := tok.(startElement)
		if !ok {
			continue
		}
		off := dec.InputOffset()
		switch se.Name.Local {
		case "MPD":
			if v := attrVal(se, "asset"); v != "" {
				pl.AssetID = v
			}
		case "AdaptationSet":
			a := attrMap(se)
			id, err := reqUint(asset, a, "id", 1, off)
			if err != nil {
				return nil, err
			}
			ts := optUint(a, "timescale")
			if ts == 0 {
				return nil, WrapMsg(ErrZeroTimescale, asset, id, StagePlaylist, off, "zero timescale")
			}
			cur = &TrackDecl{ID: id, Kind: attrOne(a, "kind", "video"), Timescale: ts,
				Version: attrOne(a, "version", "v1"), Target: attrOne(a, "targetLayout", ""),
				TargetRate: optUint(a, "targetRate"), ColorPrio: optUint(a, "colorPrio"),
				Line: 1, ByteOff: off}
			if d := attrOne(a, "depends", ""); d != "" {
				for _, p := range strings.Split(d, " ") {
					if p == "" {
						continue
					}
					dep, perr := strconv.ParseUint(p, 10, 32)
					if perr != nil {
						return nil, illegal(asset, "bad depends list: "+d, 1, off)
					}
					cur.DependsOn = append(cur.DependsOn, uint32(dep))
				}
			}
			pl.Tracks = append(pl.Tracks, cur)
		case "S":
			if cur == nil {
				return nil, illegal(asset, "S outside AdaptationSet", 1, off)
			}
			a := attrMap(se)
			ticks, err := reqUint(asset, a, "d", 1, off)
			if err != nil {
				return nil, err
			}
			if ticks == 0 {
				return nil, WrapMsg(ErrIllegalDuration, asset, cur.ID, StagePlaylist, off,
					"zero segment duration")
			}
			ns, serr := ScaleChecked(int64(ticks), cur.Timescale)
			if serr != nil {
				return nil, WrapMsg(ErrOverflow, asset, cur.ID, StagePlaylist, off, serr.Error())
			}
			ref := &SegmentRef{URI: attrOne(a, "uri", ""), Seq: optUint(a, "n"),
				Duration: ns, Timescale: cur.Timescale, Line: 1, ByteOff: off,
				Kind: attrOne(a, "kind", "media"), Attrs: a}
			if ref.URI == "" {
				return nil, illegal(asset, "S missing uri", 1, off)
			}
			cur.Segments = append(cur.Segments, ref)
		}
	}
	if len(pl.Tracks) == 0 {
		return nil, illegal(asset, "no AdaptationSet", 1, 0)
	}
	sort.SliceStable(pl.Tracks, func(i, j int) bool { return pl.Tracks[i].ID < pl.Tracks[j].ID })
	return pl, nil
}
