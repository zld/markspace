package rtp

import (
	"math/rand"

	"rtptrace/internal/clock"
	"rtptrace/internal/container"
	"rtptrace/internal/fault"
)

// RTPClockHZ is the reference RTP clock rate applied to every track after
// scaling from its own timescale.
const RTPClockHZ = uint32(90_000)

// TraceConfig parameterizes one deterministic weak-network trace.
type TraceConfig struct {
	WindowCapacity int
	// ReorderEvery>0 swaps every nth delivered packet with its successor.
	ReorderEvery int
	// DuplicateEvery>0 re-delivers every nth packet once.
	DuplicateEvery int
	// BurstLoss is an optional list of 0-based delivery indices to drop.
	BurstLoss []int
	// MaxSeq optionally caps the generated sequence space (0 = no cap).
	StartSeq uint16
}

// BuildPackets maps every sample of every track to an RTP packet. The RTP
// timestamp comes from the track timescale via exact rational scaling, so
// bandwidth shaping cannot alter media time.
func BuildPackets(c *container.Container) ([]*Packet, error) {
	var out []*Packet
	for ti, tr := range c.Tracks {
		ssrc := uint32(0x4000 + ti)
		pt := uint8(96 + ti)
		rate := clock.Rational{Num: 1, Den: uint64(tr.Timescale)}
		for _, s := range tr.Samples {
			ts, err := clock.ToRTPTimestamp(
				clock.Duration{Value: s.DTS, Rate: rate}, RTPClockHZ, "stts.dts")
			if err != nil {
				return nil, err.(*fault.Error).WithTrack(ti)
			}
			p := &Packet{
				SSRC:           ssrc,
				SequenceNumber: uint16(s.Index),
				Timestamp:      ts,
				PayloadType:    pt,
				Marker:         s.Sync,
				Payload:        []byte{byte(ti), byte(s.Index), byte(s.Index >> 8), byte(s.DTS)},
			}
			if err := Encode(p); err != nil {
				return nil, err.(*fault.Error).WithTrack(ti)
			}
			out = append(out, p)
		}
	}
	return out, nil
}

// Simulate applies the deterministic weak-network effects to an ordered packet
// stream and returns the delivery order actually observed by the receiver.
// The rng makes every effect reproducible from a fixed seed.
func Simulate(ordered []*Packet, cfg TraceConfig, seed int64) []*Packet {
	rng := rand.New(rand.NewSource(seed))
	loss := map[int]bool{}
	for _, idx := range cfg.BurstLoss {
		loss[idx] = true
	}
	var delivered []*Packet
	for i, p := range ordered {
		if loss[i] {
			continue // burst loss outside the window: never delivered
		}
		cp := clonePacket(p)
		cp.SequenceNumber = cfg.StartSeq + p.SequenceNumber
		delivered = append(delivered, cp)
		if cfg.DuplicateEvery > 0 && (i+1)%cfg.DuplicateEvery == 0 {
			delivered = append(delivered, clonePacket(cp))
		}
	}
	if cfg.ReorderEvery > 0 {
		for i := 0; i+1 < len(delivered); i++ {
			if (i+1)%cfg.ReorderEvery == 0 && rng.Intn(2) == 0 {
				delivered[i], delivered[i+1] = delivered[i+1], delivered[i]
				i++ // skip the swapped partner
			}
		}
	}
	return delivered
}

func clonePacket(p *Packet) *Packet {
	cp := *p
	cp.Payload = append([]byte(nil), p.Payload...)
	cp.Raw = append([]byte(nil), p.Raw...)
	return &cp
}

// Reassemble runs the delivered stream through per-SSRC reorder windows and
// returns the in-order accepted packets, aggregate stats and loss gaps.
func Reassemble(delivered []*Packet, capacity int) ([]*Packet, Stats, []Gap, error) {
	windows := map[uint32]*Window{}
	var order []uint32
	var emitted []*Packet
	for _, p := range delivered {
		w, ok := windows[p.SSRC]
		if !ok {
			var err error
			w, err = NewWindow(p.SSRC, capacity)
			if err != nil {
				return nil, Stats{}, nil, err
			}
			windows[p.SSRC] = w
			order = append(order, p.SSRC)
		}
		if err := w.PushInto(p, &emitted); err != nil {
			return nil, Stats{}, nil, err
		}
	}
	var agg Stats
	var gaps []Gap
	for _, ssrc := range order {
		st, g := windows[ssrc].Flush()
		agg.Received += st.Received
		agg.Duplicates += st.Duplicates
		agg.LostPackets += st.LostPackets
		agg.Reordered += st.Reordered
		agg.Emitted += st.Emitted
		gaps = append(gaps, g...)
	}
	return emitted, agg, gaps, nil
}
