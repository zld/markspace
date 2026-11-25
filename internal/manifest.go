package continuity

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// ManifestKind identifies the playlist dialect.
type ManifestKind string

const (
	ManifestHLS  ManifestKind = "hls"
	ManifestDASH ManifestKind = "dash"
)

// MediaKind is the coarse role of a rendition.
type MediaKind uint8

const (
	MediaUnknown MediaKind = iota
	MediaVideo
	MediaAudio
)

// String renders the media kind for diagnostics and output.
func (m MediaKind) String() string {
	switch m {
	case MediaVideo:
		return "video"
	case MediaAudio:
		return "audio"
	default:
		return "unknown"
	}
}

// MaxManifestBytes bounds any single manifest read.
const MaxManifestBytes = 4 << 20

// MaxSegmentsPerRepresentation bounds playlist expansion so a malicious or
// broken manifest cannot exhaust memory.
const MaxSegmentsPerRepresentation = 200000

// ByteRange is an inclusive-start, exclusive-end byte range inside a segment.
type ByteRange struct {
	Start int64
	End   int64
}

// Len returns the number of bytes covered.
func (b ByteRange) Len() int64 { return b.End - b.Start }

// SegmentRef is one addressable media segment, with its presentation time
// already converted to BaseTimescale and its URI validated as relative.
type SegmentRef struct {
	Number        int64
	URI           string
	InitURI       string
	Start         Ticks
	Duration      Ticks
	ByteRange     *ByteRange
	Discontinuity bool
	DiscSequence  int64
	Line          int
}

// Representation is one muxed track rendition described by a manifest.
type Representation struct {
	ID        string
	Kind      MediaKind
	Timescale uint32
	InitURI   string
	Codecs    string
	Width     int
	Height    int
	Bandwidth int64
	Segments  []SegmentRef
	Line      int
}

// Manifest is the parsed, validated playlist for one source.
type Manifest struct {
	Kind            ManifestKind
	Path            string
	TargetDuration  Ticks
	MediaSequence   int64
	Representations []Representation
}

// Segments returns every segment across all representations in stable order.
func (m *Manifest) Segments() []SegmentRef {
	var out []SegmentRef
	for i := range m.Representations {
		out = append(out, m.Representations[i].Segments...)
	}
	return out
}

// LoadSourceManifest loads every rendition playlist describing one source.
// HLS keeps one media playlist per rendition under label/<role>/index.m3u8;
// DASH keeps a single label/index.mpd. The returned Manifest is the union of
// all renditions, with deterministic ordering and every URI rewritten relative
// to root so segment reads are uniform.
func LoadSourceManifest(root, label string, kind ManifestKind) (*Manifest, error) {
	switch kind {
	case ManifestHLS:
		entries, err := os.ReadDir(filepath.Join(root, filepath.FromSlash(label)))
		if err != nil {
			return nil, WrapError(err, KindParse, Location{File: label, Offset: -1, Sample: -1}, "source directory not readable")
		}
		var dirs []string
		for _, e := range entries {
			if e.IsDir() {
				dirs = append(dirs, e.Name())
			}
		}
		sort.Strings(dirs)
		merged := &Manifest{Kind: ManifestHLS, Path: label}
		for _, role := range dirs {
			rel := label + "/" + role + "/index.m3u8"
			if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(rel))); err != nil {
				continue
			}
			m, err := LoadManifest(root, rel)
			if err != nil {
				return nil, err
			}
			merged.MediaSequence = m.MediaSequence
			if m.TargetDuration > merged.TargetDuration {
				merged.TargetDuration = m.TargetDuration
			}
			for i := range m.Representations {
				if m.Representations[i].Kind == MediaUnknown {
					m.Representations[i].Kind = KindForRole(role)
				}
				m.Representations[i].ID = label + "-" + role
				// Playlist URIs are relative to the rendition directory; rewrite
				// them to be relative to the source root for uniform resolution.
				rendDir := label + "/" + role
				m.Representations[i].InitURI = joinSlash(label, m.Representations[i].InitURI)
				for j := range m.Representations[i].Segments {
					seg := &m.Representations[i].Segments[j]
					seg.URI = joinSlash(rendDir, seg.URI)
					seg.InitURI = m.Representations[i].InitURI
				}
				merged.Representations = append(merged.Representations, m.Representations[i])
			}
		}
		if len(merged.Representations) == 0 {
			return nil, NewError(KindParse, Location{File: label, Offset: -1, Sample: -1}, "no HLS rendition playlists under %q", label)
		}
		return merged, nil
	case ManifestDASH:
		rel := label + "/index.mpd"
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(rel))); err != nil {
			return nil, WrapError(err, KindParse, Location{File: rel, Offset: -1, Sample: -1}, "manifest not found")
		}
		return LoadManifest(root, rel)
	default:
		return nil, Usagef("unsupported manifest format %q", kind)
	}
}

