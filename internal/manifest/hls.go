package manifest

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"mediabatch/internal/clock"
	"mediabatch/internal/fault"
)

// ParseHLS reads a media playlist. Playlists are parsed line by line so every
// anomaly carries the 1-based line and the byte offset of its declaration.
func ParseHLS(path string) (*Manifest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read hls %s: %w", path, err)
	}
	return ParseHLSBytes(path, data)
}

// ParseHLSBytes parses an in-memory HLS media playlist.
func ParseHLSBytes(name string, data []byte) (*Manifest, error) {
	m := &Manifest{Kind: "hls", File: name}
	if m.File == "" {
		m.File = "playlist.m3u8"
	}
	if !bytes.HasPrefix(bytes.TrimSpace(data), []byte("#EXTM3U")) {
		m.Issues = append(m.Issues, issueAt(m.File, 1, 0, fault.CodeParse, "playlist does not begin with #EXTM3U"))
		return m, fmt.Errorf("parse hls %s: missing #EXTM3U header", name)
	}

	// Variant resolution: the playlist's own directory is the reference base.
	baseDir := filepath.Dir(name)

	type pending struct {
		duration   *clock.Time
		durLine    int
		durOffset  int64
		disc       bool
		discLine   int
		discOffset int64
		// badDuration marks an EXTINF that was present but unusable, so the
		// following URI line is not reported a second time.
		badDuration bool
	}
	var p pending
	cur := clock.Zero
	repName := strings.TrimSuffix(filepath.Base(name), filepath.Ext(name))
	seen := map[string]int{}

	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	line, offset, index := 0, int64(0), 0
	for sc.Scan() {
		line++
		raw := sc.Bytes()
		lineOffset := offset
		offset += int64(len(raw)) + 1
		text := strings.TrimSpace(string(raw))
		if text == "" {
			continue
		}
		if strings.HasPrefix(text, "#") {
			tag, _, _ := strings.Cut(text, ":")
			switch tag {
			case "#EXTM3U":
			case "#EXTINF":
				val := strings.TrimPrefix(text, "#EXTINF:")
				durPart, _, _ := strings.Cut(val, ",")
				d, err := clock.FromString(strings.TrimSpace(durPart))
				if err != nil || d.Num <= 0 {
					m.Issues = append(m.Issues, fault.Issue{
						Code: fault.CodeMissingDuration, Severity: fault.SeverityError, Action: fault.ActionIsolated,
						Message: fmt.Sprintf("EXTINF duration %q is unusable", durPart),
						Source: fault.Source{
							Manifest: m.File, Representation: repName,
							Loc:   fault.Locator{File: m.File, Line: line, Offset: lineOffset, Context: text},
							Start: cur, End: cur,
						},
					})
					p.duration = nil
					p.durLine, p.durOffset = line, lineOffset
					p.badDuration = true
					continue
				}
				dv := d
				p.duration = &dv
				p.durLine, p.durOffset = line, lineOffset
			case "#EXT-X-DISCONTINUITY":
				p.disc = true
				p.discLine, p.discOffset = line, lineOffset
			case "#EXT-X-TARGETDURATION", "#EXT-X-MEDIA-SEQUENCE", "#EXT-X-VERSION",
				"#EXT-X-PLAYLIST-TYPE", "#EXT-X-ENDLIST", "#EXT-X-INDEPENDENT-SEGMENTS",
				"#EXT-X-DISCONTINUITY-SEQUENCE", "#EXT-X-I-FRAMES-ONLY":
			case "#EXT-X-KEY", "#EXT-X-MAP", "#EXT-X-PROGRAM-DATE-TIME",
				"#EXT-X-BYTERANGE", "#EXT-X-GAP", "#EXT-X-BITRATE", "#EXT-X-DATERANGE":
				// Recognised but not interpreted; the fixture generator emits
				// no media payload for these.
			default:
				m.Issues = append(m.Issues, fault.Issue{
					Code: fault.CodeUnknownTag, Severity: fault.SeverityWarning, Action: fault.ActionRecorded,
					Message: fmt.Sprintf("unknown tag %s", tag),
					Source: fault.Source{
						Manifest: m.File, Representation: repName,
						Loc:   fault.Locator{File: m.File, Line: line, Offset: lineOffset, Context: tag},
						Start: cur, End: cur,
					},
				})
			}
			continue
		}
		// A media URI line.
		if p.duration == nil {
			// No usable timing exists, so no segment is fabricated. When the
			// EXTINF line itself was bad it has already been reported there;
			// otherwise the URI line is the traceable location.
			if !p.badDuration {
				m.Issues = append(m.Issues, fault.Issue{
					Code: fault.CodeMissingDuration, Severity: fault.SeverityError, Action: fault.ActionIsolated,
					Message: "segment URI has no preceding EXTINF duration",
					Source: fault.Source{
						Manifest: m.File, Representation: repName, Segment: text,
						Loc:   fault.Locator{File: m.File, Line: line, Offset: lineOffset, Context: text},
						Start: cur, End: cur,
					},
				})
			}
			p = pending{}
			continue
		}
		prev, dup := seen[text]
		if dup {
			// A duplicate reference is isolated and does not advance the
			// presentation timeline: it describes no new media, so making it
			// consume time would silently shift every later segment. It is kept
			// in the segment list as a zero-length entry so the report can name
			// it at its exact position.
			seg := Segment{
				Representation: repName, URL: text,
				Start: cur, End: cur, Duration: clock.Zero, Declared: true,
				Index: index, Line: line, Offset: lineOffset, Isolated: true,
			}
			m.Segments = append(m.Segments, seg)
			m.Issues = append(m.Issues, fault.Issue{
				Code: fault.CodeDuplicateRef, Severity: fault.SeverityError, Action: fault.ActionIsolated,
				Message: fmt.Sprintf("duplicate segment reference; first seen on line %d", prev),
				Source: fault.Source{
					Manifest: m.File, Representation: repName, Segment: text,
					Loc:   fault.Locator{File: m.File, Line: line, Offset: lineOffset, Context: text},
					Start: seg.Start, End: seg.End,
				},
			})
			p = pending{}
			continue
		}
		seen[text] = line
		seg := Segment{
			Representation: repName, URL: text,
			Start: cur, End: cur.Add(*p.duration), Duration: *p.duration, Declared: true,
			Index: index, Line: line, Offset: lineOffset, Discontinuity: p.disc,
		}
		m.Segments = append(m.Segments, seg)
		cur = seg.End
		index++
		if p.disc {
			// The discontinuity marker belongs between segments; record its
			// exact position on the segment that follows it.
			m.Issues = append(m.Issues, fault.Issue{
				Code: fault.CodeDiscontinuity, Severity: fault.SeverityWarning, Action: fault.ActionRecorded,
				Message: "discontinuity boundary before this segment",
				Source: fault.Source{
					Manifest: m.File, Representation: repName, Segment: text,
					Loc:   fault.Locator{File: m.File, Line: p.discLine, Offset: p.discOffset},
					Start: seg.Start, End: seg.End,
				},
			})
		}
		p = pending{}
	}
	if err := sc.Err(); err != nil {
		m.Issues = append(m.Issues, issueAt(m.File, line+1, offset, fault.CodeParse, fmt.Sprintf("scan playlist: %v", err)))
		return m, err
	}
	if p.duration != nil || p.badDuration {
		m.Issues = append(m.Issues, fault.Issue{
			Code: fault.CodeMissingDuration, Severity: fault.SeverityError, Action: fault.ActionIsolated,
			Message: "EXTINF duration has no following segment URI",
			Source: fault.Source{
				Manifest: m.File, Representation: repName,
				Loc:   fault.Locator{File: m.File, Line: p.durLine, Offset: p.durOffset},
				Start: cur, End: cur,
			},
		})
	}
	m.Segments = sortSegments(m.Segments)
	m.SortIssues()
	_ = baseDir
	return m, nil
}

// ParseHLSSegmentRange translates an #EXT-X-BYTERANGE value ("len@off") into
// absolute offsets inside a segment file. It is used by the scanner when a
// playlist declares partial ranges.
func ParseHLSSegmentRange(value string) (length, start int64, err error) {
	value = strings.TrimSpace(value)
	lenPart, offPart, hasOff := strings.Cut(value, "@")
	length, err = strconv.ParseInt(strings.TrimSpace(lenPart), 10, 64)
	if err != nil || length <= 0 {
		return 0, 0, fmt.Errorf("bad byte range length %q", value)
	}
	if hasOff {
		start, err = strconv.ParseInt(strings.TrimSpace(offPart), 10, 64)
		if err != nil || start < 0 {
			return 0, 0, fmt.Errorf("bad byte range offset %q", value)
		}
	}
	return length, start, nil
}
