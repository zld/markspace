package pipeline

import (
	"fmt"
	"strings"

	"github.com/pion/rtp"

	"mediadist/media"
	"mediadist/timeline"
)

// PacketKind categorizes synthetic RTP arrival behavior.
type PacketKind int

const (
	KindNormal PacketKind = iota
	KindDuplicate
	KindReordered
	KindRecoverableLate
	KindPermanentLoss
)

// PlannedPacket is one entry of the deterministic arrival plan.
type PlannedPacket struct {
	Packet *rtp.Packet
	Kind   PacketKind
}

const (
	fixturePackets = 400
	segmentCount   = 6
	ssrc           = 0xC0FFEE
)

var repSpecs = []struct {
	id        string
	bandwidth int
	timescale int64
	jitterMs  int64
}{
	{"rep-audio-64k", 64000, 48000, 0},
	{"rep-video-1m", 1000000, 30000, 4},
	{"rep-video-3m", 3000000, 90000, 8},
}

// GenerateFixtures builds all local, self-contained demo inputs from the
// config. The RNG is seeded from the config so reruns are identical.
func GenerateFixtures(cfg Config) ([]byte, []byte, []PlannedPacket, []media.ColorMeta, error) {
	if cfg.CorruptScale == 0 {
		return nil, nil, nil, nil, zeroTimescaleError()
	}
	specs := repSpecs
	if cfg.Rates < len(specs) {
		specs = specs[:cfg.Rates]
	}
	mpd := buildMPD(specs)
	m3u8 := buildM3U8()
	packets := buildRTPPlan(cfg)
	colors := buildColorMetas()
	return []byte(mpd), []byte(m3u8), packets, colors, nil
}

func zeroTimescaleError() error {
	return timeline.NewError("INVALID_TIMESCALE",
		"manifest declares a zero or non-positive timescale",
		map[string]any{"timescale": 0})
}

func buildMPD(specs []struct {
	id        string
	bandwidth int
	timescale int64
	jitterMs  int64
}) string {
	var b strings.Builder
	b.WriteString("MPD media-presentation-duration-ms=12000\n")
	for ri, s := range specs {
		b.WriteString(fmt.Sprintf("REP id=%s bandwidth=%d timescale=%d\n",
			s.id, s.bandwidth, s.timescale))
		for i := 0; i < segmentCount; i++ {
			startMs := int64(i*2000) + s.jitterMs
			endMs := int64((i+1)*2000) + s.jitterMs
			start := msToScale(startMs, s.timescale)
			dur := msToScale(endMs, s.timescale) - start
			b.WriteString(fmt.Sprintf("SEG start=%d duration=%d uri=%s-%d.m4s\n",
				start, dur, s.id, i))
		}
		_ = ri
	}
	return b.String()
}

func msToScale(ms, timescale int64) int64 {
	return ms * timescale / 1000
}

func buildM3U8() string {
	lines := []string{
		"#EXTM3U",
		"#EXT-X-TARGETDURATION:2",
		"#EXT-X-MEDIA-SEQUENCE:500",
	}
	for i := 0; i < segmentCount; i++ {
		if i == 3 {
			lines = append(lines, "#EXT-X-DISCONTINUITY")
		}
		lines = append(lines,
			fmt.Sprintf("#EXTINF:2.0,"),
			fmt.Sprintf("edge-seg-%d.ts", 500+i))
	}
	return strings.Join(lines, "\n") + "\n"
}

func buildRTPPlan(cfg Config) []PlannedPacket {
	lateIndexes := map[int]bool{}
	permanent := fixturePackets - 3
	for i := 20; i <= 380; i += 20 {
		lateIndexes[i] = true // 19 deterministic recoverable gaps
	}
	mk := func(i int) PlannedPacket {
		seq := uint16(uint64(65530) + uint64(i)) // forces early 16-bit wraparound
		return PlannedPacket{
			Packet: &rtp.Packet{
				Header: rtp.Header{
					Version: 2, PayloadType: 96, SSRC: ssrc,
					SequenceNumber: seq, Timestamp: uint32(i * 3000),
				},
				Payload: []byte{byte(i), byte(i >> 8)},
			},
			Kind: KindNormal,
		}
	}
	var ordered []PlannedPacket
	// Process 40-packet chunks: first skip the late/permanent indexes so
	// the jitter buffer opens holes and advances, then fill the late
	// holes while they are younger than the window of 64.
	for start := 0; start < fixturePackets; start += 40 {
		end := start + 40
		if end > fixturePackets {
			end = fixturePackets
		}
		var held []PlannedPacket
		for i := start; i < end; i++ {
			switch {
			case i == permanent:
				ordered = append(ordered,
					PlannedPacket{Packet: mk(i).Packet, Kind: KindPermanentLoss})
			case lateIndexes[i]:
				held = append(held,
					PlannedPacket{Packet: mk(i).Packet, Kind: KindRecoverableLate})
			case i != 0 && i%50 == 0:
				ordered = append(ordered, mk(i),
					PlannedPacket{Packet: mk(i).Packet, Kind: KindDuplicate})
			default:
				ordered = append(ordered, mk(i))
			}
		}
		ordered = append(ordered, held...)
	}
	return ordered
}

func buildColorMetas() []media.ColorMeta {
	return []media.ColorMeta{
		{Source: "origin", Primaries: "bt2020", Transfer: "smpte2084",
			MatrixCoeffs: "bt2020nc", Range: "tv"},
		{Source: "edge-a", Primaries: "bt2020", Transfer: "bt709", Range: "tv"},
		{Source: "edge-b", Primaries: "bt709", Transfer: "smpte2084"},
	}
}
