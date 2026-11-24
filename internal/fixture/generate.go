package fixture

import (
	"github.com/pion/rtp"

	"mediasync/internal/clock"
)

// rng is a deterministic LCG-style source so identical seeds reproduce bytes.
type rng struct {
	state uint64
}

func newRNG(seed int64) *rng { return &rng{state: uint64(seed)*6364136223846793005 + 1442695040888963407} }

func (r *rng) float64() float64 {
	r.state = r.state*2862933555777941757 + 3037000493
	// Use the high 53 bits for a value in [0,1).
	return float64(r.state>>11) / float64(uint64(1)<<53)
}

func (r *rng) byte() byte {
	r.state = r.state*2862933555777941757 + 3037000493
	return byte(r.state >> 56)
}

// Generate builds the full multi-rendition stream for a scenario.
func Generate(cfg Config, scenario Scenario) (*Stream, *clock.Error) {
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	stream := &Stream{Cfg: cfg, Scenario: scenario}
	for rend, bitrate := range cfg.TargetBitrates {
		rs, ground, err := cfg.generateRendition(rend, bitrate, scenario)
		if err != nil {
			return nil, err
		}
		stream.Renditions = append(stream.Renditions, rs)
		stream.Ground = append(stream.Ground, ground)
	}
	return stream, nil
}

func (c Config) validate() *clock.Error {
	if c.ClockRate == 0 || c.BaseFrameNS <= 0 || c.GOPFrames <= 0 {
		return clock.NewError(clock.ErrInvalidLength, "clock rate, frame period and GOP must be positive")
	}
	if c.LossRate < 0 || c.LossRate > 0.9 || c.TruncateRate < 0 || c.TruncateRate > 1 {
		return clock.NewError(clock.ErrInvalidLength, "impairment rates out of [0,1] bounds")
	}
	return nil
}

// generateRendition synthesizes one representation end to end.
func (c Config) generateRendition(rend int, bitrate int, scenario Scenario) (RenditionStream, []GroundFrame, *clock.Error) {
	r := newRNG(c.Seed + int64(rend)*1_000_003)
	ssrc := c.SSRCBase + uint32(rend)
	// Bytes per frame follows the target bitrate; keyframes are larger.
	bytesPerFrame := bitrate / 8 / int(1_000_000_000/c.BaseFrameNS)
	if bytesPerFrame < 8 {
		bytesPerFrame = 8
	}

	// Drift-aware device clock: RTP advances slightly faster/slower than wall.
	drift := c.DriftPPM
	if scenario == CaseWrap {
		if drift == 0 {
			drift = 250
		}
	}
	devicePerNS := (float64(c.ClockRate) / 1e9) * (1 + drift/1e6)
	startRTP := uint32(1000)
	if c.ForceWrap || scenario == CaseWrap {
		startRTP = uint32(clock.Uint32Mod - int64(c.ClockRate)*2) // two seconds from wrap
	}

	frameCount := int(c.DurationNS / c.BaseFrameNS)
	if frameCount > clock.MaxIndexEntries {
		return RenditionStream{}, nil, clock.NewError(clock.ErrOverflow, "frame count exceeds bound")
	}
	ground := make([]GroundFrame, 0, frameCount)
	var extRTP int64 = int64(startRTP)
	var wall int64
	gopOffset := 0
	if rend < len(c.KeyframeOffset) {
		gopOffset = c.KeyframeOffset[rend]
	}
	for f := 0; f < frameCount; f++ {
		// VFR: deterministic jitter around the nominal period.
		jitter := int64(r.float64()*2-1) * c.VFRAmplitude
		if f > 0 {
			wall += c.BaseFrameNS + jitter
			extRTP += int64(devicePerNS * float64(c.BaseFrameNS+jitter))
		}
		isKey := (f+gopOffset)%c.GOPFrames == 0
		size := bytesPerFrame
		if isKey {
			size = bytesPerFrame * 4
		}
		gf := GroundFrame{
			Rendition:   rend,
			FrameIndex:  f,
			Seq:         uint16(f),
			IsKeyframe:  isKey,
			RawRTP:      uint32(extRTP),
			ExtendedRTP: extRTP,
			WallNS:      wall,
			PayloadSize: size,
			PayloadHash: r.byte(),
		}
		ground = append(ground, gf)
	}

	dgs, reports := c.serializeAndImpair(r, ssrc, ground, scenario)
	return RenditionStream{SSRC: ssrc, Bitrate: bitrate, Datagrams: dgs, Reports: reports}, ground, nil
}

// serializeAndImpair turns ground frames into wire datagrams, then applies the
// scenario-specific loss, reorder, duplication, wrap and truncation effects.
func (c Config) serializeAndImpair(r *rng, ssrc uint32, ground []GroundFrame, scenario Scenario) ([]Datagram, []SenderReportBundle) {
	dgs := make([]Datagram, 0, len(ground))
	for _, gf := range ground {
		payload := make([]byte, gf.PayloadSize)
		payload[0] = gf.PayloadHash
		pkt := rtp.Packet{
			Header:  rtp.Header{Version: 2, PayloadType: 96, SequenceNumber: gf.Seq, Timestamp: gf.RawRTP, SSRC: ssrc},
			Payload: payload,
		}
		data, _ := pkt.Marshal()
		dg := Datagram{Rendition: gf.Rendition, Data: data, Seq: gf.Seq, SSRC: ssrc, RawRTP: gf.RawRTP, OrigOrder: gf.FrameIndex}
		dgs = append(dgs, dg)
	}

	lossRate, reorderRate, dupRate, truncRate := c.scenarioRates(scenario, r)
	dgs = applyLoss(dgs, r, lossRate)
	dgs = applyTruncation(dgs, r, truncRate)
	dgs = applyDuplicates(dgs, r, dupRate)
	dgs = applyReorder(dgs, r, reorderRate, c.ReorderWindow)

	reports := buildReports(ssrc, ground)
	return dgs, reports
}

func (c Config) scenarioRates(scenario Scenario, r *rng) (loss, reorder, dup, trunc float64) {
	switch scenario {
	case CaseLoss:
		return 0.08, 0.25, 0.03, 0
	case CaseWrap:
		return 0.05, 0.2, 0.02, 0
	case CaseCorrupt:
		return 0.06, 0.15, 0.02, 0.05
	default:
		return c.LossRate, c.ReorderRate, c.DuplicateRate, c.TruncateRate
	}
}

// buildReports emits one SR per second for drift fitting.
func buildReports(ssrc uint32, ground []GroundFrame) []SenderReportBundle {
	out := make([]SenderReportBundle, 0)
	if len(ground) == 0 {
		return out
	}
	lastSec := int64(-1)
	for _, gf := range ground {
		sec := gf.WallNS / 1_000_000_000
		if sec == lastSec {
			continue
		}
		lastSec = sec
		sr := encodeSR(ssrc, gf.WallNS, gf.RawRTP)
		out = append(out, SenderReportBundle{
			Rendition: gf.Rendition, Data: sr, WallNS: gf.WallNS, RTP: gf.RawRTP, ArrivalNS: gf.WallNS,
		})
	}
	return out
}
