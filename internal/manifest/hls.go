package manifest

import (
	"bytes"
	"strconv"
	"strings"

	"archive-lab/internal/media"
)

// Limits bounds manifest parsing. Zero means the package default.
type Limits struct {
	MaxSegments int
	MaxLineLen  int
}

// DefaultLimits are applied when Limits fields are zero.
var DefaultLimits = Limits{MaxSegments: 100_000, MaxLineLen: 1 << 20}

func (l Limits) resolved() Limits {
	if l.MaxSegments <= 0 {
		l.MaxSegments = DefaultLimits.MaxSegments
	}
	if l.MaxLineLen <= 0 {
		l.MaxLineLen = DefaultLimits.MaxLineLen
	}
	return l
}

// ParseHLS parses an HLS playlist held in memory. path is used for error
// locations only; parsing never touches the file system.
func ParseHLS(path string, data []byte, limits Limits) (*Playlist, error) {
	limits = limits.resolved()
	pl := &Playlist{Path: path, Version: 1}
	if len(data) >= 3 && bytes.Equal(data[:3], []byte{0xEF, 0xBB, 0xBF}) {
		data = data[3:]
	}
	lines := strings.Split(string(data), "\n")
	if len(lines) > limits.MaxSegments*8 {
		return nil, media.New(media.StageParse, "hls", "playlist has too many lines").WithPath(path)
	}

	var (
		pendingDurNs int64
		pendingLine  int
		pendingRange *ByteRange
		lastRange    *ByteRange
		pendingDisc  bool
		seenMedia    bool
		seq          int64
	)
	for i, raw := range lines {
		lineNo := i + 1
		line := strings.TrimSuffix(raw, "\r")
		if len(line) > limits.MaxLineLen {
			return nil, media.New(media.StageParse, "hls", "line exceeds %d bytes", limits.MaxLineLen).
				WithPath(path).WithLine(lineNo)
		}
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if strings.HasPrefix(trimmed, "#") {
			tag, value := splitTag(trimmed)
			if !strings.HasPrefix(tag, "#EXT") {
				continue
			}
			switch tag {
			case "#EXTM3U":
			case "#EXT-X-VERSION":
				n, err := strconv.Atoi(strings.TrimSpace(value))
				if err != nil || n <= 0 {
					return nil, media.New(media.StageParse, "hls", "invalid EXT-X-VERSION %q", value).
						WithPath(path).WithLine(lineNo)
				}
				pl.Version = n
			case "#EXT-X-TARGETDURATION":
				secs, err := parseNonNegativeInt(value)
				if err != nil {
					return nil, media.New(media.StageParse, "hls", "invalid EXT-X-TARGETDURATION %q", value).
						WithPath(path).WithLine(lineNo)
				}
				pl.TargetDurationNs = secs * media.NanosPerSecond
			case "#EXT-X-MEDIA-SEQUENCE":
				n, err := parseNonNegativeInt(value)
				if err != nil {
					return nil, media.New(media.StageParse, "hls", "invalid EXT-X-MEDIA-SEQUENCE %q", value).
						WithPath(path).WithLine(lineNo)
				}
				seq = n
				pl.MediaSequence = n
			case "#EXT-X-PLAYLIST-TYPE":
				pl.PlaylistType = strings.TrimSpace(value)
				if pl.PlaylistType != "VOD" && pl.PlaylistType != "EVENT" {
					return nil, media.New(media.StageParse, "hls", "unsupported playlist type %q", pl.PlaylistType).
						WithPath(path).WithLine(lineNo)
				}
			case "#EXT-X-ENDLIST":
				pl.EndList = true
			case "#EXTINF":
				durText := strings.TrimSpace(strings.SplitN(value, ",", 2)[0])
				ns, err := parseDecimalNanos(durText)
				if err != nil || ns <= 0 {
					return nil, media.New(media.StageParse, "hls", "invalid EXTINF duration %q", durText).
						WithPath(path).WithLine(lineNo).WithSegment(len(pl.Segments))
				}
				pendingDurNs = ns
				pendingLine = lineNo
				seenMedia = true
			case "#EXT-X-BYTERANGE":
				rng, err := parseByteRange(value, lastRange)
				if err != nil {
					return nil, media.New(media.StageParse, "hls", "%v", err).
						WithPath(path).WithLine(lineNo).WithSegment(len(pl.Segments))
				}
				pendingRange = rng
			case "#EXT-X-DISCONTINUITY":
				pendingDisc = true
			case "#EXT-X-MAP":
				return nil, media.New(media.StageParse, "hls", "EXT-X-MAP initialization segments are not supported").
					WithPath(path).WithLine(lineNo)
			case "#EXT-X-KEY":
				attrs := parseAttributes(value)
				method := strings.ToUpper(attrs["METHOD"])
				if method != "" && method != "NONE" {
					return nil, media.New(media.StageParse, "hls", "encrypted playlists (METHOD=%s) are not supported", method).
						WithPath(path).WithLine(lineNo)
				}
			case "#EXT-X-STREAM-INF":
				if seenMedia {
					return nil, media.New(media.StageParse, "hls", "master and media tags are mixed in one playlist").
						WithPath(path).WithLine(lineNo)
				}
				v, err := parseVariantAttrs(value, lineNo)
				if err != nil {
					return nil, err.WithPath(path)
				}
				pl.Variants = append(pl.Variants, v)
				pl.Kind = KindMaster
			case "#EXT-X-MEDIA":
				if seenMedia {
					return nil, media.New(media.StageParse, "hls", "master and media tags are mixed in one playlist").
						WithPath(path).WithLine(lineNo)
				}
				r, err := parseRenditionAttrs(value, lineNo)
				if err != nil {
					return nil, err.WithPath(path)
				}
				pl.Renditions = append(pl.Renditions, r)
				pl.Kind = KindMaster
			default:
				// Unknown EXT tags are tolerated only when they are comments
				// by convention; known-but-unsupported constructs fail above.
				if isUnsupportedTag(tag) {
					return nil, media.New(media.StageParse, "hls", "unsupported tag %s", tag).
						WithPath(path).WithLine(lineNo)
				}
			}
			continue
		}

		// A URI line.
		if pl.Kind == KindMaster && len(pl.Variants) > 0 && pl.Variants[len(pl.Variants)-1].URI == "" && pendingDurNs == 0 {
			v := &pl.Variants[len(pl.Variants)-1]
			v.URI = trimmed
			if !isLocalRef(trimmed) {
				return nil, media.New(media.StageParse, "hls", "remote URI %q is not supported", trimmed).
					WithPath(path).WithLine(lineNo)
			}
			continue
		}
		if len(pl.Renditions) > 0 && pl.Kind == KindMaster && pl.Variants == nil {
			// A bare URI after EXT-X-MEDIA is not a variant; reject.
			return nil, media.New(media.StageParse, "hls", "unexpected URI %q after EXT-X-MEDIA", trimmed).
				WithPath(path).WithLine(lineNo)
		}
		if pendingDurNs <= 0 {
			return nil, media.New(media.StageParse, "hls", "URI %q has no preceding valid EXTINF", trimmed).
				WithPath(path).WithLine(lineNo).WithSegment(len(pl.Segments))
		}
		if !isLocalRef(trimmed) {
			return nil, media.New(media.StageParse, "hls", "remote URI %q is not supported", trimmed).
				WithPath(path).WithLine(lineNo).WithSegment(len(pl.Segments))
		}
		if len(pl.Segments) >= limits.MaxSegments {
			return nil, media.New(media.StageParse, "hls", "segment count exceeds limit %d", limits.MaxSegments).
				WithPath(path).WithLine(lineNo)
		}
		pl.Kind = KindMedia
		segs, end, err := appendSegment(pl.Segments, trimmed, pendingLine, pl.TotalNs, pendingDurNs, seq, pendingRange, pendingDisc)
		if err != nil {
			return nil, err.WithPath(path).WithLine(pendingLine)
		}
		pl.Segments = segs
		pl.TotalNs = end
		if pendingRange != nil {
			lastRange = pendingRange
		}
		seq++
		pendingDurNs, pendingLine, pendingRange, pendingDisc = 0, 0, nil, false
	}

	if pendingDurNs > 0 {
		return nil, media.New(media.StageParse, "hls", "EXTINF at line %d has no following URI", pendingLine).
			WithPath(path).WithLine(pendingLine).WithSegment(len(pl.Segments))
	}
	for i := range pl.Variants {
		if pl.Variants[i].URI == "" {
			return nil, media.New(media.StageParse, "hls", "EXT-X-STREAM-INF has no following URI").
				WithPath(path).WithLine(pl.Variants[i].Line)
		}
	}
	if pl.Kind == 0 {
		return nil, media.New(media.StageParse, "hls", "playlist contains neither segments nor variants").
			WithPath(path)
	}
	if pl.Kind == KindMedia && !pl.EndList && pl.PlaylistType == "VOD" {
		return nil, media.New(media.StageParse, "hls", "VOD playlist is missing EXT-X-ENDLIST").
			WithPath(path)
	}
	if pl.Kind == KindMedia && len(pl.Segments) == 0 {
		return nil, media.New(media.StageParse, "hls", "media playlist has no segments").
			WithPath(path)
	}
	return pl, nil
}

