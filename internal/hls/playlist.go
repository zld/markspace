// Package hls parses media playlists with EXTINF durations and
// EXT-X-DISCONTINUITY boundaries. Every diagnostic retains the byte offset
// and 1-based line number of the original playlist line.
package hls

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"mediadeliver/internal/model"
)

// Playlist is a parsed media playlist.
type Playlist struct {
	Path             string         `json:"path"`
	Version          int            `json:"version"`
	TargetDurationNS int64          `json:"target_duration_ns"`
	PlaylistType     string         `json:"playlist_type,omitempty"`
	MediaSequence    uint64         `json:"media_sequence"`
	DiscontinuitySeq uint64         `json:"discontinuity_seq"`
	Segments         []Segment      `json:"segments"`
	Intervals        []Interval     `json:"intervals"`
	EndList          bool           `json:"end_list"`
	Errors           []*model.Error `json:"errors,omitempty"`
}

// Segment is one media segment.
type Segment struct {
	MediaSequence    uint64 `json:"media_sequence"`
	DiscontinuitySeq uint64 `json:"discontinuity_seq"`
	IntervalIndex    int    `json:"interval_index"`
	IsDiscontinuity  bool   `json:"is_discontinuity"`
	URI              string `json:"uri"`
	DurationNS       int64  `json:"duration_ns"`
	StartNS          int64  `json:"start_ns"`           // program time in the playlist
	IntervalOffsetNS int64  `json:"interval_offset_ns"` // media time since interval start
	Line             int    `json:"line"`
	LineOffset       int64  `json:"line_offset"`
}

// EndNS returns the program-time end of the segment.
func (s Segment) EndNS() int64 { return s.StartNS + s.DurationNS }

// Interval is a continuous media time range between discontinuities.
type Interval struct {
	Index            int    `json:"index"`
	DiscontinuitySeq uint64 `json:"discontinuity_seq"`
	StartNS          int64  `json:"start_ns"`
	DurationNS       int64  `json:"duration_ns"`
	SegmentCount     int    `json:"segment_count"`
	Line             int    `json:"line"`
	LineOffset       int64  `json:"line_offset"`
}

// EndNS returns the program-time end of the interval.
func (iv Interval) EndNS() int64 { return iv.StartNS + iv.DurationNS }

type line struct {
	text   string
	number int
	offset int64
}

// ParseFile reads and parses a media playlist from disk.
func ParseFile(path string) (*Playlist, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, model.Wrap(err, model.CodeHLSInvalid, path, model.NoOffset, model.StageParse, false)
	}
	return Parse(data, path)
}

