// Package m3u8 generates and parses the subset of HLS playlists this
// pipeline publishes: VOD/event media playlists with discontinuities and a
// simple master playlist. No third-party HLS library is used.
package m3u8

import (
	"fmt"
	"io"
	"sort"
	"strings"
)

// Segment is one media playlist entry.
type Segment struct {
	URI string
	// Duration is the EXTINF value in seconds.
	Duration float64
	// Discontinuity inserts EXT-X-DISCONTINUITY before the segment.
	Discontinuity bool
	// MediaSeq is the EXT-X-MEDIA-SEQUENCE number for this entry.
	MediaSeq int
}

// Variant is one master playlist rendition.
type Variant struct {
	URI       string
	Bandwidth int
	// AverageBandwidth is optional; zero omits the attribute.
	AverageBandwidth int
	Name             string
	Codecs           string
}

// MediaPlaylist is a media (rendition) playlist.
type MediaPlaylist struct {
	TargetDuration int
	MediaSequence  int
	Segments       []Segment
	// Closed writes EXT-X-ENDLIST (VOD). Otherwise an event playlist is
	// assumed and later segments may be appended.
	Closed bool
}

func formatSeconds(d float64) string {
	// Three decimals keeps the values stable and human readable.
	return strings.TrimRight(strings.TrimRight(fmt.Sprintf("%.3f", d), "0"), ".")
}

// Write renders the media playlist to w.
func (p MediaPlaylist) Write(w io.Writer) error {
	var b strings.Builder
	b.WriteString("#EXTM3U\n")
	b.WriteString(fmt.Sprintf("#EXT-X-VERSION:3\n"))
	b.WriteString(fmt.Sprintf("#EXT-X-TARGETDURATION:%d\n", p.TargetDuration))
	b.WriteString(fmt.Sprintf("#EXT-X-MEDIA-SEQUENCE:%d\n", p.MediaSequence))
	b.WriteString("#EXT-X-PLAYLIST-TYPE:" + map[bool]string{true: "VOD", false: "EVENT"}[p.Closed] + "\n")
	for _, s := range p.Segments {
		if s.Discontinuity {
			b.WriteString("#EXT-X-DISCONTINUITY\n")
		}
		b.WriteString(fmt.Sprintf("#EXTINF:%s,\n", formatSeconds(s.Duration)))
		b.WriteString(s.URI)
		b.WriteString("\n")
	}
	if p.Closed {
		b.WriteString("#EXT-X-ENDLIST\n")
	}
	_, err := io.WriteString(w, b.String())
	return err
}

// Bytes renders the playlist to a byte slice.
func (p MediaPlaylist) Bytes() []byte {
	var b strings.Builder
	_ = p.Write(&b)
	return []byte(b.String())
}

// MasterPlaylist groups variants for output as a master playlist.
type MasterPlaylist struct {
	Variants []Variant
}

// Write renders the master playlist to w, sorted by bandwidth.
func (m MasterPlaylist) Write(w io.Writer) error {
	vars := append([]Variant(nil), m.Variants...)
	sort.Slice(vars, func(i, j int) bool { return vars[i].Bandwidth < vars[j].Bandwidth })
	var b strings.Builder
	b.WriteString("#EXTM3U\n")
	b.WriteString("#EXT-X-VERSION:3\n")
	for _, v := range vars {
		attrs := []string{
			fmt.Sprintf("BANDWIDTH=%d", v.Bandwidth),
		}
		if v.AverageBandwidth > 0 {
			attrs = append(attrs, fmt.Sprintf("AVERAGE-BANDWIDTH=%d", v.AverageBandwidth))
		}
		if v.Name != "" {
			attrs = append(attrs, fmt.Sprintf("NAME=%q", v.Name))
		}
		if v.Codecs != "" {
			attrs = append(attrs, fmt.Sprintf("CODECS=%q", v.Codecs))
		}
		b.WriteString("#EXT-X-STREAM-INF:" + strings.Join(attrs, ",") + "\n")
		b.WriteString(v.URI + "\n")
	}
	_, err := io.WriteString(w, b.String())
	return err
}

// Bytes renders the master playlist to a byte slice.
func (m MasterPlaylist) Bytes() []byte {
	var b strings.Builder
	_ = m.Write(&b)
	return []byte(b.String())
}
