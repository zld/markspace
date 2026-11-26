package manifest

import (
	"fmt"
	"strings"

	"medtrace/internal/core"
)

// parseHLS parses an HLS media playlist (the dialect that lists segments).
// Master playlists are rejected: replaying a rendition requires the caller to
// choose one, which medtrace does not guess. The parser tracks the
// #EXT-X-DISCONTINUITY flag and the #EXT-X-PROGRAM-DATE-TIME anchor so the
// epoch tracker can be told exactly where the source timeline jumps.
func parseHLS(rel string, data []byte) (*Playlist, error) {
	pl := &Playlist{Kind: HLSKind, File: rel}
	ls := lines(string(data))
	if len(ls) == 0 || strings.TrimSpace(ls[0].Text) != "#EXTM3U" {
		line := 1
		if len(ls) > 0 {
			line = ls[0].Num
		}
		return nil, hlsFault(rel, line, "playlist does not begin with #EXTM3U")
	}
	var (
		targetDur   core.Nanos
		hasTarget   bool
		mediaSeq    int64
		hasSeq      bool
		pendingDur  core.Nanos
		haveDur     bool
		pendingDisc bool
		pendingPDT  int64
		havePDT     bool
		segIndex    int64
		seenSeg     bool
	)
	for i := 1; i < len(ls); i++ {
		l := ls[i]
		text := l.trim()
		if text == "" {
			continue
		}
		switch {
		case strings.HasPrefix(text, "#EXT-X-STREAM-INF"):
			return nil, hlsFault(rel, l.Num, "master playlist is not supported; pass a media playlist")
		case strings.HasPrefix(text, "#EXT-X-TARGETDURATION:"):
			v := strings.TrimPrefix(text, "#EXT-X-TARGETDURATION:")
			n, err := parseUint(v)
			if err != nil || n == 0 {
				return nil, hlsFault(rel, l.Num, fmt.Sprintf("invalid target duration %q", v))
			}
			targetDur, hasTarget = core.Nanos(n)*core.Second, true
		case strings.HasPrefix(text, "#EXT-X-MEDIA-SEQUENCE:"):
			v := strings.TrimPrefix(text, "#EXT-X-MEDIA-SEQUENCE:")
			n, err := parseUint(v)
			if err != nil {
				return nil, hlsFault(rel, l.Num, fmt.Sprintf("invalid media sequence %q", v))
			}
			mediaSeq, hasSeq = int64(n), true
		case strings.HasPrefix(text, "#EXT-X-DISCONTINUITY-TYPE"):
			return nil, hlsFault(rel, l.Num, "#EXT-X-DISCONTINUITY-TYPE is not supported")
		case text == "#EXT-X-DISCONTINUITY":
			pendingDisc = true
		case strings.HasPrefix(text, "#EXT-X-PROGRAM-DATE-TIME:"):
			v := strings.TrimPrefix(text, "#EXT-X-PROGRAM-DATE-TIME:")
			nanos, err := parseProgramDateTime(v)
			if err != nil {
				return nil, hlsFault(rel, l.Num, err.Error())
			}
			pendingPDT, havePDT = nanos, true
		case strings.HasPrefix(text, "#EXTINF:"):
			v := strings.TrimPrefix(text, "#EXTINF:")
			if comma := strings.IndexByte(v, ','); comma >= 0 {
				v = v[:comma]
			}
			n, err := parseDecimalDuration(v)
			if err != nil {
				return nil, hlsFault(rel, l.Num, fmt.Sprintf("invalid #EXTINF duration %q", v))
			}
			if n < 0 {
				return nil, hlsFault(rel, l.Num, "negative #EXTINF duration")
			}
			pendingDur, haveDur = core.Nanos(n), true
		case strings.HasPrefix(text, "#"):
			// Unknown tags are ignored per RFC 8216.
		default:
			if !haveDur {
				return nil, hlsFault(rel, l.Num, fmt.Sprintf("segment %q has no preceding #EXTINF", text))
			}
			if hasTarget && pendingDur > 2*targetDur {
				return nil, hlsFault(rel, l.Num,
					fmt.Sprintf("segment duration %s exceeds twice the target duration %s", pendingDur, targetDur))
			}
			uri, f := resolveRel(rel, ref{URIPath: text, Line: l.Num})
			if f != nil {
				return nil, f
			}
			seq := segIndex
			if hasSeq {
				seq = mediaSeq + segIndex
			}
			seg := Segment{
				URI:             uri,
				Line:            l.Num,
				Duration:        pendingDur,
				Sequence:        seq,
				Discontinuity:   pendingDisc,
				AnchorUnixNanos: pendingPDT,
			}
			if !havePDT {
				seg.AnchorUnixNanos = 0
			}
			pl.Segments = append(pl.Segments, seg)
			segIndex++
			seenSeg = true
			pendingDur, haveDur = 0, false
			pendingDisc = false
			pendingPDT, havePDT = 0, false
		}
	}
	if !seenSeg {
		return nil, hlsFault(rel, 1, "playlist contains no media segments")
	}
	if pendingDisc || haveDur {
		return nil, hlsFault(rel, ls[len(ls)-1].Num, "playlist ends with dangling segment tags")
	}
	if hasTarget && pendingDur == 0 {
		// target duration validated above; nothing further to check.
	}
	return pl, nil
}

func hlsFault(rel string, line int, detail string) *core.Fault {
	return core.NewFault(core.KindManifest, core.Pos{File: rel, Line: line, Byte: -1}, "hls", detail)
}

// parseProgramDateTime converts an RFC 3339 timestamp with optional
// fractional seconds to Unix nanoseconds. The time package is avoided here on
// purpose: its nanosecond field is lossy for the sub-nanosecond digits an
// RFC 3339 timestamp may carry, and medtrace must reject ambiguity rather
// than round it.
func parseProgramDateTime(s string) (int64, error) {
	s = strings.TrimSpace(s)
	return ParseRFC3339Nanos(s)
}