// Parse parses playlist bytes. A nil error means the document was readable;
// line-level diagnostics are recorded in Playlist.Errors.
func Parse(data []byte, path string) (*Playlist, error) {
	p := &Playlist{Path: path}
	lines := splitLines(data)
	if len(lines) > 0 && !strings.HasPrefix(strings.TrimSpace(lines[0].text), "#EXTM3U") {
		return nil, model.NewError(model.CodeHLSInvalid, path, lines[0].offset, model.StageParse, false,
			"playlist does not start with #EXTM3U")
	}
	if len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1].text) == "" {
		lines = lines[:len(lines)-1]
	}

	var (
		nextSeq       uint64
		seqBaseSet    bool
		programNS     int64
		intervalIndex = -1
		intervalStart int64
		intervalSeq   uint64
		pending       *line
		pendingDur    int64
		seenSeq       = map[uint64]bool{}
		haveSegment   bool
	)

	startInterval := func(l line) {
		if intervalIndex >= 0 {
			p.Intervals[intervalIndex].DurationNS = programNS - intervalStart
		}
		intervalIndex++
		intervalStart = programNS
		intervalSeq = p.DiscontinuitySeq + uint64(intervalIndex)
		p.Intervals = append(p.Intervals, Interval{
			Index:            intervalIndex,
			DiscontinuitySeq: intervalSeq,
			StartNS:          intervalStart,
			Line:             l.number,
			LineOffset:       l.offset,
		})
	}
	// The playlist always starts in interval 0.
	startInterval(line{number: 1, offset: 0})

	for i := 1; i < len(lines); i++ {
		l := lines[i]
		text := strings.TrimSpace(l.text)
		switch {
		case text == "":
			continue
		case strings.HasPrefix(text, "#EXT-X-MEDIA-SEQUENCE:"):
			value := strings.TrimPrefix(text, "#EXT-X-MEDIA-SEQUENCE:")
			v, err := strconv.ParseUint(strings.TrimSpace(value), 10, 64)
			if err != nil {
				p.Errors = append(p.Errors, model.NewError(model.CodeInvalidMediaSeq, path, l.offset,
					model.StageParse, true, fmt.Sprintf("media sequence %q is not an unsigned integer", value)))
				continue
			}
			if !seqBaseSet {
				p.MediaSequence = v
				nextSeq = v
				seqBaseSet = true
				continue
			}
			switch {
			case v < nextSeq || (v == nextSeq && haveSegment):
				// Never rewind the counter: the segments already emitted keep
				// their file order and stay strictly increasing.
				p.Errors = append(p.Errors, model.NewError(model.CodeDuplicateMediaSeq, path, l.offset,
					model.StageParse, true,
					fmt.Sprintf("EXT-X-MEDIA-SEQUENCE %d repeats sequence %d", v, nextSeq-1)))
			case v == nextSeq:
				p.Errors = append(p.Errors, model.NewError(model.CodeDuplicateMediaSeq, path, l.offset,
					model.StageParse, true,
					fmt.Sprintf("repeated EXT-X-MEDIA-SEQUENCE %d", v)))
			default:
				p.Errors = append(p.Errors, model.NewError(model.CodeUnknownMediaSeq, path, l.offset,
					model.StageParse, true,
					fmt.Sprintf("EXT-X-MEDIA-SEQUENCE jumps from %d to %d", nextSeq, v)))
				nextSeq = v
			}
		case strings.HasPrefix(text, "#EXT-X-DISCONTINUITY-SEQUENCE:"):
			value := strings.TrimSpace(strings.TrimPrefix(text, "#EXT-X-DISCONTINUITY-SEQUENCE:"))
			v, err := strconv.ParseUint(value, 10, 64)
			if err != nil {
				p.Errors = append(p.Errors, model.NewError(model.CodeHLSInvalid, path, l.offset,
					model.StageParse, true, "invalid EXT-X-DISCONTINUITY-SEQUENCE"))
				continue
			}
			if haveSegment {
				p.Errors = append(p.Errors, model.NewError(model.CodeHLSInvalid, path, l.offset,
					model.StageParse, true, "EXT-X-DISCONTINUITY-SEQUENCE appears after a segment"))
			}
			p.DiscontinuitySeq = v
			if intervalIndex == 0 && len(p.Intervals) == 1 && p.Intervals[0].SegmentCount == 0 {
				p.Intervals[0].DiscontinuitySeq = v
			}
		case strings.HasPrefix(text, "#EXT-X-TARGETDURATION:"):
			value := strings.TrimSpace(strings.TrimPrefix(text, "#EXT-X-TARGETDURATION:"))
			v, err := strconv.ParseInt(value, 10, 64)
			if err != nil || v <= 0 {
				p.Errors = append(p.Errors, model.NewError(model.CodeHLSInvalid, path, l.offset,
					model.StageParse, true, "invalid EXT-X-TARGETDURATION"))
				continue
			}
			p.TargetDurationNS = v * model.NanosPerSecond
		case strings.HasPrefix(text, "#EXT-X-VERSION:"):
			value := strings.TrimSpace(strings.TrimPrefix(text, "#EXT-X-VERSION:"))
			if v, err := strconv.Atoi(value); err == nil {
				p.Version = v
			}
		case strings.HasPrefix(text, "#EXT-X-PLAYLIST-TYPE:"):
			p.PlaylistType = strings.TrimSpace(strings.TrimPrefix(text, "#EXT-X-PLAYLIST-TYPE:"))
		case text == "#EXT-X-ENDLIST":
			p.EndList = true
		case text == "#EXT-X-DISCONTINUITY":
			if pending != nil {
				p.Errors = append(p.Errors, model.NewError(model.CodeHLSTruncated, path, pending.offset,
					model.StageParse, true, "EXT-X-DISCONTINUITY between EXTINF and its URI"))
				pending = nil
			}
			startInterval(l)
		case strings.HasPrefix(text, "#EXTINF:"):
			if pending != nil {
				p.Errors = append(p.Errors, model.NewError(model.CodeHLSTruncated, path, pending.offset,
					model.StageParse, true, "EXTINF is not followed by a URI"))
			}
			value := strings.TrimPrefix(text, "#EXTINF:")
			if i := strings.IndexByte(value, ','); i >= 0 {
				value = value[:i]
			}
			dur, err := model.ParseDecimalSeconds(value)
			if err != nil {
				p.Errors = append(p.Errors, model.NewError(model.CodeInvalidEXTINF, path, l.offset,
					model.StageParse, true, fmt.Sprintf("invalid EXTINF duration %q", value)))
				dur = 0
			}
			pending = &l
			pendingDur = dur
		case strings.HasPrefix(text, "#"):
			// Unknown tags are ignored; the playlist grammar only needs the
			// listed fields.
		default:
			if pending == nil {
				p.Errors = append(p.Errors, model.NewError(model.CodeOrphanURI, path, l.offset,
					model.StageParse, true, fmt.Sprintf("URI %q has no preceding EXTINF", text)))
				continue
			}
			seq := nextSeq
			duplicate := seenSeq[seq]
			if duplicate {
				p.Errors = append(p.Errors, model.NewError(model.CodeDuplicateMediaSeq, path, pending.offset,
					model.StageParse, true,
					fmt.Sprintf("media sequence %d already used; segment kept in file order", seq)))
			}
			seenSeq[seq] = true
			nextSeq = seq + 1
			iv := &p.Intervals[intervalIndex]
			seg := Segment{
				MediaSequence:    seq,
				DiscontinuitySeq: iv.DiscontinuitySeq,
				IntervalIndex:    intervalIndex,
				IsDiscontinuity:  iv.SegmentCount == 0 && intervalIndex > 0,
				URI:              text,
				DurationNS:       pendingDur,
				StartNS:          programNS,
				IntervalOffsetNS: programNS - iv.StartNS,
				Line:             l.number,
				LineOffset:       l.offset,
			}
			p.Segments = append(p.Segments, seg)
			iv.SegmentCount++
			if pendingDur > 0 {
				programNS += pendingDur
			}
			haveSegment = true
			pending = nil
		}
	}
	p.Intervals[intervalIndex].DurationNS = programNS - intervalStart

	if pending != nil {
		p.Errors = append(p.Errors, model.NewError(model.CodeHLSTruncated, path, pending.offset,
			model.StageParse, true, "playlist ends between EXTINF and its URI"))
	}
	if truncatedLine(lines) {
		last := lines[len(lines)-1]
		p.Errors = append(p.Errors, model.NewError(model.CodeHLSTruncated, path, last.offset,
			model.StageParse, true, "playlist ends with a truncated tag"))
	}
	model.SortErrors(p.Errors)
	return p, nil
}

