package scenario

import (
	"fmt"
	"math/rand"

	pionrtp "github.com/pion/rtp"

	"github.com/zld/avpipe/internal/clock"
	"github.com/zld/avpipe/internal/metadata"
	"github.com/zld/avpipe/internal/mp4"
	"github.com/zld/avpipe/internal/rtp"
)

type generatedRep struct {
	id   string
	data []byte
}

func generateRTPArrivals(cfg Config) []*rtp.Packet {
	rng := rand.New(rand.NewSource(cfg.Seed))
	n := cfg.Packets
	lost := make(map[int]bool)
	for i := 0; i < n; i++ {
		if rng.Float64() < cfg.Loss {
			lost[i] = true
		}
	}

	var live []*rtp.Packet
	for seq := 0; seq < n; seq++ {
		if lost[seq] {
			continue
		}
		live = append(live, &rtp.Packet{
			Header: pionrtp.Header{
				SequenceNumber: uint16(seq),
				Timestamp:      uint32(seq * 3000), // 30 fps in 90 kHz media clock
				SSRC:           0x415650,
			},
			Payload: []byte(fmt.Sprintf("rtp-seq-%04d-seed-%d", seq, cfg.Seed)),
		})
	}

	// Deterministic local reordering: swap packets two positions apart so a
	// later arrival fills a real sequence gap inside the recovery window.
	for i := 4; i+2 < len(live); i += 9 {
		live[i], live[i+2] = live[i+2], live[i]
	}
	if len(live) > 2 {
		dup := *live[1]
		live = append(live, &dup)
	}
	return live
}

func generateMetadataEvents(cfg Config) []metadata.Event {
	var srt string
	var raw []metadata.Event
	for i := 1; i <= cfg.Segments; i++ {
		startMS := int64(i-1)*2000 + 250
		endMS := startMS + 1500
		text := fmt.Sprintf("avpipe subtitle %d", i)
		srt += fmt.Sprintf("%d\n%s --> %s\n%s\n\n", i, formatSRTTime(startMS), formatSRTTime(endMS), text)
		end := clock.NewTimestamp(clock.Ticks(endMS), 1000)
		raw = append(raw, metadata.Event{
			Track: "subtitle", Kind: "srt-source",
			Start: clock.NewTimestamp(clock.Ticks(startMS), 1000), End: &end, Text: text,
		})
	}

	events, err := metadata.ParseSRT([]byte(srt))
	if err != nil {
		panic(fmt.Sprintf("generated SRT must parse: %v", err))
	}

	binaryEvents, err := metadata.ParseTimedRecords(metadata.BuildTimedRecords(raw))
	if err != nil {
		panic(fmt.Sprintf("generated timed records must parse: %v", err))
	}
	events = append(events, binaryEvents...)

	id3Event := metadata.Event{
		Track: "id3", Kind: "chapter",
		Start: clock.NewTimestamp(2000, 1000), Text: "chapter-boundary",
	}
	id3Events, err := metadata.ParseID3Priv(metadata.BuildID3Priv(id3Event))
	if err != nil {
		panic(fmt.Sprintf("generated ID3 must parse: %v", err))
	}
	events = append(events, id3Events...)
	events = append(events, id3Events...) // deliberate repeated event for dedupe

	seiEnd := clock.NewTimestamp(3250, 1000)
	seiEvent := metadata.Event{
		Track: "sei", Kind: "cue",
		Start: clock.NewTimestamp(2250, 1000), End: &seiEnd, Text: "sei-boundary-marker",
	}
	seiEvents, err := metadata.ParseSEI(metadata.BuildSEI(seiEvent))
	if err != nil {
		panic(fmt.Sprintf("generated SEI must parse: %v", err))
	}
	return append(events, seiEvents...)
}

func generateMP4Data(cfg Config) []generatedRep {
	scales := []uint32{90000, 48000, 1000}
	out := make([]generatedRep, 0, cfg.Representations)
	for rep := 0; rep < cfg.Representations; rep++ {
		scale := scales[rep%len(scales)]
		id := fmt.Sprintf("rep-%d-%s", rep, bandwidthName(rep))
		spec := mp4.RepresentationSpec{ID: id, Timescale: scale, IncludeSidx: !cfg.MissingIndex}
		for i := 1; i <= cfg.Segments; i++ {
			duration := uint32(2 * int64(scale))
			start := int64(i-1) * int64(duration)
			seg := mp4.SegmentSpec{
				Sequence:      uint32(i),
				StartTicks:    start,
				DurationTicks: duration,
				Timescale:     scale,
				Samples: []mp4.SampleSpec{{
					Data:     []byte(fmt.Sprintf("%s-segment-%d-local-sample", id, i)),
					Duration: duration,
				}},
			}
			if i == 1 && rep == 0 {
				seg.EMSG = &mp4.EMSGEvent{
					SchemeIDURI: "urn:avpipe:timed",
					Timescale:   scale,
					PTS:         uint64(start),
					Duration:    duration,
					ID:          uint32(i),
					Message:     []byte(fmt.Sprintf("emsg-%d", i)),
				}
			}
			spec.Segments = append(spec.Segments, seg)
		}
		data, err := mp4.BuildRepresentation(spec)
		if err != nil {
			panic(fmt.Sprintf("generated MP4 must build: %v", err))
		}
		out = append(out, generatedRep{id: id, data: data})
	}
	return out
}

func bandwidthName(i int) string {
	names := []string{"low", "mid", "high", "xhigh"}
	if i < len(names) {
		return names[i]
	}
	return fmt.Sprintf("br%d", i)
}

func formatSRTTime(ms int64) string {
	h := ms / 3_600_000
	ms %= 3_600_000
	m := ms / 60_000
	ms %= 60_000
	s := ms / 1000
	rem := ms % 1000
	return fmt.Sprintf("%02d:%02d:%02d,%03d", h, m, s, rem)
}
