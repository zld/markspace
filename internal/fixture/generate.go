// Package testdata generates deterministic, self-contained broadcast
// fixtures: multi-representation DASH MPDs, discontinuous HLS playlists and
// RTP packet streams carrying five fault classes (duplicates, reorder, burst
// loss, sustained bandwidth change, timestamp wrap). No external files are
// referenced; every byte derives from the integer seed so reruns are
// identical and auditable.
package fixture

import (
	"fmt"
	"math/rand"

	"github.com/pion/rtp"
)

// Case names select a fixture scenario.
const (
	CaseNormal    = "normal"
	CaseDamaged   = "damaged"
	CaseBoundary  = "boundary"
	CaseCancel    = "cancel"
	CaseDuplicate = "duplicate"
	CaseReorder   = "reorder"
	CaseBurstLoss = "burst_loss"
	CaseBandwidth = "bandwidth"
	CaseTSWrap    = "ts_wrap"
)

// Fixed fixture geometry, sized to stay well inside every hard limit.
const (
	SegmentsPerRep = 8
	SegmentMillis  = 1000
	VideoTimescale = 90000
	AudioTimescale = 48000
	VideoPacketsMs = 30 // one 30 fps frame packet per 33 ms
	AudioPacketsMs = 21 // ~48 AAC frames per second
	PayloadBytes   = 256
)

// FaultCase describes one generated RTP fault stream and the pipeline stage
// it primarily exercises.
type FaultCase struct {
	Name     string `json:"name"`
	Stage    string `json:"stage"`
	StreamID string `json:"stream_id"`
	Packets  []*rtp.Packet
	// Fault metadata for assertions.
	DuplicateSeqs []uint16 `json:"duplicate_seqs,omitempty"`
	LostSeqs      []uint16 `json:"lost_seqs,omitempty"`
	Reordered     bool     `json:"reordered,omitempty"`
	WrapsTS       bool     `json:"wraps_ts,omitempty"`
	BandwidthKbps []int    `json:"bandwidth_kbps,omitempty"`
	LossBurst     int      `json:"loss_burst,omitempty"`
}

// Fixture bundles the manifests and RTP streams for one scenario.
type Fixture struct {
	Name        string
	Seed        int64
	DASHMPD     string
	HLSPlaylist string
	HLSDamaged  string // playlist with a sequence gap and discontinuity
	Cases       []FaultCase
	// Bandwidth profile applied to pool pacing, in kbps over time.
	BandwidthKbps []int
	LossBurst     int
}

// Generator holds the deterministic RNG and sequence state.
type Generator struct {
	rng      *rand.Rand
	seed     int64
	videoSeq uint16
	audioSeq uint16
	videoTS  uint32
	audioTS  uint32
}

// NewGenerator seeds a deterministic generator.
func NewGenerator(seed int64) *Generator {
	return &Generator{
		rng:      rand.New(rand.NewSource(seed)),
		seed:     seed,
		videoSeq: 1000,
		audioSeq: 2000,
		// Start the video timestamp near the 32-bit boundary so the wrap case
		// can cross it within the fixed fixture duration.
		videoTS: 0xFFFFFFFF - uint32(SegmentsPerRep*SegmentMillis*VideoPacketsMs)/2,
		audioTS: 50000,
	}
}

// Build assembles every scenario for a seed.
func (g *Generator) Build(lossBurst int, bandwidthKbps []int) *Fixture {
	return &Fixture{
		Name:          "generated",
		Seed:          g.seed,
		DASHMPD:       g.buildDASH(),
		HLSPlaylist:   g.buildHLS(false),
		HLSDamaged:    g.buildHLS(true),
		Cases:         g.buildCases(lossBurst),
		BandwidthKbps: bandwidthKbps,
		LossBurst:     lossBurst,
	}
}

// basePackets builds a clean, in-order packet stream for one kind.
func (g *Generator) basePackets(kind string) []*rtp.Packet {
	perMs := VideoPacketsMs
	if kind == "audio" {
		perMs = AudioPacketsMs
	}
	total := SegmentsPerRep * SegmentMillis * perMs / 1000
	out := make([]*rtp.Packet, 0, total)
	for i := 0; i < total; i++ {
		var ssrc uint32 = 11
		var seq uint16
		var ts uint32
		if kind == "video" {
			ssrc = 11
			seq = g.videoSeq
			ts = g.videoTS
			g.videoSeq++
			g.videoTS += 3000 // 90 kHz / 30 fps
		} else {
			ssrc = 22
			seq = g.audioSeq
			ts = g.audioTS
			g.audioSeq++
			g.audioTS += 1024 // AAC frame duration at 48 kHz
		}
		pkt := &rtp.Packet{
			Header: rtp.Header{
				Version:        2,
				PayloadType:    96,
				SequenceNumber: seq,
				Timestamp:      ts,
				SSRC:           ssrc,
				Marker:         i%perMs == perMs-1,
			},
			Payload: g.payload(kind, i),
		}
		out = append(out, pkt)
	}
	return out
}

