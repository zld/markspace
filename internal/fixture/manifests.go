package fixture

import (
	"fmt"
	"strings"
)

// buildMPD emits a multi-bitrate MPD: two video representations plus one
// audio representation, each with explicit SegmentList byte ranges and a
// declared timescale that the parser must validate.
func (b *Bundle) buildMPD(opts Options) []byte {
	var sb strings.Builder
	sb.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n")
	sb.WriteString(`<MPD type="static" profiles="urn:mpeg:dash:profile:isoff-on-demand:2011">` + "\n")
	sb.WriteString("  <Period>\n")
	reps := []struct {
		contentType, id string
		timescale       uint32
	}{
		{"video", RepVideoHi, videoTS},
		{"video", RepVideoLo, videoTS},
		{"audio", RepAudio, audioTS},
	}
	for _, rep := range reps {
		fmt.Fprintf(&sb,
			"    <AdaptationSet contentType=\"%s\">\n", rep.contentType)
		fmt.Fprintf(&sb,
			"      <Representation id=\"%s\" timescale=\"%d\">\n",
			rep.id, rep.timescale)
		fmt.Fprintf(&sb,
			"        <SegmentList init=\"%s/init.mp4\" media=\"%s/$Number$.m4s\">\n",
			rep.id, rep.id)
		var tickCursor int64
		for seg := 1; seg <= opts.Segments; seg++ {
			rel := fmt.Sprintf("%s/seg-%d.m4s", rep.id, seg)
			length := b.SegmentFiles[rel]
			dur := b.segmentDurationTicks(rep.timescale, opts)
			fmt.Fprintf(&sb,
				"          <Segment n=\"%d\" t=\"%d\" d=\"%d\" byteOffset=\"%d\" byteLength=\"%d\"/>\n",
				seg, tickCursor, dur, 0, length)
			tickCursor += dur
		}
		sb.WriteString("        </SegmentList>\n")
		sb.WriteString("      </Representation>\n")
		sb.WriteString("    </AdaptationSet>\n")
	}
	sb.WriteString("  </Period>\n</MPD>\n")
	return []byte(sb.String())
}

// segmentDurationTicks returns the per-segment tick span declared to DASH.
func (b *Bundle) segmentDurationTicks(ts uint32, opts Options) int64 {
	vfrDurs := []uint32{2700, 3003, 3300, 3003}
	var sum int64
	for k := 0; k < opts.FramesPerSeg; k++ {
		if ts == audioTS {
			vidDur := vfrDurs[k%len(vfrDurs)]
			sum += int64(vidDur) * int64(ts) / int64(videoTS)
		} else {
			sum += int64(vfrDurs[k%len(vfrDurs)])
		}
	}
	return sum
}

// buildHLS emits a media playlist with two discontinuities, exercising the
// parser's local-clock reset and isolated first-segment handling.
func (b *Bundle) buildHLS(opts Options) []byte {
	var sb strings.Builder
	sb.WriteString("#EXTM3U\n")
	sb.WriteString("#EXT-X-VERSION:6\n")
	sb.WriteString("#EXT-X-TARGETDURATION:2\n")
	fmt.Fprintf(&sb, "#EXT-X-MEDIA-SEQUENCE:1\n")
	for seg := 1; seg <= opts.Segments; seg++ {
		if seg == 2 || seg == 4 {
			sb.WriteString("#EXT-X-DISCONTINUITY\n")
		}
		dur := float64(b.segmentDurationTicks(videoTS, opts)) /
			float64(videoTS)
		fmt.Fprintf(&sb, "#EXTINF:%.3f,\n", dur)
		fmt.Fprintf(&sb, "%s/seg-%d.m4s\n", RepVideoHi, seg)
	}
	sb.WriteString("#EXT-X-ENDLIST\n")
	return []byte(sb.String())
}
