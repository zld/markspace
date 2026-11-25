// Package audio detects silence regions from normalized payload energy.
// Because the implementation boundary excludes arbitrary codec decoding,
// the level is the deterministic mean absolute amplitude of the packet
// payload bytes: the fixtures emit zero bytes for silence and non-zero tone
// bytes otherwise, which any PCM-style or synthetic payload preserves.
package audio

import (
	"rtpreplay/ingest"
	"rtpreplay/media"
	"rtpreplay/pipeline"
)

// DefaultLevelThreshold is the mean absolute byte level at or below which a
// packet counts as silent.
const DefaultLevelThreshold = 4.0

// SilenceRegion is a maximal run of consecutive silent packets.
type SilenceRegion struct {
	StartMS     float64 `json:"start_ms"`
	EndMS       float64 `json:"end_ms"`
	Threshold   float64 `json:"threshold"`
	StartRecord int     `json:"start_record"`
	EndRecord   int     `json:"end_record"`
	Packets     int     `json:"packets"`
}

// Level returns the mean absolute byte amplitude of one RTP payload.
func Level(payload []byte) float64 {
	if len(payload) == 0 {
		return 0
	}
	var sum uint64
	for _, b := range payload {
		// Absolute amplitude for signed PCM-style bytes.
		v := int(b)
		if v > 127 {
			v = 256 - v
		}
		sum += uint64(v)
	}
	return float64(sum) / float64(len(payload))
}

// DetectSilence scans the representation's observed packets and merges
// consecutive silent packets into regions. Recovered (synthesized) packets
// have no payload and are skipped rather than counted as evidence.
func DetectSilence(rep *pipeline.Representation, threshold float64) []SilenceRegion {
	if threshold < 0 {
		threshold = DefaultLevelThreshold
	}
	pkts := rep.Stream.Packets
	if len(pkts) == 0 {
		return nil
	}
	rate := rep.Stream.ClockRate
	baseMS := media.TicksToMS(pkts[0].Clock.Unwrapped, rate)
	relMS := func(p *ingest.Packet) float64 {
		return media.TicksToMS(p.Clock.Unwrapped, rate) - baseMS
	}

	var regions []SilenceRegion
	var start *ingest.Packet
	var last *ingest.Packet
	count := 0
	flush := func() {
		if start == nil {
			return
		}
		regions = append(regions, SilenceRegion{
			StartMS:     relMS(start),
			EndMS:       relMS(last),
			Threshold:   threshold,
			StartRecord: start.RecordIndex,
			EndRecord:   last.RecordIndex,
			Packets:     count,
		})
		start, last, count = nil, nil, 0
	}

	for _, p := range pkts {
		if p.Clock.Isolated || p.RTP == nil || len(p.RTP.Payload) == 0 {
			flush() // an evidence gap closes the current region
			continue
		}
		if Level(p.RTP.Payload) <= threshold {
			if start == nil {
				start = p
			}
			last = p
			count++
		} else {
			flush()
		}
	}
	flush()
	return regions
}
