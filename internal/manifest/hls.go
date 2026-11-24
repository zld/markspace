package manifest

import (
	"bytes"
	"fmt"
	"strings"

	"mediarun/internal/clock"
	"mediarun/internal/fault"
)

// HLSSegment is one media segment of an HLS playlist, mapped onto the
// unified play timeline. Discontinuities advance the timeline without
// assuming anything about the media timestamps inside the next segment.
type HLSSegment struct {
	Index    int         // segment index in the playlist
	URI      string      // segment URI (relative to playlist)
	Duration clock.Ticks // declared EXTINF duration on the master timeline
	Start    clock.Ticks // start on the unified play timeline
	DiscSeq  int         // discontinuity sequence number
	Offset   int64       // raw byte offset of the EXTINF line
}

// Playlist is a parsed HLS media playlist.
type Playlist struct {
	Path            string
	Version         int
	MediaSequence   int64
	TargetDuration  clock.Ticks
	Segments        []HLSSegment
	Discontinuities int
	Ended           bool
}

// ParsePlaylist parses an HLS media playlist. EXTINF durations are parsed
// as exact decimal fractions and accumulated in integer master ticks, so
// the play timeline never accumulates floating-point error.
func ParsePlaylist(path string, data []byte) (*Playlist, error) {
	pl := &Playlist{Path: path}
	var cursor clock.Ticks // unified play timeline cursor
	var discSeq int
	var pending *HLSSegment
	sawTag := false

	// Walk lines while tracking raw byte offsets.
	pos := int64(0)
	lines := bytes.Split(data, []byte("\n"))
	lineOff := make([]int64, len(lines))
	for i, ln := range lines {
		lineOff[i] = pos
		pos += int64(len(ln)) + 1
	}

	for i, raw := range lines {
		line := strings.TrimSpace(string(raw))
		if line == "" {
			continue
		}
		switch {
		case line == "#EXTM3U":
			sawTag = true
		case strings.HasPrefix(line, "#EXT-X-VERSION:"):
			fmt.Sscanf(line, "#EXT-X-VERSION:%d", &pl.Version)
		case strings.HasPrefix(line, "#EXT-X-MEDIA-SEQUENCE:"):
			fmt.Sscanf(line, "#EXT-X-MEDIA-SEQUENCE:%d", &pl.MediaSequence)
		case strings.HasPrefix(line, "#EXT-X-TARGETDURATION:"):
			var sec string
			fmt.Sscanf(line, "#EXT-X-TARGETDURATION:%s", &sec)
			d, err := clock.ParseDuration(sec)
			if err != nil {
				return nil, fault.New(Stage(), "bad TARGETDURATION").At(path, lineOff[i]).Wrap(err)
			}
			pl.TargetDuration = d
		case line == "#EXT-X-DISCONTINUITY":
			discSeq++
			pl.Discontinuities++
		case strings.HasPrefix(line, "#EXT-X-DISCONTINUITY-SEQUENCE:"):
			fmt.Sscanf(line, "#EXT-X-DISCONTINUITY-SEQUENCE:%d", &discSeq)
		case strings.HasPrefix(line, "#EXTINF:"):
			rest := strings.TrimPrefix(line, "#EXTINF:")
			if idx := strings.IndexByte(rest, ','); idx >= 0 {
				rest = rest[:idx]
			}
			d, err := clock.ParseDuration(rest)
			if err != nil {
				return nil, fault.New(Stage(), "bad EXTINF duration").
					At(path, lineOff[i]).For("hls", len(pl.Segments)).Wrap(err)
			}
			if d <= 0 {
				return nil, fault.New(Stage(), "non-positive EXTINF duration").
					At(path, lineOff[i]).For("hls", len(pl.Segments))
			}
			pending = &HLSSegment{
				Index:    len(pl.Segments),
				Duration: d,
				Start:    cursor,
				DiscSeq:  discSeq,
				Offset:   lineOff[i],
			}
			cursor += d
		case line == "#EXT-X-ENDLIST":
			pl.Ended = true
		case strings.HasPrefix(line, "#"):
			// Other tags are ignored.
		default:
			// URI line: attaches to the pending EXTINF.
			if pending == nil {
				return nil, fault.New(Stage(), "segment URI without EXTINF").
					At(path, lineOff[i]).For("hls", len(pl.Segments))
			}
			pending.URI = line
			pl.Segments = append(pl.Segments, *pending)
			pending = nil
		}
	}
	if !sawTag {
		return nil, fault.New(Stage(), "missing #EXTM3U header").At(path, 0)
	}
	if pending != nil {
		return nil, fault.New(Stage(), "EXTINF without following URI").
			At(path, pending.Offset).For("hls", pending.Index)
	}
	if len(pl.Segments) == 0 {
		return nil, fault.New(Stage(), "playlist has no segments").At(path, 0)
	}
	return pl, nil
}
