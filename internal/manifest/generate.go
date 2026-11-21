package manifest

import (
	"bytes"
	"fmt"
	"sort"
)

// BuildPlaylist applies the generator rules to the usable segments of one
// bitrate: ascending numeric order, no duplicate ids, and no fabricated
// entries. A playlist may still be returned when ids are missing; callers
// decide whether that is acceptable under their strictness mode.
func BuildPlaylist(rate int, segments []Segment) (Playlist, error) {
	usable := make([]Segment, 0, len(segments))
	seen := make(map[int]string, len(segments))
	for _, seg := range segments {
		if seg.RateKbps != rate {
			continue
		}
		if !seg.Usable() {
			continue
		}
		if prev, dup := seen[seg.ID]; dup {
			return Playlist{}, fmt.Errorf("rate %d: duplicate segment id %d in %q and %q",
				rate, seg.ID, prev, seg.FileName)
		}
		seen[seg.ID] = seg.FileName
		usable = append(usable, seg)
	}
	sort.Slice(usable, func(i, j int) bool { return usable[i].ID < usable[j].ID })

	entries := make([]PlaylistSegment, 0, len(usable))
	for _, seg := range usable {
		entries = append(entries, PlaylistSegment{
			ID:         seg.ID,
			FileName:   seg.FileName,
			DurationMS: seg.DurationMS,
		})
	}
	mediaSequence := 0
	if len(entries) > 0 {
		mediaSequence = entries[0].ID
	}
	return Playlist{
		RateKbps:       rate,
		Version:        3,
		TargetDuration: TargetDurationSeconds(entries),
		MediaSequence:  mediaSequence,
		Segments:       entries,
	}, nil
}

// Render renders the media playlist. The same rules are re-applied by the
// verifier, so generation and verification cannot drift apart.
func Render(p Playlist) []byte {
	var b bytes.Buffer
	b.WriteString("#EXTM3U\n")
	fmt.Fprintf(&b, "#EXT-X-VERSION:%d\n", p.Version)
	fmt.Fprintf(&b, "#EXT-X-TARGETDURATION:%d\n", p.TargetDuration)
	fmt.Fprintf(&b, "#EXT-X-MEDIA-SEQUENCE:%d\n", p.MediaSequence)
	b.WriteString("#EXT-X-PLAYLIST-TYPE:VOD\n")
	b.WriteString("#EXT-X-INDEPENDENT-SEGMENTS\n")
	for _, seg := range p.Segments {
		fmt.Fprintf(&b, "#EXTINF:%s,\n%s\n", FormatEXTINF(seg.DurationMS), seg.FileName)
	}
	b.WriteString("#EXT-X-ENDLIST\n")
	return b.Bytes()
}

// ExpectedSegments derives the ordered segment list a playlist must contain
// from the same segment facts the generator used.
func ExpectedSegments(rate int, segments []Segment) []PlaylistSegment {
	usable := make([]Segment, 0, len(segments))
	for _, seg := range segments {
		if seg.RateKbps == rate && seg.Usable() {
			usable = append(usable, seg)
		}
	}
	sort.Slice(usable, func(i, j int) bool { return usable[i].ID < usable[j].ID })
	out := make([]PlaylistSegment, 0, len(usable))
	for _, seg := range usable {
		out = append(out, PlaylistSegment{ID: seg.ID, FileName: seg.FileName, DurationMS: seg.DurationMS})
	}
	return out
}