func splitTag(line string) (tag, value string) {
	if i := strings.IndexByte(line, ':'); i >= 0 {
		return line[:i], line[i+1:]
	}
	return line, ""
}

func parseNonNegativeInt(s string) (int64, error) {
	n, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	if err != nil || n < 0 {
		return 0, errInvalid
	}
	return n, nil
}

var errInvalid = &media.Error{Stage: media.StageParse, Op: "hls", Offset: -1, Segment: -1, Detail: "invalid number"}

// parseDecimalNanos parses a decimal seconds value such as "3.003" into
// integer nanoseconds without floating point.
func parseDecimalNanos(s string) (int64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, errInvalid
	}
	whole, frac, hasFrac := strings.Cut(s, ".")
	if whole == "" {
		whole = "0"
	}
	w, err := strconv.ParseInt(whole, 10, 64)
	if err != nil || w < 0 {
		return 0, errInvalid
	}
	if w > (1<<62)/media.NanosPerSecond {
		return 0, errInvalid
	}
	ns := w * media.NanosPerSecond
	if !hasFrac {
		return ns, nil
	}
	if len(frac) > 9 {
		// Round at nanosecond precision, reject non-digits.
		extra := frac[9:]
		frac = frac[:9]
		for _, c := range extra {
			if c < '0' || c > '9' {
				return 0, errInvalid
			}
		}
	}
	for _, c := range frac {
		if c < '0' || c > '9' {
			return 0, errInvalid
		}
	}
	digits, err := strconv.ParseInt(frac, 10, 64)
	if err != nil {
		return 0, errInvalid
	}
	for i := len(frac); i < 9; i++ {
		digits *= 10
	}
	if ns > (1<<63-1)-digits {
		return 0, errInvalid
	}
	return ns + digits, nil
}