// truncatedLine reports whether the final unterminated line is a tag prefix
// that cannot be a valid complete tag.
func truncatedLine(lines []line) bool {
	if len(lines) == 0 {
		return false
	}
	last := strings.TrimSpace(lines[len(lines)-1].text)
	if !strings.HasPrefix(last, "#EXT") {
		return false
	}
	if last == "#EXTM3U" || last == "#EXT-X-ENDLIST" || last == "#EXT-X-DISCONTINUITY" {
		return false
	}
	if !strings.Contains(last, ":") {
		return true
	}
	if strings.HasPrefix(last, "#EXTINF:") && strings.TrimSpace(strings.TrimPrefix(last, "#EXTINF:")) == "" {
		return true
	}
	return false
}

// splitLines returns the playlist lines together with their byte offsets.
func splitLines(data []byte) []line {
	var out []line
	start, number := 0, 1
	for i := 0; i < len(data); i++ {
		if data[i] == '\n' {
			text := string(data[start:i])
			text = strings.TrimSuffix(text, "\r")
			out = append(out, line{text: text, number: number, offset: int64(start)})
			start = i + 1
			number++
		}
	}
	if start < len(data) {
		out = append(out, line{text: string(data[start:]), number: number, offset: int64(start)})
	} else if len(data) > 0 {
		out = append(out, line{text: "", number: number, offset: int64(start)})
	}
	return out
}
