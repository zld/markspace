// Package manifest reads HLS master, media and I-frame playlists. Durations
// are parsed as exact rationals so that segment boundaries never accumulate
// floating point error; callers convert to integer ticks at a chosen
// timescale via clock.Rational.Ticks.
package manifest

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"liveregress/internal/clock"
	"liveregress/internal/fault"
)

// Variant is one EXT-X-STREAM-INF entry of a master playlist.
type Variant struct {
	Bandwidth        uint64
	AverageBandwidth uint64
	Codecs           []string
	Resolution       string
	AudioGroup       string
	FrameRate        string
	URI              string
	Path             string // slash-relative path inside the root
}

// Rendition is one EXT-X-MEDIA entry (audio or subtitle).
type Rendition struct {
	Type       string
	GroupID    string
	Name       string
	Language   string
	Default    bool
	Autoselect bool
	URI        string
	Path       string
}

// Master is a parsed master playlist with resolved child paths.
type Master struct {
	Path       string
	Variants   []Variant
	Renditions []Rendition
}

// ByteRange is a sub-range of a resource, as declared by EXT-X-BYTERANGE.
type ByteRange struct {
	Length int64
	Offset int64
}

// InitSegment is an EXT-X-MAP initialisation segment.
type InitSegment struct {
	URI       string
	Path      string
	ByteRange *ByteRange
}

// Segment is one media segment. Start is the exact presentation start
// measured from the playlist origin; durations and offsets stay rational until
// a caller picks a timescale.
type Segment struct {
	Sequence         int64
	Duration         clock.Rational
	URI              string
	Path             string
	ByteRange        *ByteRange
	Discontinuity    bool
	DiscontinuitySeq int64
	Start            clock.Rational
	End              clock.Rational
	Init             *InitSegment
	ProgramDateTime  string
}

// Playlist is a parsed media playlist, including I-frame only playlists.
type Playlist struct {
	Path                  string
	Version               int
	TargetDuration        clock.Rational
	MediaSequence         int64
	DiscontinuitySequence int64
	EndList               bool
	PlaylistType          string
	IFramesOnly           bool
	Segments              []Segment
}

// DeclaredDuration is the exact sum of all segment durations.
func (p *Playlist) DeclaredDuration() clock.Rational {
	var d clock.Rational
	for _, s := range p.Segments {
		d = d.Add(s.Duration)
	}
	return d
}

// Boundaries returns integer start ticks for every segment at timescale,
// followed by the end of the last segment. Conversions are one-shot from
// exact rationals, so no drift is introduced.
func (p *Playlist) Boundaries(timescale uint32) []clock.Time {
	out := make([]clock.Time, 0, len(p.Segments)+1)
	for _, s := range p.Segments {
		out = append(out, clock.Time{Ticks: s.Start.Ticks(timescale), Timescale: timescale})
	}
	out = append(out, clock.Time{Ticks: p.DeclaredDuration().Ticks(timescale), Timescale: timescale})
	return out
}

// Attributes parses an HLS attribute list, honouring quoted values that may
// contain commas.
func Attributes(s string) map[string]string {
	out := make(map[string]string)
	var b strings.Builder
	var key string
	inQuote := false
	flush := func(part string) {
		if key == "" {
			return
		}
		out[key] = part
		key = ""
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '"':
			inQuote = !inQuote
			b.WriteByte(c)
		case c == '=' && !inQuote && key == "":
			key = strings.TrimSpace(b.String())
			b.Reset()
		case c == ',' && !inQuote:
			flush(unquote(b.String()))
			b.Reset()
		default:
			b.WriteByte(c)
		}
	}
	flush(unquote(b.String()))
	return out
}

func unquote(s string) string {
	s = strings.TrimSpace(s)
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		return s[1 : len(s)-1]
	}
	return s
}

// lines yields trimmed non-blank lines, stripping a UTF-8 BOM on the first.
func lines(r io.Reader) ([]string, error) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	var out []string
	first := true
	for sc.Scan() {
		line := sc.Text()
		if first {
			line = strings.TrimPrefix(line, "\uFEFF")
			first = false
		}
		line = strings.TrimRight(line, " \t\r")
		if strings.TrimSpace(line) == "" {
			continue
		}
		out = append(out, line)
	}
	return out, sc.Err()
}