func parseByteRange(s string, previous *ByteRange) (*ByteRange, error) {
	length, offset, hasOffset := strings.Cut(strings.TrimSpace(s), "@")
	n, err := strconv.ParseInt(strings.TrimSpace(length), 10, 64)
	if err != nil || n <= 0 {
		return nil, errInvalid
	}
	rng := &ByteRange{Length: n, Offset: -1}
	if hasOffset {
		o, err := strconv.ParseInt(strings.TrimSpace(offset), 10, 64)
		if err != nil || o < 0 {
			return nil, errInvalid
		}
		rng.Offset = o
	} else {
		if previous == nil {
			return nil, media.New(media.StageParse, "hls", "EXT-X-BYTERANGE without offset has no preceding range")
		}
		rng.Offset = previous.Offset + previous.Length
	}
	return rng, nil
}

// parseAttributes splits an HLS attribute list, honouring quoted values.
func parseAttributes(s string) map[string]string {
	attrs := make(map[string]string)
	for i := 0; i < len(s); {
		for i < len(s) && (s[i] == ',' || s[i] == ' ') {
			i++
		}
		start := i
		for i < len(s) && s[i] != '=' && s[i] != ',' {
			i++
		}
		key := strings.ToUpper(strings.TrimSpace(s[start:i]))
		if i >= len(s) || s[i] != '=' {
			continue
		}
		i++
		var val string
		if i < len(s) && s[i] == '"' {
			i++
			vstart := i
			for i < len(s) && s[i] != '"' {
				i++
			}
			val = s[vstart:i]
			if i < len(s) {
				i++
			}
		} else {
			vstart := i
			for i < len(s) && s[i] != ',' {
				i++
			}
			val = strings.TrimSpace(s[vstart:i])
		}
		if key != "" {
			attrs[key] = val
		}
	}
	return attrs
}

