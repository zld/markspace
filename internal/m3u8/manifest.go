// Package m3u8 is a self-contained HLS playlist reader and writer covering
// the master and media playlist tags this project produces and validates.
package m3u8

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Tag names handled by the reader/writer.
const (
	TagM3U          = "#EXTM3U"
	TagVersion      = "#EXT-X-VERSION"
	TagTargetDur    = "#EXT-X-TARGETDURATION"
	TagMediaSeq     = "#EXT-X-MEDIA-SEQUENCE"
	TagPlaylistType = "#EXT-X-PLAYLIST-TYPE"
	TagInf          = "#EXTINF"
	TagStreamInf    = "#EXT-X-STREAM-INF"
	TagEndlist      = "#EXT-X-ENDLIST"
	TagAllowCache   = "#EXT-X-ALLOW-CACHE"
)

// Segment is one media playlist entry.
type Segment struct {
	URI      string
	Duration time.Duration
	// Sequence is the media sequence number assigned to this segment.
	Sequence uint64
	// ByteSize and SHA256 are optional verification metadata.
	ByteSize int64
	SHA256   string
}

// MediaPlaylist models an HLS media (variant) playlist.
type MediaPlaylist struct {
	Version        int
	TargetDuration time.Duration
	MediaSequence  uint64
	PlaylistType   string
	Segments       []Segment
	Endlist        bool
}

// Variant is one master playlist rendition.
type Variant struct {
	URI       string
	Bandwidth int
	// AverageBandwidth, Codecs and Resolution are optional attributes.
	AverageBandwidth int
	Codecs           string
	Resolution       string
}

// MasterPlaylist models an HLS master playlist.
type MasterPlaylist struct {
	Version  int
	Variants []Variant
}

// DurationMS returns the total segment duration in whole milliseconds.
func (m MediaPlaylist) DurationMS() int64 {
	var total time.Duration
	for _, s := range m.Segments {
		total += s.Duration
	}
	return total.Milliseconds()
}

// WriteMedia serialises a media playlist in a stable, canonical layout.
func WriteMedia(w io.Writer, m MediaPlaylist) error {
	bw := bufio.NewWriter(w)
	fmt.Fprintln(bw, TagM3U)
	fmt.Fprintf(bw, "%s:%d\n", TagVersion, orDefault(m.Version, 3))
	targetSecs := int(m.TargetDuration.Round(time.Second).Seconds())
	if targetSecs <= 0 {
		targetSecs = maxSegmentCeil(m)
	}
	fmt.Fprintf(bw, "%s:%d\n", TagTargetDur, targetSecs)
	fmt.Fprintf(bw, "%s:%d\n", TagMediaSeq, m.MediaSequence)
	if m.PlaylistType != "" {
		fmt.Fprintf(bw, "%s:%s\n", TagPlaylistType, m.PlaylistType)
	}
	fmt.Fprintf(bw, "%s:NO\n", TagAllowCache)
	for _, s := range m.Segments {
		fmt.Fprintf(bw, "%s:%.3f,\n", TagInf, s.Duration.Seconds())
		fmt.Fprintln(bw, s.URI)
	}
	if m.Endlist {
		fmt.Fprintln(bw, TagEndlist)
	}
	return bw.Flush()
}

func orDefault(v, d int) int {
	if v > 0 {
		return v
	}
	return d
}

func maxSegmentCeil(m MediaPlaylist) int {
	max := 1
	for _, s := range m.Segments {
		if c := int(s.Duration.Round(time.Second).Seconds()); c > max {
			max = c
		}
	}
	return max
}

// WriteMaster serialises a master playlist; variants are sorted by bandwidth
// so output is deterministic.
func WriteMaster(w io.Writer, m MasterPlaylist) error {
	variants := append([]Variant(nil), m.Variants...)
	sort.SliceStable(variants, func(i, j int) bool {
		return variants[i].Bandwidth < variants[j].Bandwidth
	})
	bw := bufio.NewWriter(w)
	fmt.Fprintln(bw, TagM3U)
	if m.Version > 0 {
		fmt.Fprintf(bw, "%s:%d\n", TagVersion, m.Version)
	}
	for _, v := range variants {
		attrs := []string{fmt.Sprintf("BANDWIDTH=%d", v.Bandwidth)}
		if v.AverageBandwidth > 0 {
			attrs = append(attrs, fmt.Sprintf("AVERAGE-BANDWIDTH=%d",
				v.AverageBandwidth))
		}
		if v.Codecs != "" {
			attrs = append(attrs, fmt.Sprintf("CODECS=%q", v.Codecs))
		}
		if v.Resolution != "" {
			attrs = append(attrs, "RESOLUTION="+v.Resolution)
		}
		fmt.Fprintf(bw, "%s:%s\n", TagStreamInf, strings.Join(attrs, ","))
		fmt.Fprintln(bw, v.URI)
	}
	return bw.Flush()
}

