package scenario

import (
	"context"
	"time"

	"github.com/pion/rtp"

	"relaylab/internal/peer"
	"relaylab/internal/signaling"
)

// generateTraffic builds the deterministic packet stream and applies the
// injector decisions (delay, reorder, duplication). A steady pacing ticker
// guarantees packets arrive throughout the run, so consumer-side backpressure
// windows actually fill the bounded queue and exercise its overflow policy.
func (e *Engine) generateTraffic(ctx context.Context, pcA *peer.Peer) error {
	var pending []*rtp.Packet // held for one-step reorder swaps
	// Spread the packets across 80% of the configured duration so the run
	// approaches --duration while reserving headroom for burst delays,
	// shutdown draining and negotiation. Steady pacing also keeps arrivals
	// flowing during the relay's backpressure windows.
	pace := time.Duration(int64(e.cfg.Duration) * 8 / 10 / int64(e.cfg.Packets))
	if pace <= 0 {
		pace = 500 * time.Microsecond
	}
	ticker := time.NewTicker(pace)
	defer ticker.Stop()
	// RTP timestamp cadence stays 200 pps regardless of wall-clock send
	// speed, keeping the clock-rate math meaningful for the jitter metric.
	const ticksPerPacket = int64(defaultClockRate) / 200
	baseTicks := e.clockTicks(time.Now())

	for i := 0; i < e.cfg.Packets; i++ {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
		pkt := e.makePacket(uint16(i), uint32(baseTicks+int64(i)*ticksPerPacket))
		decision := e.inj.Plan()
		if err := decision.Sleep(ctx); err != nil {
			return nil
		}
		if decision.Reorder {
			pending = append(pending, pkt)
			if len(pending) == 2 {
				pending[0], pending[1] = pending[1], pending[0]
				for _, p := range pending {
					if err := e.sendPacket(ctx, p, pcA); err != nil {
						return err
					}
				}
				pending = nil
			}
			continue
		}
		if len(pending) == 1 {
			if err := e.sendPacket(ctx, pending[0], pcA); err != nil {
				return err
			}
			pending = nil
		}
		if err := e.sendPacket(ctx, pkt, pcA); err != nil {
			return err
		}
		if decision.Duplicate {
			if err := e.sendPacket(ctx, pkt, pcA); err != nil {
				return err
			}
		}
	}
	for _, p := range pending {
		_ = e.sendPacket(ctx, p, pcA)
	}
	return nil
}

// sendPacket writes one deterministic packet onto the sender's local track.
func (e *Engine) sendPacket(ctx context.Context, pkt *rtp.Packet, pcA *peer.Peer) error {
	if err := pcA.WriteRTP(pkt); err != nil {
		if ctx.Err() != nil {
			return nil
		}
		return phaseErr("transport", err)
	}
	e.mu.Lock()
	e.sent++
	e.mu.Unlock()
	return nil
}

// makePacket produces a deterministic payload derived from seed + sequence.
func (e *Engine) makePacket(seq uint16, ts uint32) *rtp.Packet {
	payload := make([]byte, 12)
	for i := range payload {
		payload[i] = byte(uint64(e.cfg.Seed) + uint64(seq)*7 + uint64(i))
	}
	return &rtp.Packet{
		Header: rtp.Header{
			Version:        2,
			PayloadType:    0,
			SequenceNumber: seq,
			Timestamp:      ts,
			SSRC:           0x524C4142, // "RLAB"
			Marker:         seq%16 == 0,
		},
		Payload: payload,
	}
}

func (e *Engine) clockTicks(t time.Time) int64 {
	return t.UnixNano() * int64(e.cfg.ClockRate) / int64(time.Second)
}

// phaseErr keeps traffic errors on the same phase taxonomy as the engine.
func phaseErr(phase string, err error) error {
	return &signaling.PhaseError{Phase: phase, Err: err}
}