func parseVariantAttrs(value string, line int) (Variant, *media.Error) {
	attrs := parseAttributes(value)
	v := Variant{Line: line}
	bwText, ok := attrs["BANDWIDTH"]
	if !ok {
		return Variant{}, media.New(media.StageParse, "hls", "EXT-X-STREAM-INF is missing BANDWIDTH").WithLine(line)
	}
	bw, err := strconv.ParseInt(bwText, 10, 64)
	if err != nil || bw <= 0 {
		return Variant{}, media.New(media.StageParse, "hls", "invalid BANDWIDTH %q", bwText).WithLine(line)
	}
	v.Bandwidth = bw
	if s, ok := attrs["AVERAGE-BANDWIDTH"]; ok {
		if n, err := strconv.ParseInt(s, 10, 64); err == nil && n >= 0 {
			v.AverageBandwidth = n
		} else {
			return Variant{}, media.New(media.StageParse, "hls", "invalid AVERAGE-BANDWIDTH %q", s).WithLine(line)
		}
	}
	v.Codecs = attrs["CODECS"]
	v.FrameRate = attrs["FRAME-RATE"]
	v.AudioGroup = attrs["AUDIO"]
	v.VideoGroup = attrs["VIDEO"]
	v.SubtitleGroup = attrs["SUBTITLES"]
	if res, ok := attrs["RESOLUTION"]; ok {
		w, h, found := strings.Cut(res, "x")
		wi, err1 := strconv.Atoi(w)
		hi, err2 := strconv.Atoi(h)
		if !found || err1 != nil || err2 != nil || wi <= 0 || hi <= 0 {
			return Variant{}, media.New(media.StageParse, "hls", "invalid RESOLUTION %q", res).WithLine(line)
		}
		v.Width, v.Height = wi, hi
	}
	return v, nil
}

func parseRenditionAttrs(value string, line int) (Rendition, *media.Error) {
	attrs := parseAttributes(value)
	r := Rendition{Line: line}
	r.Type = attrs["TYPE"]
	r.GroupID = attrs["GROUP-ID"]
	r.Name = attrs["NAME"]
	r.Language = attrs["LANGUAGE"]
	r.URI = attrs["URI"]
	r.Default = strings.EqualFold(attrs["DEFAULT"], "YES")
	r.Autoselect = strings.EqualFold(attrs["AUTOSELECT"], "YES")
	r.Forced = strings.EqualFold(attrs["FORCED"], "YES")
	if r.Type == "" || r.GroupID == "" {
		return Rendition{}, media.New(media.StageParse, "hls", "EXT-X-MEDIA requires TYPE and GROUP-ID").WithLine(line)
	}
	if r.URI != "" && !isLocalRef(r.URI) {
		return Rendition{}, media.New(media.StageParse, "hls", "remote URI %q is not supported", r.URI).WithLine(line)
	}
	return r, nil
}

func isUnsupportedTag(tag string) bool {
	switch tag {
	case "#EXT-X-I-FRAME-STREAM-INF", "#EXT-X-SESSION-DATA", "#EXT-X-SESSION-KEY",
		"#EXT-X-DEFINE", "#EXT-X-START", "#EXT-X-SKIP", "#EXT-X-PART", "#EXT-X-PRELOAD-HINT":
		return true
	}
	return false
}

// isLocalRef rejects absolute URLs and parent-directory escapes.
func isLocalRef(ref string) bool {
	if strings.Contains(ref, "://") || strings.HasPrefix(ref, "//") {
		return false
	}
	return true
}
