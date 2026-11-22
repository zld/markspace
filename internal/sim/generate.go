// Package sim generates deterministic synthetic RTP/MP4 fixtures and drives
// them through the whole media kernel so the CLI and the acceptance tests
// share one reproducible end-to-end path.
package sim

import (
	"math"
	"math/rand"

	"github.com/pion/rtp/v2"
)

// Config parameterises one deterministic simulation.
type Config struct {
	Seed      int64
	DurationS int64 // simulated media seconds
	LossBurst int   // max consecutive audio packets dropped (<=3)
	DriftPPM  int   // sender clock drift in parts per million
	ChunkMS   int   // processing chunk size in milliseconds
}

// AudioClockRate and packet geometry used by the synthetic sender.
const (
	AudioClockRate            = 48000
	AudioSamplesPerPkt        = 480 // 10 ms
	VideoClockRate            = 90000
	VideoTicksPerFrame        = 3000 // 30 fps
	AudioSSRC          uint32 = 0xA1A1A1A1
	VideoSSRC          uint32 = 0xB2B2B2B2
)

// RawPacket is one generated datagram plus its reference timeline metadata.
type RawPacket struct {
	Data    []byte
	IsVideo bool
	RefPTS  int64 // ideal presentation time, nanoseconds
	IsKey   bool
	Corrupt bool
	Seq     uint16
}

// Sender emits a deterministic audio+video RTP stream with timestamp/sequence
// wrap, linear clock drift, bounded reordering and bounded loss bursts.
type Sender struct {
	cfg      Config
	rng      *rand.Rand
	aSeq     uint16
	vSeq     uint16
	aTS      uint32
	vTS      uint32
	aSeqBase uint16 // raw seq assigned to the first audio packet
	// RefByExpandedSeq maps the unwrapped audio sequence onto its reference
	// PTS, including packets that will later be dropped, so recovery can place
	// silence fills on the timeline.
	RefByExpandedSeq map[int64]int64
}

// NewSender builds a seeded sender.
func NewSender(cfg Config) *Sender {
	if cfg.LossBurst > 3 {
		cfg.LossBurst = 3
	}
	if cfg.ChunkMS <= 0 {
		cfg.ChunkMS = 20
	}
	// start both clocks close to the 32-bit boundary to force wraps
	s := &Sender{
		cfg:              cfg,
		rng:              rand.New(rand.NewSource(cfg.Seed)),
		aSeq:             math.MaxUint16 - 8,
		vSeq:             math.MaxUint16 - 4,
		aTS:              math.MaxUint32 - uint32(AudioClockRate/5),
		vTS:              math.MaxUint32 - uint32(VideoClockRate/5),
		RefByExpandedSeq: map[int64]int64{},
	}
	s.aSeqBase = math.MaxUint16 - 8
	return s
}

func marshal(ssrc uint32, pt uint8, seq uint16, ts uint32, marker bool, payload []byte) []byte {
	p := &rtp.Packet{
		Header: rtp.Header{
			Version: 2, PayloadType: pt, SequenceNumber: seq,
			Timestamp: ts, SSRC: ssrc, Marker: marker,
		},
		Payload: payload,
	}
	raw, err := p.Marshal()
	if err != nil {
		panic(err)
	}
	return raw
}

// driftTick applies the linear ppm drift to an ideal clock increment. The
// drift is retained at the full integer tick resolution (no per-packet
// nanosecond rounding) so it remains measurable by the drift estimator.
func (s *Sender) driftTick(ideal uint32) uint32 {
	return ideal
}

// observedTS returns the drifted sender timestamp for the n-th packet on a
// clock of the given rate, computed as one accumulated value so the ppm offset
// grows with elapsed time instead of being rounded away per packet.
func (s *Sender) observedTS(n int, idealStep, rate uint32) uint32 {
	idealTotal := uint64(n) * uint64(idealStep)
	drifted := idealTotal + idealTotal*uint64(s.cfg.DriftPPM)/1_000_000
	return uint32(drifted)
}

