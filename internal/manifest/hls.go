package manifest

import (
	"bufio"
	"bytes"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"

	"continuity/internal/media"
)

// ParseHLSBundle parses a media playlist or an HLS master playlist and all
// local media playlists it references.
func ParseHLSBundle(entry, root string) (*Bundle, error) {
	return parseHLSBundle(entry, root, refTracker{}, DefaultLimits())
}

func parseHLSBundle(entry, root string, refs refTracker, limits Limits) (*Bundle, error) {
	data, err := readLimited(entry, limits)
	if err != nil {
		return nil, err
	}
	bundle := &Bundle{Entry: entry, Root: root}
	if bytes.Contains(data, []byte("#EXT-X-STREAM-INF")) || bytes.Contains(data, []byte("#EXT-X-MEDIA:")) {
		plays, perr := parseMaster(entry, root, data)
		if perr != nil {
			return nil, perr
		}
		for _, p := range plays {
			m, err := parseHLSPlaylist(p.Path, root, p.Kind, refs, limits)
			if err != nil {
				return nil, err
			}
			bundle.Manifests = append(bundle.Manifests, m)
			bundle.Segments = append(bundle.Segments, m.Segments...)
		}
		return bundle, nil
	}
	kind := inferKind(entry)
	m, err := parseHLSPlaylist(entry, root, kind, refs, limits)
	if err != nil {
		return nil, err
	}
	bundle.Manifests = []*Manifest{m}
	bundle.Segments = m.Segments
	return bundle, nil
}

type playlistRef struct{ Path, Kind string }

func parseMaster(entry, root string, data []byte) ([]playlistRef, error) {
	var out []playlistRef
	nextStream := false
	sc := bufio.NewScanner(bytes.NewReader(data))
	lineNo := 0
	for sc.Scan() {
		lineNo++
		line := strings.TrimSpace(sc.Text())
		switch {
		case strings.HasPrefix(line, "#EXT-X-MEDIA:"):
			uri := attrValue(line, "URI")
			if uri == "" {
				continue
			}
			kind := strings.ToLower(attrValue(line, "TYPE"))
			if kind == "" {
				kind = inferKind(uri)
			}
			p, _, err := SafeResolve(root, entry, uri)
			if err != nil {
				return nil, withLine(err, entry, lineNo)
			}
			out = append(out, playlistRef{p, kind})
		case strings.HasPrefix(line, "#EXT-X-STREAM-INF"):
			nextStream = true
		case line != "" && !strings.HasPrefix(line, "#"):
			if nextStream {
				p, _, err := SafeResolve(root, entry, line)
				if err != nil {
					return nil, withLine(err, entry, lineNo)
				}
				out = append(out, playlistRef{p, "video"})
				nextStream = false
			}
		}
	}
	if err := sc.Err(); err != nil {
		return nil, &media.Fault{Phase: media.PhaseManifest, Code: media.CodeInvalidManifest, Source: entry, Err: err}
	}
	if len(out) == 0 {
		return nil, &media.Fault{Phase: media.PhaseManifest, Code: media.CodeInvalidManifest, Source: entry,
			Line: lineNo, Detail: "master playlist contains no local rendition"}
	}
	return out, nil
}

// ParseHLSPlaylist parses one media playlist.
func ParseHLSPlaylist(path, root, kind string) (*Manifest, error) {
	return parseHLSPlaylist(path, root, kind, refTracker{}, DefaultLimits())
}

