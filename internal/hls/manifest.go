package hls

import (
	"bufio"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"
)

// Custom tags: TagSeq carries the explicit media sequence number of the
// following segment, TagSHA256 its content hash.
const (
	TagSeq    = "#EXT-X-SEGMENT-SEQ:"
	TagSHA256 = "#EXT-X-SEGMENT-SHA256:"
)

// maxDuration bounds a plausible EXTINF value (one day in seconds).
const maxDuration = 86400.0

// Encode writes p as an M3U8 media playlist.
func Encode(w io.Writer, p *Playlist) error {
	bw := bufio.NewWriter(w)
	fmt.Fprintln(bw, "#EXTM3U")
	fmt.Fprintf(bw, "#EXT-X-VERSION:%d\n", p.Version)
	fmt.Fprintf(bw, "#EXT-X-TARGETDURATION:%d\n", p.TargetDuration)
	fmt.Fprintf(bw, "#EXT-X-MEDIA-SEQUENCE:%d\n", p.MediaSequence)
	for _, s := range p.Segments {
		fmt.Fprintf(bw, "%s%d\n", TagSeq, s.Seq)
		fmt.Fprintf(bw, "#EXTINF:%.3f,\n", s.Duration)
		fmt.Fprintf(bw, "%s%s\n", TagSHA256, s.SHA256)
		fmt.Fprintln(bw, s.URI)
	}
	if p.EndList {
		fmt.Fprintln(bw, "#EXT-X-ENDLIST")
	}
	return bw.Flush()
}

// Decode parses a media playlist, rejecting duplicate sequence numbers,
// illegal durations and out-of-bounds segment paths.
func Decode(r io.Reader) (*Playlist, error) {
	p := &Playlist{Version: 3}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 4096), 1<<20)
	var (
		lineNo  int
		seenAny bool
		seenMS  bool
		seenSeq = map[int]bool{}
		pendSeq = -1
		pendDur = -1.0
		pendSHA string
	)
	fail := func(format string, args ...any) error {
		return fmt.Errorf("manifest line %d: %s", lineNo, fmt.Sprintf(format, args...))
	}
	for sc.Scan() {
		lineNo++
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		if lineNo == 1 {
			if line != "#EXTM3U" {
				return nil, errors.New("manifest: missing #EXTM3U header")
			}
			seenAny = true
			continue
		}
		switch {
		case strings.HasPrefix(line, "#EXT-X-MEDIA-SEQUENCE:"):
			if seenMS {
				return nil, fail("duplicate EXT-X-MEDIA-SEQUENCE")
			}
			seenMS = true
			n, err := strconv.Atoi(strings.TrimPrefix(line, "#EXT-X-MEDIA-SEQUENCE:"))
			if err != nil || n < 0 {
				return nil, fail("invalid media sequence")
			}
			p.MediaSequence = n
		case strings.HasPrefix(line, "#EXT-X-TARGETDURATION:"):
			n, err := strconv.Atoi(strings.TrimPrefix(line, "#EXT-X-TARGETDURATION:"))
			if err != nil || n <= 0 {
				return nil, fail("invalid target duration")
			}
			p.TargetDuration = n
		case strings.HasPrefix(line, "#EXT-X-VERSION:"):
			if _, err := fmt.Sscanf(line, "#EXT-X-VERSION:%d", &p.Version); err != nil {
				return nil, fail("invalid version")
			}
		case strings.HasPrefix(line, TagSeq):
			n, err := strconv.Atoi(strings.TrimPrefix(line, TagSeq))
			if err != nil || n < 0 {
				return nil, fail("invalid segment sequence")
			}
			if seenSeq[n] {
				return nil, fail("duplicate segment sequence %d", n)
			}
			seenSeq[n] = true
			pendSeq = n
		case strings.HasPrefix(line, "#EXTINF:"):
			v := strings.TrimSuffix(strings.TrimPrefix(line, "#EXTINF:"), ",")
			f, err := strconv.ParseFloat(v, 64)
			if err != nil || f < 0 || f > maxDuration || math.IsNaN(f) || math.IsInf(f, 0) {
				return nil, fail("illegal EXTINF duration %q", v)
			}
			pendDur = f
		case strings.HasPrefix(line, TagSHA256):
			s := strings.TrimPrefix(line, TagSHA256)
			if len(s) != 64 {
				return nil, fail("malformed sha256 tag")
			}
			if _, err := hex.DecodeString(s); err != nil {
				return nil, fail("malformed sha256 tag")
			}
			pendSHA = s
		case line == "#EXT-X-ENDLIST":
			p.EndList = true
		case strings.HasPrefix(line, "#"):
			// Unknown tag: tolerated and ignored.
		default:
			if err := validateURI(line); err != nil {
				return nil, fail("%v", err)
			}
			if pendSeq < 0 || pendDur < 0 || pendSHA == "" {
				return nil, fail("segment URI %q missing SEQ/EXTINF/SHA256", line)
			}
			p.Segments = append(p.Segments, Segment{Seq: pendSeq, URI: line, Duration: pendDur, SHA256: pendSHA})
			pendSeq, pendDur, pendSHA = -1, -1, ""
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if !seenAny {
		return nil, errors.New("manifest: empty input")
	}
	return p, nil
}

// validateURI rejects absolute paths and directory traversal so a
// manifest can never point outside its own directory.
func validateURI(u string) error {
	if u == "" || strings.HasPrefix(u, "/") || strings.Contains(u, "\\") {
		return fmt.Errorf("out-of-bounds path %q", u)
	}
	for _, part := range strings.Split(u, "/") {
		if part == ".." {
			return fmt.Errorf("out-of-bounds path %q", u)
		}
	}
	return nil
}