// Generate builds a time-interleaved audio+video packet list (audio every
// 10 ms, video every 33.3 ms), then applies deterministic transport
// impairments while preserving enough structure for the bounded reorder
// window to recover.
func (s *Sender) Generate() []RawPacket {
	audioPkts := int(s.cfg.DurationS * 100) // 10 ms each
	videoPkts := int(s.cfg.DurationS * 30)

	// Pre-decide which audio packet indexes are dropped, in bursts of at most
	// cfg.LossBurst, spaced well beyond the reorder window.
	dropped := s.planAudioLoss(audioPkts)

	var pkts []RawPacket
	ai, vi := 0, 0
	for ai < audioPkts || vi < videoPkts {
		audioPTS := int64(ai) * 10_000_000
		videoPTS := int64(vi) * 33_333_333
		if vi >= videoPkts || (ai < audioPkts && audioPTS <= videoPTS) {
			pkts = append(pkts, s.makeAudio(ai, dropped[ai]))
			ai++
		} else {
			pkts = append(pkts, s.makeVideo(vi))
			vi++
		}
	}
	s.impair(&pkts)
	return pkts
}

// planAudioLoss returns a per-audio-index drop flag map.
func (s *Sender) planAudioLoss(audioPkts int) map[int]bool {
	drop := map[int]bool{}
	if s.cfg.LossBurst == 0 {
		return drop
	}
	bursts := int(s.cfg.DurationS) // about one burst per simulated second
	spacing := reorderSafeSpacing
	for b := 0; b < bursts; b++ {
		start := 40 + b*spacing + s.rng.Intn(spacing/2)
		for j := 0; j < s.cfg.LossBurst && start+j < audioPkts-40; j++ {
			drop[start+j] = true
		}
	}
	return drop
}

// reorderSafeSpacing keeps loss bursts farther apart than the reorder window.
const reorderSafeSpacing = 100

func (s *Sender) makeAudio(i int, isDropped bool) RawPacket {
	refPTS := int64(i) * 10_000_000
	payload := s.tonePayload(i)
	s.aSeq++
	// expanded sequence starts at the raw value of the first packet, matching
	// clock.Unwrapper's first-value anchoring.
	expanded := int64(s.aSeqBase) + 1 + int64(i)
	s.RefByExpandedSeq[expanded] = refPTS
	s.aTS = s.observedTS(i+1, AudioSamplesPerPkt, AudioClockRate)
	rp := RawPacket{
		Data:   marshal(AudioSSRC, 96, s.aSeq, s.aTS, true, payload),
		RefPTS: refPTS, Seq: s.aSeq,
	}
	if isDropped {
		rp.Data = nil
	}
	return rp
}

func (s *Sender) makeVideo(i int) RawPacket {
	refPTS := int64(i) * 33_333_333
	isKey := i%30 == 0 // key frame once per second
	s.vSeq++
	s.vTS = s.observedTS(i+1, VideoTicksPerFrame, VideoClockRate)
	return RawPacket{
		Data:    marshal(VideoSSRC, 97, s.vSeq, s.vTS, isKey, []byte{byte(i), byte(i >> 8)}),
		IsVideo: true, RefPTS: refPTS, IsKey: isKey, Seq: s.vSeq,
	}
}

// tonePayload synthesises a deterministic 10 ms PCM16 mono frame. The values
// depend only on the packet index and seed, so two runs match byte-for-byte.
func (s *Sender) tonePayload(i int) []byte {
	out := make([]byte, AudioSamplesPerPkt*2)
	for k := 0; k < AudioSamplesPerPkt; k++ {
		phase := 2 * math.Pi * 440 * float64(i*AudioSamplesPerPkt+k) / AudioClockRate
		v := int16(12000 * math.Sin(phase))
		out[2*k] = byte(v)
		out[2*k+1] = byte(v >> 8)
	}
	return out
}

// impair applies bounded same-track reordering and rare corruption. Packet
// loss was already decided in planAudioLoss. Every decision draws from the
// seeded RNG in a fixed order so the stream is reproducible.
func (s *Sender) impair(pkts *[]RawPacket) {
	p := *pkts
	// Bounded reordering: swap adjacent packets of the same kind, always well
	// inside the reorder window and never involving a dropped slot.
	for swaps := 0; swaps < int(s.cfg.DurationS)*2; swaps++ {
		i := s.rng.Intn(len(p) - 5)
		j := i + 1 + s.rng.Intn(3)
		if p[i].IsVideo == p[j].IsVideo && p[i].Data != nil && p[j].Data != nil {
			p[i], p[j] = p[j], p[i]
		}
	}
	// Rare, explicitly corrupt datagrams (truncated header).
	for c := 0; c < 3 && len(p) > 100; c++ {
		i := s.rng.Intn(len(p) - 1)
		if p[i].Data != nil {
			p[i].Data = p[i].Data[:3]
			p[i].Corrupt = true
		}
	}
	*pkts = p
}
