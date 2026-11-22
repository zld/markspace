// Package manifest parses local HLS and DASH playlists into ordered segment
// references on the shared integer timeline. It rejects path escapes,
// duplicate sequence numbers and non-positive durations, and marks missing
// segments explicitly instead of silently shortening the timeline.
package manifest

import (
	"fmt"
	"path"
	"strings"

	"mediapipe/internal/clock"
)

// SegmentRef is one ordered segment slot on a track timeline.
type SegmentRef struct {
	TrackID  string
	Index    int64          // manifest sequence number
	URI      string         // playlist-relative, already validated
	Interval clock.Interval // position on the shared timeline
	Missing  bool           // marked by explicit gap policy
}

// Playlist is the parsed result for one track.
type Playlist struct {
	TrackID  string
	Refs     []SegmentRef
	Complete bool // false when the playlist declares itself unfinished
}

// LiveRefs returns only the refs that should be fetched (non-missing).
func (p *Playlist) LiveRefs() []SegmentRef {
	out := make([]SegmentRef, 0, len(p.Refs))
	for _, r := range p.Refs {
		if !r.Missing {
			out = append(out, r)
		}
	}
	return out
}

// Duration returns the full timeline span including missing slots.
func (p *Playlist) Duration() clock.Ticks {
	if len(p.Refs) == 0 {
		return 0
	}
	return p.Refs[len(p.Refs)-1].Interval.End
}

// ValidateURI rejects absolute paths and parent-directory escapes so a
// hostile playlist cannot point outside its own directory.
func ValidateURI(uri string) error {
	if uri == "" {
		return fmt.Errorf("empty segment URI")
	}
	if strings.HasPrefix(uri, "/") || strings.HasPrefix(uri, "\\") {
		return fmt.Errorf("absolute path %q", uri)
	}
	if strings.Contains(uri, "://") {
		return fmt.Errorf("remote URI %q not allowed", uri)
	}
	clean := path.Clean(uri)
	if clean == ".." || strings.HasPrefix(clean, "../") {
		return fmt.Errorf("path escape %q", uri)
	}
	return nil
}

// fillGaps walks refs in index order and inserts Missing refs wherever the
// next interval starts after the running cursor, preserving the timeline.
func fillGaps(track string, refs []SegmentRef) []SegmentRef {
	if len(refs) == 0 {
		return refs
	}
	out := make([]SegmentRef, 0, len(refs))
	cursor := refs[0].Interval.Start
	missIdx := int64(-1)
	for _, r := range refs {
		for r.Interval.Start > cursor {
			out = append(out, SegmentRef{
				TrackID:  track,
				Index:    missIdx,
				URI:      "",
				Interval: clock.Interval{Start: cursor, End: r.Interval.Start},
				Missing:  true,
			})
			missIdx--
			cursor = r.Interval.Start
		}
		out = append(out, r)
		cursor = r.Interval.End
	}
	return out
}