func (g *Generator) payload(kind string, i int) []byte {
	b := make([]byte, PayloadBytes)
	for j := range b {
		b[j] = byte(g.rng.Intn(256))
	}
	// Deterministic prefix identifies kind and ordinal.
	copy(b, []byte(fmt.Sprintf("%s:%06d:", kind, i)))
	return b
}

func (g *Generator) buildCases(lossBurst int) []FaultCase {
	video := g.basePackets("video")
	audio := g.basePackets("audio")
	cases := []FaultCase{
		g.normalCase(video, audio),
		g.duplicateCase(video),
		g.reorderCase(video),
		g.burstLossCase(video, lossBurst),
		g.bandwidthCase(audio),
		g.wrapCase(video),
	}
	return cases
}

func clone(in []*rtp.Packet) []*rtp.Packet {
	out := make([]*rtp.Packet, len(in))
	for i, p := range in {
		cp := *p
		out[i] = &cp
	}
	return out
}

func (g *Generator) normalCase(video, audio []*rtp.Packet) FaultCase {
	return FaultCase{
		Name: CaseNormal, Stage: "transport", StreamID: "video",
		Packets: clone(video),
	}
}

// duplicateCase re-injects a deterministic subset of packets.
func (g *Generator) duplicateCase(video []*rtp.Packet) FaultCase {
	out := clone(video)
	var dups []uint16
	idx := 5
	for idx < len(video)-2 && len(dups) < 6 {
		dup := *video[idx]
		out = insertAt(out, idx+1, &dup)
		dups = append(dups, video[idx].SequenceNumber)
		idx += 7
	}
	return FaultCase{
		Name: CaseDuplicate, Stage: "transport", StreamID: "video",
		Packets: out, DuplicateSeqs: dups,
	}
}

// reorderCase swaps adjacent packets within the reorder window.
func (g *Generator) reorderCase(video []*rtp.Packet) FaultCase {
	out := clone(video)
	for i := 3; i+1 < len(out); i += 9 {
		out[i], out[i+1] = out[i+1], out[i]
	}
	return FaultCase{
		Name: CaseReorder, Stage: "transport", StreamID: "video",
		Packets: out, Reordered: true,
	}
}

// burstLossCase removes a contiguous run and reports the lost sequences.
func (g *Generator) burstLossCase(video []*rtp.Packet, burst int) FaultCase {
	if burst <= 0 {
		burst = 8
	}
	start := 12
	if start+burst >= len(video) {
		start = len(video) / 2
	}
	out := make([]*rtp.Packet, 0, len(video)-burst)
	var lost []uint16
	for i, p := range video {
		if i >= start && i < start+burst {
			lost = append(lost, p.SequenceNumber)
			continue
		}
		out = append(out, p)
	}
	return FaultCase{
		Name: CaseBurstLoss, Stage: "transport", StreamID: "video",
		Packets: out, LostSeqs: lost, LossBurst: burst,
	}
}

// bandwidthCase keeps all packets but tags a pacing profile; the pool adjusts
// delivery rhythm without changing merged results.
func (g *Generator) bandwidthCase(audio []*rtp.Packet) FaultCase {
	return FaultCase{
		Name: CaseBandwidth, Stage: "transport", StreamID: "audio",
		Packets: clone(audio), BandwidthKbps: []int{900, 2500, 700},
	}
}

// wrapCase forces the video timestamp across the 32-bit boundary early.
func (g *Generator) wrapCase(video []*rtp.Packet) FaultCase {
	out := clone(video)
	for _, p := range out {
		if p.Timestamp > 0xFFFFFFF0 {
			// Natural wraparound already present from the base generator.
		}
	}
	return FaultCase{
		Name: CaseTSWrap, Stage: "alignment", StreamID: "video",
		Packets: out, WrapsTS: true,
	}
}

func insertAt(s []*rtp.Packet, at int, p *rtp.Packet) []*rtp.Packet {
	s = append(s, nil)
	copy(s[at+1:], s[at:])
	s[at] = p
	return s
}