func readPlaylist(root, rel string) ([]string, string, error) {
	abs, clean, err := SecureJoin(root, rel)
	if err != nil {
		return nil, "", err
	}
	f, err := os.Open(abs)
	if err != nil {
		return nil, "", fault.Wrap(fault.KindPath, clean, -1, "open playlist", err)
	}
	defer f.Close()
	ls, err := lines(f)
	if err != nil {
		return nil, "", fault.Wrap(fault.KindFormat, clean, -1, "read playlist", err)
	}
	return ls, clean, nil
}

// LoadMaster parses a master playlist and validates every referenced URI
// against the input root. Child Paths are slash-relative to that root.
func LoadMaster(root, rel string) (*Master, error) {
	ls, clean, err := readPlaylist(root, rel)
	if err != nil {
		return nil, err
	}
	m := &Master{Path: clean}
	dir := Dir(clean)
	var pending *Variant
	for i, line := range ls {
		off := lineOffset(i)
		switch {
		case strings.HasPrefix(line, "#EXT-X-STREAM-INF:"):
			attrs := Attributes(strings.TrimPrefix(line, "#EXT-X-STREAM-INF:"))
			v := Variant{
				Codecs:     splitList(attrs["CODECS"]),
				Resolution: attrs["RESOLUTION"],
				AudioGroup: attrs["AUDIO"],
				FrameRate:  attrs["FRAME-RATE"],
			}
			if v.Bandwidth, err = parseUintAttr(attrs, "BANDWIDTH", clean, i); err != nil {
				return nil, err
			}
			v.AverageBandwidth, _ = strconv.ParseUint(attrs["AVERAGE-BANDWIDTH"], 10, 64)
			pending = &v
		case strings.HasPrefix(line, "#EXT-X-MEDIA:"):
			attrs := Attributes(strings.TrimPrefix(line, "#EXT-X-MEDIA:"))
			r := Rendition{
				Type:       attrs["TYPE"],
				GroupID:    attrs["GROUP-ID"],
				Name:       attrs["NAME"],
				Language:   attrs["LANGUAGE"],
				Default:    attrs["DEFAULT"] == "YES",
				Autoselect: attrs["AUTOSELECT"] == "YES",
				URI:        attrs["URI"],
			}
			if r.URI != "" {
				if r.Path, err = resolveChild(root, dir, r.URI, clean, off); err != nil {
					return nil, err
				}
			}
			m.Renditions = append(m.Renditions, r)
		case strings.HasPrefix(line, "#"):
			continue
		default:
			if pending == nil {
				return nil, fault.New(fault.KindFormat, clean, off, "variant URI without EXT-X-STREAM-INF")
			}
			pending.URI = line
			if pending.Path, err = resolveChild(root, dir, line, clean, off); err != nil {
				return nil, err
			}
			m.Variants = append(m.Variants, *pending)
			pending = nil
		}
	}
	if pending != nil {
		return nil, fault.New(fault.KindFormat, clean, lineOffset(len(ls)), "EXT-X-STREAM-INF without URI")
	}
	if len(m.Variants) == 0 {
		return nil, fault.New(fault.KindFormat, clean, -1, "master playlist has no variants")
	}
	return m, nil
}

// resolveChild resolves uri against the referencing playlist and validates it
// against the root. All child lookups fail here, before any media is opened.
func resolveChild(root, dir, uri, playlist string, at int64) (string, error) {
	rel := ResolveRelative(dir, uri)
	_, clean, err := SecureJoin(root, rel)
	if err != nil {
		if fe, ok := fault.As(err); ok {
			fe.Path = playlist
			fe.Offset = at
			fe.Detail = fmt.Sprintf("child URI %q: %s", uri, fe.Detail)
		}
		return "", err
	}
	return clean, nil
}

func parseUintAttr(a map[string]string, key, path string, line int) (uint64, error) {
	raw, ok := a[key]
	if !ok || raw == "" {
		return 0, fault.New(fault.KindFormat, path, lineOffset(line), "missing "+key)
	}
	n, err := strconv.ParseUint(raw, 10, 64)
	if err != nil {
		return 0, fault.Wrap(fault.KindFormat, path, lineOffset(line), "invalid "+key, err)
	}
	return n, nil
}

func splitList(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// lineOffset maps a zero-based logical line index to a synthetic byte offset
// used in faults. It is deterministic for a given playlist.
func lineOffset(i int) int64 { return int64(i) }