// KindForRole maps a rendition directory name to a media kind.
func KindForRole(role string) MediaKind {
	switch strings.ToLower(role) {
	case "video", "v":
		return MediaVideo
	case "audio", "a":
		return MediaAudio
	}
	return MediaUnknown
}

func joinSlash(a, b string) string {
	if b == "" {
		return ""
	}
	if strings.HasPrefix(b, a+"/") {
		return b
	}
	return a + "/" + b
}

// LoadManifest reads and parses a playlist from root/rel. The file extension
// selects HLS (.m3u8) or DASH (.mpd). Sizes beyond MaxManifestBytes are
// rejected at the file boundary.
func LoadManifest(root, rel string) (*Manifest, error) {
	if err := validateRelativeRef(rel); err != nil {
		return nil, NewError(KindPathEscape, Location{File: rel, Offset: -1, Sample: -1}, "manifest reference rejected: %v", err)
	}
	full := filepath.Join(root, filepath.FromSlash(rel))
	info, err := os.Stat(full)
	if err != nil {
		return nil, WrapError(err, KindParse, Location{File: rel, Offset: -1, Sample: -1}, "manifest not readable")
	}
	if info.Size() > MaxManifestBytes {
		return nil, NewError(KindParse, Location{File: rel, Offset: -1, Sample: -1}, "manifest is %d bytes, limit %d", info.Size(), MaxManifestBytes)
	}
	data, err := os.ReadFile(full)
	if err != nil {
		return nil, WrapError(err, KindParse, Location{File: rel, Offset: -1, Sample: -1}, "manifest not readable")
	}
	switch strings.ToLower(filepath.Ext(rel)) {
	case ".m3u8":
		return ParseHLS(rel, data)
	case ".mpd":
		return ParseDASH(rel, data)
	default:
		return nil, NewError(KindUnsupported, Location{File: rel, Offset: -1, Sample: -1}, "unsupported manifest extension %q", filepath.Ext(rel))
	}
}

