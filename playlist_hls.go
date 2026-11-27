package mediacore

import (
	"sort"
	"strconv"
	"strings"
)

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
		Rate: optUint(a, "RATE"), Layout: attrOne(a, "LAYOUT", ""),
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

func applyRefAttrs(ref *SegmentRef) {
	ref.Seq = optUint(ref.Attrs, "SEQ")
	ref.Kind = attrOne(ref.Attrs, "KIND", "media")
	if ts := optUint(ref.Attrs, "TIMESCALE"); ts != 0 {
		ref.Timescale = ts
	}
}