// ParseMedia reads a media playlist. Unknown tags are ignored without error
// so forward-compatible playlists still parse; malformed known tags fail.
func ParseMedia(r io.Reader) (MediaPlaylist, error) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	var m MediaPlaylist
	var sawHeader bool
	var pending *Segment
	lineNo := 0
	for sc.Scan() {
		lineNo++
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		if line == TagM3U {
			sawHeader = true
			continue
		}
		if !strings.HasPrefix(line, "#") {
			if pending == nil {
				return m, fmt.Errorf("m3u8: URI without %s at line %d",
					TagInf, lineNo)
			}
			pending.URI = line
			m.Segments = append(m.Segments, *pending)
			pending = nil
			continue
		}
		tag, val, _ := strings.Cut(line, ":")
		switch tag {
		case TagVersion:
			n, err := atoi(tag, val)
			if err != nil {
				return m, err
			}
			m.Version = n
		case TagTargetDur:
			n, err := atoi(tag, val)
			if err != nil {
				return m, err
			}
			m.TargetDuration = time.Duration(n) * time.Second
		case TagMediaSeq:
			n, err := atoi(tag, val)
			if err != nil {
				return m, err
			}
			m.MediaSequence = uint64(n)
		case TagPlaylistType:
			m.PlaylistType = val
		case TagEndlist:
			m.Endlist = true
		case TagInf:
			secs, _, _ := strings.Cut(val, ",")
			f, err := strconv.ParseFloat(strings.TrimSpace(secs), 64)
			if err != nil {
				return m, fmt.Errorf("m3u8: bad %s value %q", TagInf, val)
			}
			pending = &Segment{Duration: secondsDuration(f)}
		}
	}
	if err := sc.Err(); err != nil {
		return m, err
	}
	if !sawHeader {
		return m, errors.New("m3u8: missing #EXTM3U header")
	}
	if pending != nil {
		return m, errors.New("m3u8: dangling EXTINF without URI")
	}
	assignSequences(&m)
	return m, nil
}

func secondsDuration(secs float64) time.Duration {
	return time.Duration(secs * float64(time.Second))
}

func assignSequences(m *MediaPlaylist) {
	for i := range m.Segments {
		m.Segments[i].Sequence = m.MediaSequence + uint64(i)
	}
}

// ParseMaster reads a master playlist into structured variants.
func ParseMaster(r io.Reader) (MasterPlaylist, error) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	var m MasterPlaylist
	var sawHeader bool
	var pending *Variant
	lineNo := 0
	for sc.Scan() {
		lineNo++
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		if line == TagM3U {
			sawHeader = true
			continue
		}
		if !strings.HasPrefix(line, "#") {
			if pending == nil {
				continue // tolerate stray URIs
			}
			pending.URI = line
			m.Variants = append(m.Variants, *pending)
			pending = nil
			continue
		}
		tag, val, _ := strings.Cut(line, ":")
		switch tag {
		case TagVersion:
			n, err := atoi(tag, val)
			if err != nil {
				return m, err
			}
			m.Version = n
		case TagStreamInf:
			attrs, err := parseAttributes(val)
			if err != nil {
				return m, fmt.Errorf("m3u8: line %d: %w", lineNo, err)
			}
			bw, ok := attrs["BANDWIDTH"]
			if !ok {
				return m, fmt.Errorf("m3u8: %s missing BANDWIDTH",
					TagStreamInf)
			}
			n, err := strconv.Atoi(bw)
			if err != nil {
				return m, fmt.Errorf("m3u8: bad BANDWIDTH %q", bw)
			}
			v := Variant{Bandwidth: n, Codecs: attrs["CODECS"],
				Resolution: attrs["RESOLUTION"]}
			if ab, ok := attrs["AVERAGE-BANDWIDTH"]; ok {
				if v.AverageBandwidth, err = strconv.Atoi(ab); err != nil {
					return m, fmt.Errorf("m3u8: bad AVERAGE-BANDWIDTH %q", ab)
				}
			}
			pending = &v
		}
	}
	if err := sc.Err(); err != nil {
		return m, err
	}
	if !sawHeader {
		return m, errors.New("m3u8: missing #EXTM3U header")
	}
	return m, nil
}

// parseAttributes splits a comma-separated KEY=VALUE attribute list,
// honouring quoted values that themselves contain commas.
func parseAttributes(s string) (map[string]string, error) {
	out := make(map[string]string)
	var key, val strings.Builder
	inQuote := false
	flush := func() error {
		k := strings.TrimSpace(key.String())
		if k == "" {
			return nil
		}
		out[k] = val.String()
		key.Reset()
		val.Reset()
		return nil
	}
	readingVal := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '"':
			// Quote characters are part of the attribute value syntax and
			// are retained verbatim; the flag only guards comma splitting.
			inQuote = !inQuote
			if readingVal {
				val.WriteByte(c)
			}
		case c == '=' && !inQuote:
			readingVal = true
		case c == ',' && !inQuote:
			if err := flush(); err != nil {
				return nil, err
			}
			readingVal = false
		default:
			if readingVal {
				val.WriteByte(c)
			} else {
				key.WriteByte(c)
			}
		}
	}
	if inQuote {
		return nil, errors.New("m3u8: unterminated quoted attribute")
	}
	if err := flush(); err != nil {
		return nil, err
	}
	return out, nil
}

func atoi(tag, val string) (int, error) {
	n, err := strconv.Atoi(strings.TrimSpace(val))
	if err != nil {
		return 0, fmt.Errorf("m3u8: bad %s value %q", tag, val)
	}
	return n, nil
}