// ParseHLS parses an HLS media playlist. Master playlists (EXT-X-STREAM-INF)
// are rejected: the engine consumes concrete media renditions only.
func ParseHLS(name string, data []byte) (*Manifest, error) {
	if len(data) > MaxManifestBytes {
		return nil, NewError(KindParse, atLine(name, 0), "manifest is %d bytes, limit %d", len(data), MaxManifestBytes)
	}
	data = bytes.TrimPrefix(data, []byte{0xEF, 0xBB, 0xBF})
	lines := strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n")
	m := &Manifest{Kind: ManifestHLS, Path: name}
	rep := Representation{ID: "hls", Timescale: BaseTimescale}
	var (
		haveHeader    bool
		pending       *hlsPending
		curSegment    Ticks
		initURI       string
		discSeq       int64
		discontinuity bool
		sawTagTarget  bool
		segIndex      int64
	)
	for i, raw := range lines {
		lineNo := i + 1
		line := strings.TrimSpace(strings.TrimSuffix(raw, "\r"))
		if line == "" {
			continue
		}
		switch {
		case !haveHeader:
			if line != "#EXTM3U" {
				return nil, NewError(KindParse, atLine(name, lineNo), "first line must be #EXTM3U")
			}
			haveHeader = true
			continue
		case strings.HasPrefix(line, "#EXT-X-STREAM-INF"), strings.HasPrefix(line, "#EXT-X-I-FRAME-STREAM-INF"):
			return nil, NewError(KindUnsupported, atLine(name, lineNo), "master playlist is not supported; supply a media playlist")
		case strings.HasPrefix(line, "#EXTINF:"):
			if pending != nil {
				return nil, NewError(KindParse, atLine(name, lineNo), "EXTINF without a preceding segment URI")
			}
			durText := strings.TrimPrefix(line, "#EXTINF:")
			if c := strings.IndexByte(durText, ','); c >= 0 {
				durText = durText[:c]
			}
			dur, err := ParseDecimalTicks(durText, BaseTimescale)
			if err != nil {
				return nil, NewError(KindParse, atLine(name, lineNo), "invalid EXTINF duration: %v", err)
			}
			pending = &hlsPending{duration: dur, line: lineNo, discontinuity: discontinuity, discSeq: discSeq}
			discontinuity = false
		case strings.HasPrefix(line, "#EXT-X-TARGETDURATION:"):
			v, err := strconv.ParseInt(strings.TrimSpace(strings.TrimPrefix(line, "#EXT-X-TARGETDURATION:")), 10, 64)
			if err != nil || v <= 0 {
				return nil, NewError(KindParse, atLine(name, lineNo), "invalid EXT-X-TARGETDURATION")
			}
			m.TargetDuration = Ticks(v) * Ticks(BaseTimescale)
			sawTagTarget = true
		case strings.HasPrefix(line, "#EXT-X-MEDIA-SEQUENCE:"):
			v, err := strconv.ParseInt(strings.TrimSpace(strings.TrimPrefix(line, "#EXT-X-MEDIA-SEQUENCE:")), 10, 64)
			if err != nil || v < 0 {
				return nil, NewError(KindParse, atLine(name, lineNo), "invalid EXT-X-MEDIA-SEQUENCE")
			}
			m.MediaSequence = v
			segIndex = v
		case strings.HasPrefix(line, "#EXT-X-DISCONTINUITY-SEQUENCE:"):
			v, err := strconv.ParseInt(strings.TrimSpace(strings.TrimPrefix(line, "#EXT-X-DISCONTINUITY-SEQUENCE:")), 10, 64)
			if err != nil || v < 0 {
				return nil, NewError(KindParse, atLine(name, lineNo), "invalid EXT-X-DISCONTINUITY-SEQUENCE")
			}
			discSeq = v
		case line == "#EXT-X-DISCONTINUITY":
			discontinuity = true
			discSeq++
		case strings.HasPrefix(line, "#EXT-X-MAP:"):
			attrs, err := parseHLSAttrs(strings.TrimPrefix(line, "#EXT-X-MAP:"))
			if err != nil {
				return nil, NewError(KindParse, atLine(name, lineNo), "invalid EXT-X-MAP: %v", err)
			}
			uri := attrs["URI"]
			if uri == "" {
				return nil, NewError(KindParse, atLine(name, lineNo), "EXT-X-MAP requires URI")
			}
			if err := validateRefAgainst(name, uri); err != nil {
				return nil, NewError(KindPathEscape, atLine(name, lineNo), "EXT-X-MAP URI rejected: %v", err)
			}
			if initURI != "" && initURI != uri {
				return nil, NewError(KindUnsupported, atLine(name, lineNo), "multiple distinct EXT-X-MAP URIs per playlist are not supported")
			}
			initURI = uri
		case strings.HasPrefix(line, "#EXT-X-BYTERANGE:"):
			if pending == nil {
				return nil, NewError(KindParse, atLine(name, lineNo), "EXT-X-BYTERANGE without EXTINF")
			}
			br, err := parseByteRange(strings.TrimPrefix(line, "#EXT-X-BYTERANGE:"), rep.Segments)
			if err != nil {
				return nil, NewError(KindParse, atLine(name, lineNo), "invalid EXT-X-BYTERANGE: %v", err)
			}
			pending.byteRange = br
		case strings.HasPrefix(line, "#EXT-X-ENDLIST"), strings.HasPrefix(line, "#EXT-X-PLAYLIST-TYPE:"),
			strings.HasPrefix(line, "#EXT-X-VERSION:"), strings.HasPrefix(line, "#EXT-X-INDEPENDENT-SEGMENTS"),
			strings.HasPrefix(line, "#EXT-X-ALLOW-CACHE:"):
			// Recognised, no effect on this engine.
		case strings.HasPrefix(line, "#"):
			// Unknown tags are ignored per HLS extensibility rules.
		default:
			if pending == nil {
				return nil, NewError(KindParse, atLine(name, lineNo), "segment URI without EXTINF")
			}
			if err := validateRefAgainst(name, line); err != nil {
				return nil, NewError(KindPathEscape, atLine(name, lineNo), "segment URI rejected: %v", err)
			}
			if len(rep.Segments) >= MaxSegmentsPerRepresentation {
				return nil, NewError(KindParse, atLine(name, lineNo), "segment count exceeds limit %d", MaxSegmentsPerRepresentation)
			}
			ref := SegmentRef{
				Number:        segIndex,
				URI:           line,
				InitURI:       initURI,
				Start:         curSegment,
				Duration:      pending.duration,
				ByteRange:     pending.byteRange,
				Discontinuity: pending.discontinuity,
				DiscSequence:  pending.discSeq,
				Line:          lineNo,
			}
			rep.Segments = append(rep.Segments, ref)
			rep.InitURI = initURI
			curSegment += pending.duration
			segIndex++
			pending = nil
		}
	}
	if !haveHeader {
		return nil, NewError(KindParse, atLine(name, 1), "empty playlist, expected #EXTM3U")
	}
	if !sawTagTarget {
		return nil, NewError(KindParse, atLine(name, len(lines)), "missing EXT-X-TARGETDURATION")
	}
	if pending != nil {
		return nil, NewError(KindParse, atLine(name, pending.line), "EXTINF without a segment URI")
	}
	if len(rep.Segments) == 0 {
		return nil, NewError(KindParse, atLine(name, len(lines)), "playlist contains no media segments")
	}
	m.Representations = []Representation{rep}
	return m, nil
}

