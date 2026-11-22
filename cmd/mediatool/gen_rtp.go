package main

import (
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"strings"

	"github.com/pion/rtp"
)

// writeManifest renders the track playlist; dropped >= 0 omits that segment
// index from the manifest, producing an explicit gap the parser must mark.
func writeManifest(dir string, ts *TrackSample, dropped int) (string, error) {
	switch ts.Format {
	case "hls":
		var b strings.Builder
		b.WriteString("#EXTM3U\n#EXT-X-VERSION:3\n#EXT-X-TARGETDURATION:2\n#EXT-X-MEDIA-SEQUENCE:0\n")
		for s := 0; s < segsPerTrack; s++ {
			if s == dropped {
				b.WriteString("#EXT-X-DISCONTINUITY\n")
				continue
			}
			fmt.Fprintf(&b, "#EXTINF:2.000000,\n%s/seg%d.m4s\n", ts.ID, s)
		}
		b.WriteString("#EXT-X-ENDLIST\n")
		p := filepath.Join(dir, ts.ID+".m3u8")
		return p, os.WriteFile(p, []byte(b.String()), 0o644)
	case "dash":
		d := segSeconds * ts.InRate
		var tl strings.Builder
		if dropped < 0 {
			fmt.Fprintf(&tl, "<S t=\"0\" d=\"%d\" r=\"%d\"/>\n", d, segsPerTrack-1)
		} else {
			fmt.Fprintf(&tl, "<S t=\"0\" d=\"%d\" r=\"%d\"/>\n", d, dropped-1)
			fmt.Fprintf(&tl, "<S t=\"%d\" d=\"%d\" r=\"%d\"/>\n", (dropped+1)*d, d, segsPerTrack-dropped-2)
		}
		mpd := fmt.Sprintf(`<MPD type="static"><Period><AdaptationSet><Representation id="%s">
<BaseURL>%s/</BaseURL>
<SegmentTemplate timescale="%d" media="seg$Number$.m4s" startNumber="0">
<SegmentTimeline>
%s</SegmentTimeline>
</SegmentTemplate>
</Representation></AdaptationSet></Period></MPD>`, ts.ID, ts.ID, ts.InRate, tl.String())
		p := filepath.Join(dir, ts.ID+".mpd")
		return p, os.WriteFile(p, []byte(mpd), 0o644)
	}
	return "", fmt.Errorf("unknown format %q", ts.Format)
}

// genRTP builds one track's packet stream in received order, injecting
// reorder, duplicates, in-window loss with retransmission, and optionally a
// loss burst beyond the recovery window.
func genRTP(rng *rand.Rand, trackIdx int, lossBurst bool) []*rtp.Packet {
	n := trackSeconds * rtpPacketsSec
	start := uint16(rng.Intn(60000))
	if trackIdx == 0 {
		start = 65530 // exercise the 16-bit wrap
	}
	mk := func(i int) *rtp.Packet {
		p := &rtp.Packet{}
		p.SequenceNumber = start + uint16(i)
		p.Timestamp = uint32(i * 960)
		p.SSRC = uint32(1000 + trackIdx)
		p.Payload = []byte{byte(i), byte(i >> 8), 0xA5}
		return p
	}
	burstAt := -1
	if lossBurst {
		burstAt = n / 2 // drop 12 consecutive packets, no retransmit
	}
	var out []*rtp.Packet
	var pending []int // dropped, awaiting retransmission
	for i := 0; i < n; i++ {
		inBurst := burstAt >= 0 && i >= burstAt && i < burstAt+12
		switch r := rng.Float64(); {
		case inBurst:
			// lost beyond recovery window
		case r < 0.10:
			pending = append(pending, i) // in-window loss, retransmit later
		case r < 0.15 && i+1 < n && burstAt < 0:
			out = append(out, mk(i+1), mk(i)) // reorder pair
			i++
		default:
			out = append(out, mk(i))
			if rng.Float64() < 0.02 {
				out = append(out, mk(i)) // duplicate
			}
		}
		var left []int
		for _, d := range pending {
			if i-d >= 3 {
				out = append(out, mk(d)) // retransmission inside window
			} else {
				left = append(left, d)
			}
		}
		pending = left
	}
	for _, d := range pending {
		out = append(out, mk(d))
	}
	return out
}
