// Package manifest renders HLS playlists from repaired timelines. A segment
// whose duration could not be inferred is emitted as an explicit GAP rather
// than silently dropped, so downstream players and operators can see the
// discontinuity instead of fetching a stale or replaced file.
package manifest

import (
	"fmt"
	"io"
	"sort"
	"strings"

	"cmafrepair/pkg/media/cmaf"
)

// Variant describes one rendition for the master playlist.
type Variant struct {
	Name       string
	Bandwidth  int
	Resolution string // e.g. "1280x720"; empty for audio
	Codecs     string
	MediaPlaylist
}

// MediaPlaylist is the segment list of one representation.
type MediaPlaylist struct {
	Name           string
	TargetDuration int // whole seconds, rounded up
	Segments       []Segment
}

// Segment is one media playlist entry.
type Segment struct {
	URI           string
	DurationMs    int64
	Gap           bool
	Discontinuity bool // set on the first segment after a GAP
	Sequence      uint32
}

// FromTimeline converts a repaired timeline into a media playlist.
func FromTimeline(tl *cmaf.Timeline) MediaPlaylist {
	pl := MediaPlaylist{Name: tl.Representation}
	var maxDur int64
	prevGap := false
	for _, e := range tl.Entries {
		seg := Segment{
			URI:        e.Name,
			DurationMs: e.EndMs - e.StartMs,
			Sequence:   e.Sequence,
		}
		switch e.Status {
		case cmaf.StatusGap, cmaf.StatusTruncated:
			seg.Gap = true
			seg.URI = ""
		}
		seg.Discontinuity = prevGap && !seg.Gap
		prevGap = seg.Gap
		if seg.DurationMs > maxDur {
			maxDur = seg.DurationMs
		}
		pl.Segments = append(pl.Segments, seg)
	}
	pl.TargetDuration = ceilSeconds(maxDur)
	return pl
}

func ceilSeconds(ms int64) int {
	if ms <= 0 {
		return 1
	}
	return int((ms + 999) / 1000)
}

// GapCount reports how many entries are marked GAP.
func (pl MediaPlaylist) GapCount() int {
	n := 0
	for _, s := range pl.Segments {
		if s.Gap {
			n++
		}
	}
	return n
}

// WriteMediaPlaylist writes an HLS media playlist. GAP segments are rendered
// as an EXT-X-GAP tag (HLS 2nd edition draft) with the declared duration.
func WriteMediaPlaylist(w io.Writer, pl MediaPlaylist, mediaSeq uint32) error {
	var b strings.Builder
	b.WriteString("#EXTM3U\n")
	b.WriteString("#EXT-X-VERSION:9\n")
	fmt.Fprintf(&b, "#EXT-X-TARGETDURATION:%d\n", pl.TargetDuration)
	fmt.Fprintf(&b, "#EXT-X-MEDIA-SEQUENCE:%d\n", mediaSeq)
	b.WriteString("#EXT-X-PLAYLIST-TYPE:EVENT\n")
	for _, s := range pl.Segments {
		if s.Discontinuity {
			b.WriteString("#EXT-X-DISCONTINUITY\n")
		}
		dur := float64(s.DurationMs) / 1000.0
		if s.Gap {
			// A gap still occupies its declared duration on the timeline.
			fmt.Fprintf(&b, "#EXTINF:%.3f,\n", dur)
			b.WriteString("#EXT-X-GAP\n")
			continue
		}
		fmt.Fprintf(&b, "#EXTINF:%.3f,\n", dur)
		b.WriteString(s.URI)
		b.WriteByte('\n')
	}
	b.WriteString("#EXT-X-ENDLIST\n")
	_, err := io.WriteString(w, b.String())
	return err
}

// WriteMasterPlaylist writes a master playlist referencing every variant.
func WriteMasterPlaylist(w io.Writer, variants []Variant) error {
	sort.SliceStable(variants, func(i, j int) bool {
		return variants[i].Name < variants[j].Name
	})
	var b strings.Builder
	b.WriteString("#EXTM3U\n")
	b.WriteString("#EXT-X-VERSION:9\n")
	for _, v := range variants {
		attrs := []string{
			fmt.Sprintf("BANDWIDTH=%d", v.Bandwidth),
			fmt.Sprintf("CODECS=\"%s\"", v.Codecs),
		}
		if v.Resolution != "" {
			attrs = append(attrs, "RESOLUTION="+v.Resolution)
		}
		fmt.Fprintf(&b, "#EXT-X-STREAM-INF:%s\n", strings.Join(attrs, ","))
		fmt.Fprintf(&b, "%s.m3u8\n", v.Name)
	}
	_, err := io.WriteString(w, b.String())
	return err
}

// Metadata is the manifest fingerprint input for the index cache key. It
// captures exactly the fields whose change must rebuild (but not re-parse):
// target durations, segment ordering/URIs and gap placement.
type Metadata struct {
	Name           string    `json:"name"`
	TargetDuration int       `json:"target_duration"`
	Segments       []MetaSeg `json:"segments"`
}

// MetaSeg is the fingerprint view of one playlist entry.
type MetaSeg struct {
	URI string `json:"uri"`
	Gap bool   `json:"gap"`
	Seq uint32 `json:"seq"`
}

// FingerprintView renders the playlist as cache-fingerprint metadata.
func (pl MediaPlaylist) FingerprintView() Metadata {
	m := Metadata{Name: pl.Name, TargetDuration: pl.TargetDuration}
	for _, s := range pl.Segments {
		m.Segments = append(m.Segments, MetaSeg{URI: s.URI, Gap: s.Gap, Seq: s.Sequence})
	}
	return m
}