type hlsPending struct {
	duration      Ticks
	line          int
	byteRange     *ByteRange
	discontinuity bool
	discSeq       int64
}

// parseHLSAttrs parses a quoted HLS attribute list (KEY=VALUE,...).
func parseHLSAttrs(s string) (map[string]string, error) {
	out := map[string]string{}
	for i := 0; i < len(s); {
		for i < len(s) && (s[i] == ' ' || s[i] == '\t' || s[i] == ',') {
			i++
		}
		if i == len(s) {
			break
		}
		eq := strings.IndexByte(s[i:], '=')
		if eq < 0 {
			return nil, fmt.Errorf("attribute %q has no value", s[i:])
		}
		key := strings.TrimSpace(s[i : i+eq])
		if key == "" {
			return nil, errors.New("empty attribute name")
		}
		i += eq + 1
		if i >= len(s) || s[i] != '"' {
			return nil, fmt.Errorf("attribute %s must be a quoted string", key)
		}
		i++
		end := strings.IndexByte(s[i:], '"')
		if end < 0 {
			return nil, fmt.Errorf("unterminated quoted attribute %s", key)
		}
		val := s[i : i+end]
		i += end + 1
		if _, dup := out[key]; dup {
			return nil, fmt.Errorf("duplicate attribute %s", key)
		}
		out[key] = val
		if i < len(s) && s[i] != ',' {
			return nil, fmt.Errorf("expected ',' after attribute %s", key)
		}
	}
	return out, nil
}

// parseByteRange parses "length[@offset]". A missing offset continues from the
// end of the previous byte range.
func parseByteRange(s string, prior []SegmentRef) (*ByteRange, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, errors.New("empty byte range")
	}
	parts := strings.SplitN(s, "@", 2)
	length, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || length <= 0 {
		return nil, fmt.Errorf("invalid length %q", parts[0])
	}
	var start int64
	if len(parts) == 2 {
		start, err = strconv.ParseInt(parts[1], 10, 64)
		if err != nil || start < 0 {
			return nil, fmt.Errorf("invalid offset %q", parts[1])
		}
	} else if len(prior) > 0 && prior[len(prior)-1].ByteRange != nil {
		start = prior[len(prior)-1].ByteRange.End
	}
	end := start + length
	if end < start {
		return nil, errors.New("byte range overflows int64")
	}
	return &ByteRange{Start: start, End: end}, nil
}

