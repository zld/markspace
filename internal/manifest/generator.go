// Package manifest renders the HLS master and media playlists for sliced
// CMAF tracks. The generator is a pure function of the slice layouts, so the
// verifier can rebuild the canonical manifest from disk and byte-compare it.
package manifest

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"hlsforge/internal/segment"
)

// TrackInput binds a sliced track to the playlist metadata it exposes.
type TrackInput struct {
	Bitrate    int
	Layout     *segment.TrackSegments
	InitURI    string
	CanSkipTo  bool // first segment is independently decodable
	HasPreload bool // emit an EXT-X-PRELOAD-HINT for the next part
}

// Version is the HLS protocol version advertised for low-latency playlists.
const Version = 9

// MasterPlaylist renders the EXT-X-STREAM-INF master playlist.
func MasterPlaylist(tracks []TrackInput) string {
	var b strings.Builder
	b.WriteString("#EXTM3U\n")
	fmt.Fprintf(&b, "#EXT-X-VERSION:%d\n", Version)
	for _, t := range tracks {
		fmt.Fprintf(&b, "#EXT-X-STREAM-INF:BANDWIDTH=%d,RESOLUTION=%s\n",
			t.Bitrate, resolutionFor(t.Bitrate))
		b.WriteString(t.Layout.MediaPlaylist + "\n")
	}
	return b.String()
}

// seconds renders an integer-millisecond duration as an HLS decimal-seconds
// string. It is pure integer division/modulo: no floating-point time math is
// performed anywhere, so long timelines cannot accumulate rounding error.
func seconds(ms int64) string {
	return fmt.Sprintf("%d.%03d", ms/1000, ms%1000)
}

// MediaPlaylist renders one low-latency media playlist including EXT-X-MAP,
// EXT-X-PART entries and an EXT-X-PRELOAD-HINT.
func MediaPlaylist(t TrackInput) string {
	l := t.Layout
	target := ceilDiv(l.SegmentMs, 1000)
	var b strings.Builder
	b.WriteString("#EXTM3U\n")
	fmt.Fprintf(&b, "#EXT-X-VERSION:%d\n", Version)
	fmt.Fprintf(&b, "#EXT-X-TARGETDURATION:%d\n", target)
	fmt.Fprintf(&b, "#EXT-X-SERVER-CONTROL:CAN-BLOCK-RELOAD=YES,PART-HOLD-BACK=%s,CAN-SKIP-UNTIL=%d\n",
		seconds(3*l.PartMs), 2*target)
	fmt.Fprintf(&b, "#EXT-X-PART-INF:PART-TARGET=%s\n", seconds(l.PartMs))
	mediaSeq := 0
	if len(l.Segments) > 0 {
		mediaSeq = l.Segments[0].MediaSeq
	}
	fmt.Fprintf(&b, "#EXT-X-MEDIA-SEQUENCE:%d\n", mediaSeq)
	fmt.Fprintf(&b, "#EXT-X-MAP:URI=\"%s\"\n", t.InitURI)
	for _, seg := range l.Segments {
		for _, p := range seg.Parts {
			if p.Gap {
				// GAP slots stay visible as explicit markers in a comment so
				// the playlist never silently renumbers later parts.
				fmt.Fprintf(&b, "#EXT-X-GAP:SEQ=%d\n", p.PartSeq)
				continue
			}
			ind := ""
			if p.Independent {
				ind = ",INDEPENDENT=YES"
			}
			fmt.Fprintf(&b, "#EXT-X-PART:URI=\"%s\",DURATION=%s%s\n",
				p.URI, seconds(p.DurationMs), ind)
		}
		if seg.HasContent {
			fmt.Fprintf(&b, "#EXTINF:%s,\n", seconds(seg.DurationMs))
			b.WriteString(seg.URI + "\n")
		} else {
			fmt.Fprintf(&b, "#EXT-X-GAP:SEGMENT=%d\n", seg.MediaSeq)
		}
	}
	if t.HasPreload {
		nextSeq := mediaSeq + len(l.Segments)
		hint := fmt.Sprintf("%s/seg-%05d-part-0.mp4", l.SegmentDir, nextSeq)
		fmt.Fprintf(&b, "#EXT-X-PRELOAD-HINT:TYPE=PART,URI=\"%s\"\n", hint)
	}
	b.WriteString("#EXT-X-ENDLIST\n")
	return b.String()
}

// WriteAll writes master + media playlists under outDir using atomic rename.
func WriteAll(outDir, masterName string, tracks []TrackInput) error {
	for _, t := range tracks {
		path := filepath.Join(outDir, t.Layout.MediaPlaylist)
		if err := atomicWritePlaylist(path, MediaPlaylist(t)); err != nil {
			return err
		}
	}
	return atomicWritePlaylist(filepath.Join(outDir, masterName), MasterPlaylist(tracks))
}

func atomicWritePlaylist(path, content string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".m3u8-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	if _, err := tmp.WriteString(content); err != nil {
		tmp.Close()
		os.Remove(name)
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		os.Remove(name)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(name)
		return err
	}
	return os.Rename(name, path)
}

// resolutionFor maps a bitrate to a stable label for the master playlist.
func resolutionFor(bitrate int) string {
	switch {
	case bitrate >= 1_500_000:
		return "1280x720"
	case bitrate >= 800_000:
		return "854x480"
	default:
		return "640x360"
	}
}

func ceilDiv(v, d int64) int64 { return (v + d - 1) / d }
