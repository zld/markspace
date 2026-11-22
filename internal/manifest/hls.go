package manifest

import (
	"bufio"
	"fmt"
	"io"
	"strconv"
	"strings"

	"mediapipe/internal/clock"
)

// ParseHLS parses a media playlist. Gap policy: an #EXT-X-DISCONTINUITY tag
// marks a dropped segment and is expanded into an explicit Missing ref of
// one target duration, so the timeline is never silently shortened.
func ParseHLS(track string, r io.Reader) (*Playlist, error) {
	fail := func(op string, err error) (*Playlist, error) {
		return nil, &clock.StageError{Track: track, Stage: clock.StageManifest, Op: op, Err: err}
	}
	var (
		refs       []SegmentRef
		seq        int64
		target     clock.Ticks
		haveSeq    bool
		pendingDur clock.Ticks
		haveDur    bool
		complete   bool
		seenURI    = map[string]bool{}
		cursor     clock.Ticks
	)
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	lineNo := 0
	for sc.Scan() {
		lineNo++
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		switch {
		case strings.HasPrefix(line, "#EXT-X-MEDIA-SEQUENCE:"):
			v, err := strconv.ParseInt(strings.TrimPrefix(line, "#EXT-X-MEDIA-SEQUENCE:"), 10, 64)
			if err != nil || v < 0 {
				return fail("media-sequence", fmt.Errorf("line %d: bad sequence %q", lineNo, line))
			}
			seq = v
			haveSeq = true
		case strings.HasPrefix(line, "#EXT-X-TARGETDURATION:"):
			v, err := strconv.ParseFloat(strings.TrimPrefix(line, "#EXT-X-TARGETDURATION:"), 64)
			if err != nil || v <= 0 {
				return fail("target-duration", fmt.Errorf("line %d: non-positive target duration", lineNo))
			}
			target = clock.FromSeconds(v)
		case strings.HasPrefix(line, "#EXTINF:"):
			rest := strings.TrimPrefix(line, "#EXTINF:")
			if i := strings.IndexByte(rest, ','); i >= 0 {
				rest = rest[:i]
			}
			v, err := strconv.ParseFloat(rest, 64)
			if err != nil || v <= 0 {
				return fail("extinf", fmt.Errorf("line %d: non-positive segment duration %q", lineNo, rest))
			}
			pendingDur = clock.FromSeconds(v)
			haveDur = true
		case line == "#EXT-X-DISCONTINUITY":
			if target <= 0 {
				return fail("discontinuity", fmt.Errorf("line %d: discontinuity before target duration", lineNo))
			}
			refs = append(refs, SegmentRef{
				TrackID:  track,
				Index:    -1,
				Interval: clock.Interval{Start: cursor, End: cursor + target},
				Missing:  true,
			})
			cursor += target
		case line == "#EXT-X-ENDLIST":
			complete = true
		case strings.HasPrefix(line, "#"):
			// Other tags are not needed by this pipeline; ignore.
		default:
			if !haveSeq {
				return fail("sequence", fmt.Errorf("line %d: segment before media sequence", lineNo))
			}
			if !haveDur {
				return fail("extinf", fmt.Errorf("line %d: segment URI without EXTINF", lineNo))
			}
			if err := ValidateURI(line); err != nil {
				return fail("uri", fmt.Errorf("line %d: %w", lineNo, err))
			}
			if seenURI[line] {
				return fail("duplicate", fmt.Errorf("line %d: duplicate segment %q", lineNo, line))
			}
			seenURI[line] = true
			refs = append(refs, SegmentRef{
				TrackID:  track,
				Index:    seq,
				URI:      line,
				Interval: clock.Interval{Start: cursor, End: cursor + pendingDur},
			})
			seq++
			cursor += pendingDur
			haveDur = false
		}
	}
	if err := sc.Err(); err != nil {
		return fail("scan", err)
	}
	if len(refs) == 0 {
		return fail("empty", fmt.Errorf("playlist has no segments"))
	}
	// Assign stable negative indices to discontinuity gaps in order.
	miss := int64(-1)
	for i := range refs {
		if refs[i].Missing {
			refs[i].Index = miss
			miss--
		}
	}
	return &Playlist{TrackID: track, Refs: refs, Complete: complete}, nil
}