func parseHLSPlaylist(path, root, kind string, refs refTracker, limits Limits) (*Manifest, error) {
	data, err := readLimited(path, limits)
	if err != nil {
		return nil, err
	}
	m := &Manifest{Format: "hls", Version: 3, Source: path, Root: root, Static: true}
	var durationNS int64 = -1
	var discontinuity bool
	var initURI string
	segmentIndex := 0
	offset := 0
	for lineNo, raw := range strings.Split(string(data), "\n") {
		lineNo++
		line := strings.TrimSpace(raw)
		if line == "" {
			offset += len(raw) + 1
			continue
		}
		switch {
		case strings.HasPrefix(line, "#EXT-X-MEDIA-SEQUENCE:"):
			v, err := strconv.ParseUint(strings.TrimPrefix(line, "#EXT-X-MEDIA-SEQUENCE:"), 10, 64)
			if err != nil {
				return nil, lineFault(path, lineNo, offset, "bad media sequence", err)
			}
			m.MediaSequence = v
		case strings.HasPrefix(line, "#EXTINF:"):
			body := strings.TrimPrefix(line, "#EXTINF:")
			if i := strings.Index(body, ","); i >= 0 {
				body = body[:i]
			}
			d, err := media.ParseSecondsToNS(body)
			if err != nil {
				return nil, lineFault(path, lineNo, offset, "invalid EXTINF", err)
			}
			durationNS = d
		case strings.HasPrefix(line, "#EXT-X-DISCONTINUITY"):
			discontinuity = true
		case strings.HasPrefix(line, "#EXT-X-MAP:"):
			initURI = attrValue(line, "URI")
			if initURI != "" {
				if _, _, err := SafeResolve(root, path, initURI); err != nil {
					return nil, withLine(err, path, lineNo)
				}
			}
		case strings.HasPrefix(line, "#"):
			// Versions and other tags do not affect normalization.
		default:
			if durationNS <= 0 {
				return nil, lineFault(path, lineNo, offset, "segment without positive EXTINF", nil)
			}
			abs, rel, err := SafeResolve(root, path, line)
			if err != nil {
				return nil, withLine(err, path, lineNo)
			}
			if err := refs.check(abs, path, lineNo); err != nil {
				return nil, err
			}
			if segmentIndex >= limits.MaxSegments {
				return nil, &media.Fault{Phase: media.PhaseManifest, Code: media.CodeSizeLimit, Source: path, Line: lineNo, Detail: "too many segments"}
			}
			seq := m.MediaSequence + uint64(segmentIndex)
			seg := Segment{Index: segmentIndex, Track: kind, Kind: kind, Sequence: seq,
				URI: line, RelPath: rel, AbsPath: abs, StartNS: segmentStart(m.Segments),
				DurationNS: durationNS, Discontinuity: discontinuity, Source: path, Line: lineNo}
			if initURI != "" {
				if ip, ir, err := SafeResolve(root, path, initURI); err == nil {
					seg.InitAbsPath, seg.InitRelPath = ip, ir
				}
			}
			m.Segments = append(m.Segments, seg)
			segmentIndex++
			durationNS = -1
			discontinuity = false
		}
		offset += len(raw) + 1
	}
	if len(m.Segments) == 0 {
		return nil, &media.Fault{Phase: media.PhaseManifest, Code: media.CodeInvalidManifest, Source: path, Detail: "playlist has no segments"}
	}
	return m, nil
}

func segmentStart(segs []Segment) int64 {
	var total int64
	for _, s := range segs {
		total += s.DurationNS
	}
	return total
}

func inferKind(p string) string {
	n := strings.ToLower(filepath.Base(p))
	if strings.Contains(n, "audio") || strings.Contains(n, "aac") {
		return "audio"
	}
	return "video"
}

func attrValue(tag, key string) string {
	needle := key + "="
	i := strings.Index(tag, needle)
	if i < 0 {
		return ""
	}
	rest := tag[i+len(needle):]
	if strings.HasPrefix(rest, "\"") {
		end := strings.Index(rest[1:], "\"")
		if end < 0 {
			return ""
		}
		return rest[1 : 1+end]
	}
	end := strings.IndexAny(rest, ",")
	if end >= 0 {
		return rest[:end]
	}
	return strings.TrimSpace(rest)
}

func lineFault(path string, line, offset int, detail string, err error) error {
	return &media.Fault{Phase: media.PhaseManifest, Code: media.CodeIllegalDuration,
		Source: path, Line: line, Offset: int64(offset), Detail: detail, Err: err}
}

func withLine(err error, path string, line int) error {
	f, ok := media.AsFault(err)
	if !ok {
		return fmt.Errorf("%s:%d: %w", path, line, err)
	}
	f.Source = path
	f.Line = line
	return f
}
