// Package hls parses media playlists with EXT-X-DISCONTINUITY sequences and
// anchors every segment onto the unified integer time base.
package hls

import (
	"bufio"
	"fmt"
	"strconv"
	"strings"

	"mediacheck/internal/clock"
	"mediacheck/internal/model"
	"mediacheck/internal/safeio"
)

// Segment is one playlist entry on the master timeline.
type Segment struct {
	URI              string
	Sequence         uint64
	DiscontinuitySeq uint64
	StartTick        int64
	EndTick          int64
	AfterReset       bool // first segment following a discontinuity
}

// Playlist is the parsed, timeline-anchored result.
type Playlist struct {
	TargetDurationTick int64
	MediaSequence      uint64
	Segments           []*Segment
	Discontinuities    []model.Range
	Issues             []model.Issue
}

// Parse reads a media playlist. Durations are converted through the exact
// decimal parser in the clock package; a reset after
// EXT-X-DISCONTINUITY starts a fresh discontinuity sequence rather than
// appearing as a backwards timestamp.
func Parse(content string, lim safeio.Limits) (*Playlist, error) {
	sc := bufio.NewScanner(strings.NewReader(content))
	sc.Buffer(make([]byte, 0, 64*1024), int(lim.MaxParseBytes))
	p := &Playlist{}
	tl := clock.NewTimeline()
	var (
		pendingDuration int64
		havePending     bool
		discoSeq        uint64
		afterReset      bool
		nextMediaSeq    uint64
		mediaSeqSet     bool
	)
	lineNo := 0
	for sc.Scan() {
		lineNo++
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		switch {
		case strings.HasPrefix(line, "#EXT-X-TARGETDURATION:"):
			secs := strings.TrimPrefix(line, "#EXT-X-TARGETDURATION:")
			ticks, err := clock.FromSeconds(strings.TrimSpace(secs))
			if err != nil {
				return nil, model.NewStageError(model.StageParse,
					fmt.Errorf("line %d target duration: %w", lineNo, err))
			}
			p.TargetDurationTick = ticks
		case strings.HasPrefix(line, "#EXT-X-MEDIA-SEQUENCE:"):
			v, err := strconv.ParseUint(strings.TrimSpace(strings.TrimPrefix(
				line, "#EXT-X-MEDIA-SEQUENCE:")), 10, 64)
			if err != nil {
				return nil, model.NewStageError(model.StageParse, err)
			}
			p.MediaSequence = v
			nextMediaSeq = v
			mediaSeqSet = true
		case line == "#EXT-X-DISCONTINUITY":
			discoSeq++
			afterReset = true
			boundary := tl.MarkDiscontinuity()
			p.Discontinuities = append(p.Discontinuities, boundary)
		case strings.HasPrefix(line, "#EXTINF:"):
			dur := strings.TrimPrefix(line, "#EXTINF:")
			if comma := strings.IndexByte(dur, ','); comma >= 0 {
				dur = dur[:comma]
			}
			ticks, err := clock.FromSeconds(strings.TrimSpace(dur))
			if err != nil {
				return nil, model.NewStageError(model.StageParse,
					fmt.Errorf("line %d extinf: %w", lineNo, err))
			}
			pendingDuration = ticks
			havePending = true
		case strings.HasPrefix(line, "#"):
			// Other tags (VERSION, KEY, ALLOW-CACHE ...) are intentionally ignored.
		default:
			if !havePending {
				return nil, model.NewStageError(model.StageParse,
					fmt.Errorf("line %d: segment URI without EXTINF", lineNo))
			}
			if !mediaSeqSet {
				nextMediaSeq = 0
				mediaSeqSet = true
			}
			start, err := tl.Advance(pendingDuration)
			if err != nil {
				return nil, model.NewStageError(model.StageParse, err)
			}
			if err := safeio.CheckDuration(tl.Cursor(), lim); err != nil {
				return nil, model.NewStageError(model.StageParse, err)
			}
			p.Segments = append(p.Segments, &Segment{
				URI: line, Sequence: nextMediaSeq, DiscontinuitySeq: discoSeq,
				StartTick: start, EndTick: start + pendingDuration, AfterReset: afterReset,
			})
			nextMediaSeq++
			afterReset = false
			havePending = false
		}
	}
	if err := sc.Err(); err != nil {
		return nil, model.NewStageError(model.StageParse, err)
	}
	if havePending {
		return nil, model.NewStageError(model.StageParse,
			fmt.Errorf("dangling EXTINF without segment URI"))
	}
	return p, nil
}
