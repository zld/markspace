package manifest

import (
	"strconv"
	"strings"

	"streamcheck/core"
)

// ParseHLS parses a media playlist limited to the supported tag set:
// EXTINF, EXT-X-MEDIA-SEQUENCE, EXT-X-DISCONTINUITY (plus TARGETDURATION,
// VERSION and ENDLIST, which are validated or ignored). EXT-X-DISCONTINUITY
// opens a new continuity interval and is never treated as a missing
// segment.
func ParseHLS(data []byte, source string) (*Timeline, error) {
	lines := strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != "#EXTM3U" {
		return nil, core.E(core.KindManifest, "parse-m3u8", "missing #EXTM3U header", nil).At(source, -1)
	}
	rep := Representation{ID: source}
	var (
		mediaSeq  int64
		seqSet    bool
		pendDur   *core.Rat
		pendDisc  bool
		segNo     int
		lineNo    int
	)
	fail := func(msg string) *core.Error {
		return core.E(core.KindManifest, "parse-m3u8", msg, nil).At(source, int64(lineNo))
	}
	for _, raw := range lines {
		lineNo++
		line := strings.TrimSpace(raw)
		if line == "" || line == "#EXTM3U" {
			continue
		}
		switch {
		case strings.HasPrefix(line, "#EXT-X-MEDIA-SEQUENCE:"):
			v, err := strconv.ParseInt(strings.TrimPrefix(line, "#EXT-X-MEDIA-SEQUENCE:"), 10, 64)
			if err != nil {
				return nil, fail("bad MEDIA-SEQUENCE: " + err.Error())
			}
			mediaSeq, seqSet = v, true
		case strings.HasPrefix(line, "#EXTINF:"):
			d := strings.TrimPrefix(line, "#EXTINF:")
			d, _, _ = strings.Cut(d, ",")
			dur, err := core.ParseDecimal(d)
			if err != nil {
				return nil, fail("bad EXTINF: " + err.Error())
			}
			pendDur = &dur
		case line == "#EXT-X-DISCONTINUITY":
			pendDisc = true
		case strings.HasPrefix(line, "#EXT-X-TARGETDURATION:"),
			strings.HasPrefix(line, "#EXT-X-VERSION:"),
			line == "#EXT-X-ENDLIST":
			// recognized, no timeline effect
		case strings.HasPrefix(line, "#EXT-X-"):
			return nil, fail("unsupported tag: " + line)
		case strings.HasPrefix(line, "#"):
			// comments ignored
		default:
			if pendDur == nil {
				return nil, fail("segment URI without EXTINF: " + line)
			}
			idx := segNo
			if seqSet {
				idx = int(mediaSeq) + segNo
			}
			rep.Segments = append(rep.Segments, Segment{
				Index:         idx,
				Dur:           *pendDur,
				URI:           line,
				Discontinuity: pendDisc,
			})
			segNo++
			pendDur, pendDisc = nil, false
		}
	}
	if len(rep.Segments) == 0 {
		return nil, fail("playlist has no segments")
	}
	// Assign start times by accumulating durations; a discontinuity restarts
	// the local clock at the accumulated end (the interval boundary).
	tl := &Timeline{Source: "hls", Reps: []Representation{rep}}
	var cur core.Rat
	for i := range tl.Reps[0].Segments {
		tl.Reps[0].Segments[i].Start = cur
		cur = cur.Add(tl.Reps[0].Segments[i].Dur)
	}
	return tl, nil
}
