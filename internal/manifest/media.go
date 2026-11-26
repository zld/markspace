package manifest

import (
	"fmt"
	"strconv"
	"strings"

	"liveregress/internal/clock"
	"liveregress/internal/fault"
)

// LoadMedia parses a media or I-frame playlist (EXT-X-I-FRAMES-ONLY).
// Durations are kept exact; segment Start/End are computed by summing
// rationals, so the declared timeline is reproducible bit-for-bit.
func LoadMedia(root, rel string) (*Playlist, error) {
	ls, clean, err := readPlaylist(root, rel)
	if err != nil {
		return nil, err
	}
	if isMasterLines(ls) {
		return nil, fault.New(fault.KindFormat, clean, -1, "expected media playlist, found master")
	}
	p := &Playlist{Path: clean, Version: -1}
	dir := Dir(clean)
	var pendingDur clock.Rational
	havePendingDur := false
	var pending *pendingRange
	var pendingDiscontinuity bool
	var pendingPdt string
	var curInit *InitSegment
	var cursor clock.Rational
	discontinuityCount := int64(0)
	var lastPath string
	var lastRangeEnd int64

	for i, line := range ls {
		off := lineOffset(i)
		switch {
		case strings.HasPrefix(line, "#EXT-X-VERSION:"):
			v, err := strconv.Atoi(strings.TrimPrefix(line, "#EXT-X-VERSION:"))
			if err != nil {
				return nil, fault.Wrap(fault.KindFormat, clean, off, "invalid EXT-X-VERSION", err)
			}
			p.Version = v
		case strings.HasPrefix(line, "#EXT-X-TARGETDURATION:"):
			r, err := clock.ParseDecimal(strings.TrimPrefix(line, "#EXT-X-TARGETDURATION:"))
			if err != nil {
				return nil, fault.Wrap(fault.KindFormat, clean, off, "invalid EXT-X-TARGETDURATION", err)
			}
			p.TargetDuration = r
		case strings.HasPrefix(line, "#EXT-X-MEDIA-SEQUENCE:"):
			n, err := strconv.ParseInt(strings.TrimPrefix(line, "#EXT-X-MEDIA-SEQUENCE:"), 10, 64)
			if err != nil {
				return nil, fault.Wrap(fault.KindFormat, clean, off, "invalid EXT-X-MEDIA-SEQUENCE", err)
			}
			p.MediaSequence = n
		case strings.HasPrefix(line, "#EXT-X-DISCONTINUITY-SEQUENCE:"):
			n, err := strconv.ParseInt(strings.TrimPrefix(line, "#EXT-X-DISCONTINUITY-SEQUENCE:"), 10, 64)
			if err != nil {
				return nil, fault.Wrap(fault.KindFormat, clean, off, "invalid EXT-X-DISCONTINUITY-SEQUENCE", err)
			}
			p.DiscontinuitySequence = n
			discontinuityCount = n
		case line == "#EXT-X-DISCONTINUITY":
			pendingDiscontinuity = true
		case line == "#EXT-X-ENDLIST":
			p.EndList = true
		case line == "#EXT-X-I-FRAMES-ONLY":
			p.IFramesOnly = true
		case strings.HasPrefix(line, "#EXT-X-PLAYLIST-TYPE:"):
			p.PlaylistType = strings.TrimPrefix(line, "#EXT-X-PLAYLIST-TYPE:")
		case strings.HasPrefix(line, "#EXT-X-PROGRAM-DATE-TIME:"):
			pendingPdt = strings.TrimPrefix(line, "#EXT-X-PROGRAM-DATE-TIME:")
		case strings.HasPrefix(line, "#EXT-X-BYTERANGE:"):
			pr, err := parsePendingRange(strings.TrimPrefix(line, "#EXT-X-BYTERANGE:"), clean, off)
			if err != nil {
				return nil, err
			}
			pending = pr
		case strings.HasPrefix(line, "#EXT-X-MAP:"):
			attrs := Attributes(strings.TrimPrefix(line, "#EXT-X-MAP:"))
			m, err := parseMap(root, dir, attrs, clean, off)
			if err != nil {
				return nil, err
			}
			curInit = m
		case strings.HasPrefix(line, "#EXTINF:"):
			raw := strings.TrimPrefix(line, "#EXTINF:")
			if comma := strings.Index(raw, ","); comma >= 0 {
				raw = raw[:comma]
			}
			d, err := clock.ParseDecimal(raw)
			if err != nil {
				return nil, fault.Wrap(fault.KindFormat, clean, off, "invalid EXTINF duration", err)
			}
			if d.Compare(clock.Rational{Num: 0, Den: 1}) < 0 {
				return nil, fault.New(fault.KindFormat, clean, off, "negative EXTINF duration")
			}
			pendingDur = d
			havePendingDur = true
		case strings.HasPrefix(line, "#"):
			continue
		default:
			if !havePendingDur {
				return nil, fault.New(fault.KindFormat, clean, off, "segment URI without EXTINF")
			}
			seg := Segment{
				Sequence:        p.MediaSequence + int64(len(p.Segments)),
				Duration:        pendingDur,
				URI:             line,
				Discontinuity:   pendingDiscontinuity,
				Start:           cursor,
				End:             cursor.Add(pendingDur),
				Init:            curInit,
				ProgramDateTime: pendingPdt,
			}
			if seg.Path, err = resolveChild(root, dir, line, clean, off); err != nil {
				return nil, err
			}
			if pending != nil {
				br, err := pending.materialize(seg.Path, lastPath, lastRangeEnd)
				if err != nil {
					return nil, err
				}
				abs, _, err := SecureJoin(root, seg.Path)
				if err != nil {
					return nil, err
				}
				if err := validateRange(abs, seg.Path, &br); err != nil {
					return nil, err
				}
				seg.ByteRange = &br
				lastRangeEnd = br.Offset + br.Length
			} else {
				lastRangeEnd = -1
			}
			if pendingDiscontinuity {
				discontinuityCount++
				pendingDiscontinuity = false
			}
			seg.DiscontinuitySeq = discontinuityCount
			cursor = seg.End
			p.Segments = append(p.Segments, seg)
			lastPath = seg.Path
			havePendingDur = false
			pending = nil
			pendingPdt = ""
		}
	}
	if havePendingDur {
		return nil, fault.New(fault.KindFormat, clean, lineOffset(len(ls)), "EXTINF without segment URI")
	}
	if len(p.Segments) == 0 {
		return nil, fault.New(fault.KindFormat, clean, -1, "playlist has no segments")
	}
	for i := 1; i < len(p.Segments); i++ {
		a, b := p.Segments[i-1], p.Segments[i]
		if a.ByteRange != nil && b.ByteRange != nil && a.Path == b.Path {
			if b.ByteRange.Offset < a.ByteRange.Offset+a.ByteRange.Length {
				return nil, fault.New(fault.KindCorrupt, b.Path, b.ByteRange.Offset,
					fmt.Sprintf("byte ranges overlap: previous ends at %d", a.ByteRange.Offset+a.ByteRange.Length))
			}
		}
	}
	return p, nil
}

