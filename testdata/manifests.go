package testdata

import (
	"strings"

	"github.com/pion/rtp"
)

// buildDASH emits a two-bitrate video + one-audio MPD on the fixed segment
// grid, matching the parser's supported subset.
func (g *Generator) buildDASH() string {
	n := SegmentsPerRep
	var b strings.Builder
	b.WriteString(`<?xml version="1.0"?>` + "\n")
	b.WriteString(`<MPD xmlns="urn:mpeg:dash:schema:mpd:2011" mediaPresentationDuration="PT` +
		itoa(SegmentsPerRep) + `S">` + "\n  <Period>\n")
	// Video adaptation set with two representations.
	b.WriteString(`    <AdaptationSet contentType="video">` + "\n")
	b.WriteString(`      <SegmentTemplate timescale="90000" media="v$RepresentationID$-$Number$.m4s" ` +
		`initialization="v$RepresentationID$-init.mp4">` + "\n")
	b.WriteString(`        <SegmentTimeline>` + "\n")
	b.WriteString(`          <S t="0" d="90000" r="` + itoa(n-1) + `"/>` + "\n")
	b.WriteString(`        </SegmentTimeline>` + "\n      </SegmentTemplate>" + "\n")
	b.WriteString(`      <Representation id="v0" bandwidth="800000" codecs="avc1.640028"/>` + "\n")
	b.WriteString(`      <Representation id="v1" bandwidth="1500000" codecs="avc1.4d401f"/>` + "\n")
	b.WriteString(`    </AdaptationSet>` + "\n")
	// Audio adaptation set on the 48 kHz timescale, same segment grid.
	b.WriteString(`    <AdaptationSet contentType="audio">` + "\n")
	b.WriteString(`      <SegmentTemplate timescale="48000" media="a0-$Number$.m4s" ` +
		`initialization="a0-init.mp4">` + "\n")
	b.WriteString(`      <SegmentTimeline>` + "\n")
	b.WriteString(`      <S t="0" d="48000" r="` + itoa(n-1) + `"/>` + "\n")
	b.WriteString(`      </SegmentTimeline></SegmentTemplate>` + "\n")
	b.WriteString(`      <Representation id="a0" bandwidth="128000" codecs="mp4a.40.2"/>` + "\n")
	b.WriteString(`    </AdaptationSet>` + "\n  </Period>\n</MPD>\n")
	return b.String()
}

// buildHLS emits a media playlist; with damage=true it removes a segment
// reference inside the first group (so continuity detects and isolates it)
// and adds an explicit discontinuity before a later group.
func (g *Generator) buildHLS(damage bool) string {
	var b strings.Builder
	b.WriteString("#EXTM3U\n#EXT-X-VERSION:3\n#EXT-X-TARGETDURATION:1\n")
	b.WriteString("#EXT-X-MEDIA-SEQUENCE:0\n")
	for i := 0; i < SegmentsPerRep; i++ {
		// An explicit discontinuity splits the timeline before seg006, while
		// seg003 below is silently absent, opening a hole inside group 0.
		if damage && i == 6 {
			b.WriteString("#EXT-X-DISCONTINUITY\n")
		}
		if damage && i == 3 {
			continue // seg003 missing: sequence gap must be isolated, not spliced
		}
		b.WriteString("#EXTINF:1.0,\n")
		b.WriteString("seg" + pad3(i) + ".ts\n")
	}
	b.WriteString("#EXT-X-ENDLIST\n")
	return b.String()
}

// DamagedDASH returns an MPD with a zero timescale, exercising the parse-stage
// failure path (the cross-stage fault required by the acceptance criteria).
func (g *Generator) DamagedDASH() string {
	good := g.buildDASH()
	return strings.Replace(good, `timescale="90000"`, `timescale="0"`, 1)
}

// BoundaryDASH returns an MPD whose last segment ends exactly at the declared
// duration, an off-by-one boundary condition for the range validator.
func (g *Generator) BoundaryDASH() string {
	return g.buildDASH()
}

// WrapPackets returns a short packet stream engineered so the RTP timestamp
// crosses the 32-bit boundary on the second packet.
func (g *Generator) WrapPackets() []*rtp.Packet {
	pkts := []*rtp.Packet{
		{Header: rtpHeader(0, 0xFFFFFFFE, 11), Payload: []byte("w0")},
		{Header: rtpHeader(1, 0xFFFFFFFF, 11), Payload: []byte("w1")},
		{Header: rtpHeader(2, 0x00000000, 11), Payload: []byte("w2")},
		{Header: rtpHeader(3, 0x00000002, 11), Payload: []byte("w3")},
	}
	return pkts
}

func itoa(v int) string {
	if v == 0 {
		return "0"
	}
	neg := v < 0
	if neg {
		v = -v
	}
	var b []byte
	for v > 0 {
		b = append([]byte{byte('0' + v%10)}, b...)
		v /= 10
	}
	if neg {
		b = append([]byte{'-'}, b...)
	}
	return string(b)
}

func pad3(v int) string {
	s := itoa(v)
	for len(s) < 3 {
		s = "0" + s
	}
	return s
}
