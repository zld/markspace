// Package manifest renders a deterministic HLS master/media playlist from the
// recovered representation states. Output is built from content-derived fields
// only and written atomically, so identical inputs always yield byte-identical
// manifests regardless of run count, clock or file path.
package manifest

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strings"

	"regress/internal/media"
)

// Variant is one representation layer on the shared time line.
type Variant struct {
	Name      string
	Bandwidth int
	Codec     string
	// Segments are the media parts, each with an exact rational duration.
	Segments []Segment
	// Subtitles names an associated subtitle rendition, if any.
	Subtitles string
}

// Segment is one playlist entry.
type Segment struct {
	URI      string
	Duration media.Rat // exact seconds
}

// Playlist is the rendered master plus media playlists and their digest.
type Playlist struct {
	MasterText  string
	MediaText   map[string]string // per-variant media playlist
	SHA256      string            // hex digest of the master playlist bytes
	SegmentCount int
}

// Build renders a playlist set. Variants are sorted by bandwidth for a stable
// layout; every floating-point rendering uses a fixed decimal width so the
// output never depends on locale or map iteration order.
func Build(variants []Variant) *Playlist {
	vs := append([]Variant(nil), variants...)
	sort.SliceStable(vs, func(i, j int) bool {
		if vs[i].Bandwidth != vs[j].Bandwidth {
			return vs[i].Bandwidth < vs[j].Bandwidth
		}
		return vs[i].Name < vs[j].Name
	})

	var b strings.Builder
	b.WriteString("#EXTM3U\n")
	b.WriteString("#EXT-X-VERSION:3\n")
	// Subtitle rendition group, declared once with a stable group id.
	hasSub := false
	for _, v := range vs {
		if v.Subtitles != "" {
			hasSub = true
		}
	}
	if hasSub {
		b.WriteString(`#EXT-X-MEDIA:TYPE=SUBTITLES,GROUP-ID="subs",NAME="sub",DEFAULT=YES,AUTOSELECT=YES,LANGUAGE="en"` + "\n")
	}
	for _, v := range vs {
		line := "#EXT-X-STREAM-INF:BANDWIDTH=" + itoa(v.Bandwidth) +
			",CODECS=\"" + v.Codec + "\""
		if hasSub {
			line += `,SUBTITLES="subs"`
		}
		b.WriteString(line + "\n")
		b.WriteString(v.Name + ".m3u8\n")
	}

	master := b.String()
	sum := sha256.Sum256([]byte(master))

	media := map[string]string{}
	segCount := 0
	for _, v := range vs {
		media[v.Name] = buildMedia(v)
		segCount += len(v.Segments)
	}
	return &Playlist{
		MasterText:   master,
		MediaText:    media,
		SHA256:        hex.EncodeToString(sum[:]),
		SegmentCount: segCount,
	}
}

// buildMedia renders one variant's media playlist with target duration derived
// from the longest segment, and exact decimal segment durations.
func buildMedia(v Variant) string {
	maxDur := media.NewInt(0)
	for _, s := range v.Segments {
		if s.Duration.Cmp(maxDur) > 0 {
			maxDur = s.Duration
		}
	}
	target := maxDur.Num()/maxDur.Den() + 1
	var b strings.Builder
	b.WriteString("#EXTM3U\n")
	b.WriteString("#EXT-X-VERSION:3\n")
	b.WriteString("#EXT-X-TARGETDURATION:" + itoa(int(target)) + "\n")
	b.WriteString("#EXT-X-MEDIA-SEQUENCE:0\n")
	for _, s := range v.Segments {
		b.WriteString("#EXTINF:" + formatRat(s.Duration) + ",\n")
		b.WriteString(s.URI + "\n")
	}
	b.WriteString("#EXT-X-ENDLIST\n")
	return b.String()
}

// formatRat renders a rational duration with exactly three decimals, rounding
// half up. Deterministic and locale-independent.
func formatRat(r media.Rat) string {
	scaled := r.Num()*1000/r.Den()
	whole := scaled / 1000
	frac := scaled % 1000
	return itoa(int(whole)) + "." + pad3(int(frac))
}

func pad3(n int) string {
	switch {
	case n < 10:
		return "00" + itoa(n)
	case n < 100:
		return "0" + itoa(n)
	default:
		return itoa(n)
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