// pendingRange is a parsed EXT-X-BYTERANGE that may not carry an offset yet.
type pendingRange struct {
	length    int64
	offset    int64
	hasOffset bool
}

func (r *pendingRange) materialize(path, prevPath string, prevEnd int64) (ByteRange, error) {
	if r.hasOffset {
		return ByteRange{Length: r.length, Offset: r.offset}, nil
	}
	if prevPath == "" || prevPath != path || prevEnd < 0 {
		return ByteRange{}, fault.New(fault.KindFormat, path, -1,
			"EXT-X-BYTERANGE without offset must follow a range in the same resource")
	}
	return ByteRange{Length: r.length, Offset: prevEnd}, nil
}

func parsePendingRange(raw, path string, off int64) (*pendingRange, error) {
	parts := strings.SplitN(strings.TrimSpace(raw), "@", 2)
	length, err := strconv.ParseInt(strings.TrimSpace(parts[0]), 10, 64)
	if err != nil || length <= 0 {
		return nil, fault.New(fault.KindFormat, path, off, "invalid EXT-X-BYTERANGE length")
	}
	pr := &pendingRange{length: length}
	if len(parts) == 2 {
		o, err := strconv.ParseInt(strings.TrimSpace(parts[1]), 10, 64)
		if err != nil || o < 0 {
			return nil, fault.New(fault.KindFormat, path, off, "invalid EXT-X-BYTERANGE offset")
		}
		pr.offset, pr.hasOffset = o, true
	}
	return pr, nil
}

func isMasterLines(ls []string) bool {
	for _, l := range ls {
		if strings.HasPrefix(l, "#EXT-X-STREAM-INF:") {
			return true
		}
	}
	return false
}

func parseMap(root, dir string, attrs map[string]string, path string, off int64) (*InitSegment, error) {
	uri := attrs["URI"]
	if uri == "" {
		return nil, fault.New(fault.KindFormat, path, off, "EXT-X-MAP without URI")
	}
	child, err := resolveChild(root, dir, uri, path, off)
	if err != nil {
		return nil, err
	}
	m := &InitSegment{URI: uri, Path: child}
	if br := attrs["BYTERANGE"]; br != "" {
		pr, err := parsePendingRange(br, path, off)
		if err != nil {
			return nil, err
		}
		if !pr.hasOffset {
			return nil, fault.New(fault.KindFormat, path, off, "EXT-X-MAP BYTERANGE requires an offset")
		}
		r := ByteRange{Length: pr.length, Offset: pr.offset}
		abs, _, err := SecureJoin(root, child)
		if err != nil {
			return nil, err
		}
		if err := validateRange(abs, child, &r); err != nil {
			return nil, err
		}
		m.ByteRange = &r
	}
	return m, nil
}