// validateRelativeRef rejects references that can escape the fixture root or
// point outside the local filesystem. It is deliberately strict and permits
// no parent traversal; use validateRefAgainst when the base directory is known.
func validateRelativeRef(ref string) error {
	return validateRefAgainst("", ref)
}

// validateRefAgainst validates ref as if resolved from baseFile, allowing ".."
// components that stay within baseFile's directory chain.
func validateRefAgainst(baseFile, ref string) error {
	if err := validateRefSyntax(ref); err != nil && !errors.Is(err, errParentComponent) {
		return err
	}
	depth := 0
	if dir := filepath.ToSlash(filepath.Dir(baseFile)); baseFile != "" && dir != "." && dir != "" {
		depth = strings.Count(dir, "/") + 1
	}
	for _, part := range strings.Split(ref, "/") {
		switch part {
		case "", ".":
			return errors.New("empty or '.' path component")
		case "..":
			depth--
			if depth < 0 {
				return errors.New("path escapes the manifest directory")
			}
		default:
			depth++
		}
	}
	return nil
}

var errParentComponent = errors.New("parent component")

// validateRefSyntax checks a reference without resolving parent components.
func validateRefSyntax(ref string) error {
	if ref == "" {
		return errors.New("empty reference")
	}
	if len(ref) > 2048 {
		return errors.New("reference longer than 2048 bytes")
	}
	for _, r := range ref {
		if r < 0x20 || r == 0x7f {
			return fmt.Errorf("control character %U in reference", r)
		}
	}
	if strings.Contains(ref, "\\") {
		return errors.New("backslash separators are not allowed")
	}
	if strings.ContainsAny(ref, "?#") {
		return errors.New("query and fragment components are not allowed")
	}
	if strings.HasPrefix(ref, "/") {
		return errors.New("absolute paths are not allowed")
	}
	if i := strings.IndexByte(ref, ':'); i >= 0 && i < strings.IndexByte(ref+"/", '/') {
		return errors.New("URI schemes and drive letters are not allowed")
	}
	for _, part := range strings.Split(ref, "/") {
		if part == "" {
			return errors.New("empty path component")
		}
		if part == "." {
			return fmt.Errorf("path component %q", part)
		}
		if part == ".." {
			return fmt.Errorf("path traversal component %q: %w", part, errParentComponent)
		}
		decoded, err := percentDecode(part)
		if err != nil {
			return err
		}
		low := strings.ToLower(decoded)
		if low == "." || low == ".." {
			return fmt.Errorf("encoded path traversal in %q", part)
		}
		if strings.ContainsAny(decoded, "/\\") {
			return fmt.Errorf("encoded separator in %q", part)
		}
	}
	return nil
}

func percentDecode(s string) (string, error) {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] != '%' {
			b.WriteByte(s[i])
			continue
		}
		if i+2 >= len(s) {
			return "", fmt.Errorf("truncated percent escape in %q", s)
		}
		v, err := strconv.ParseUint(s[i+1:i+3], 16, 8)
		if err != nil {
			return "", fmt.Errorf("invalid percent escape in %q", s)
		}
		b.WriteByte(byte(v))
		i += 2
	}
	return b.String(), nil
}

// ResolveRef resolves manifest-relative ref against baseFile inside root and
// returns the cleaned slash path relative to root. References that resolve
// outside root are rejected.
func ResolveRef(root, baseFile, ref string) (string, error) {
	if err := validateRefAgainst(baseFile, ref); err != nil {
		return "", err
	}
	joined := filepath.Join(root, filepath.FromSlash(filepath.Dir(baseFile)), filepath.FromSlash(ref))
	rel, err := filepath.Rel(root, joined)
	if err != nil {
		return "", err
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return "", fmt.Errorf("reference %q escapes root", ref)
	}
	return filepath.ToSlash(rel), nil
}
