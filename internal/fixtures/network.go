package fixtures

import (
	"fmt"
	"math"
	"math/rand"
	"sort"

	"mediapipe/internal/clock"
	"mediapipe/internal/rtp"
)

// FaultConfig selects which injected faults a run exercises. All faults are
// derived from the scenario seed, so runs are reproducible.
type FaultConfig struct {
	// ReorderPermille is the probability that a packet is delayed enough to
	// arrive after its successors.
	ReorderPermille int
	// DuplicatePermille is the probability of an extra duplicate copy.
	DuplicatePermille int
	// LossBurst is the length of a contiguous burst dropped per segment.
	LossBurst int
	// JitterMS bounds arrival jitter in milliseconds.
	JitterMS int
	// LateLossRatio is the fraction of lost packets retransmitted but after the
	// jitter hold, i.e. unrecoverable through the normal window.
	LateLateRatio int
}

// Delivered is one packet that reached the receiver, as serialized RTP bytes.
type Delivered struct {
	Wire    []byte
	Arrival int64
}

// Retransmit is one packet held back by the network for the recovery path.
// Index is the position in the scenario packet order, used to address extended
// sequence numbers.
type Retransmit struct {
	Wire   []byte
	Packet rtp.Packet
	Due    int64
	Index  int
	// Late marks a retransmission that the sender only produces after the
	// receiver's recovery deadline; it must never be treated as recovered.
	Late bool
}

// Transmission is the simulated edge network result.
type Transmission struct {
	Delivered  []Delivered
	Retransmit []Retransmit
	Lost       []RawPacket
	Expected   int
	Duplicates int
}

// Transmit encodes every scenario packet and applies the fault model.
func Transmit(sc *Scenario, cfg FaultConfig) (*Transmission, error) {
	if cfg.LossBurst < 0 || cfg.LossBurst > 32 {
		return nil, fmt.Errorf("fixtures: loss burst %d out of range", cfg.LossBurst)
	}
	if cfg.JitterMS < 0 || cfg.JitterMS > 2000 {
		return nil, fmt.Errorf("fixtures: jitter %d ms out of range", cfg.JitterMS)
	}
	for _, v := range []int{cfg.ReorderPermille, cfg.DuplicatePermille, cfg.LateLateRatio} {
		if v < 0 || v > 1000 {
			return nil, fmt.Errorf("fixtures: probability %d out of range", v)
		}
	}
	r := rand.New(rand.NewSource(sc.Seed*7919 + 13))
	tx := &Transmission{Expected: len(sc.Packets)}
	burstAt := make(map[int]bool)
	if cfg.LossBurst > 0 {
		// One burst per segment, positioned deterministically inside it.
		perSeg := len(sc.Packets) / Segments
		for s := 0; s < Segments; s++ {
			start := s*perSeg + r.Intn(perSeg-cfg.LossBurst)
			for i := 0; i < cfg.LossBurst; i++ {
				burstAt[start+i] = true
			}
		}
	}
	for i, raw := range sc.Packets {
		arrival := JitterArrival(sc.Seed, i, cfg.JitterMS)
		if cfg.ReorderPermille > 0 && r.Intn(1000) < cfg.ReorderPermille {
			arrival += int64(cfg.JitterMS+20) * 90
		}
		pkt := rtp.Packet{Seq: raw.Seq, TS: uint32(raw.MediaTicks), SSRC: rtp.WireSSRC}
		enc, err := rtp.Payload{Flags: raw.Flags, TrackID: uint16(raw.TrackID), Frame: raw.Frame,
			MediaTicks: raw.MediaTicks, FragIndex: raw.FragIndex, FragCount: raw.FragCount, Data: raw.Data}.Marshal()
		if err != nil {
			return nil, err
		}
		pkt.Payload = enc
		wire, werr := rtp.ToWire(pkt)
		if werr != nil {
			return nil, werr
		}
		if burstAt[i] {
			late := r.Intn(1000) < cfg.LateLateRatio
			tx.Retransmit = append(tx.Retransmit, Retransmit{Wire: wire, Due: arrival + 5*90, Index: i, Late: late})
			tx.Lost = append(tx.Lost, raw)
			continue
		}
		tx.Delivered = append(tx.Delivered, Delivered{Wire: wire, Arrival: arrival})
		if cfg.DuplicatePermille > 0 && r.Intn(1000) < cfg.DuplicatePermille {
			tx.Delivered = append(tx.Delivered, Delivered{Wire: wire, Arrival: arrival + 900})
			tx.Duplicates++
		}
	}
	sort.SliceStable(tx.Delivered, func(i, j int) bool { return tx.Delivered[i].Arrival < tx.Delivered[j].Arrival })
	sort.SliceStable(tx.Retransmit, func(i, j int) bool { return tx.Retransmit[i].Due < tx.Retransmit[j].Due })
	return tx, nil
}

// UnifiedFromRTP converts a track RTP timestamp to unified ticks at an epoch of
// zero. It is used only by the simulation harness, not by the library core.
func UnifiedFromRTP(ts uint64, timescale uint32) int64 {
	v, err := clock.Convert(int64(ts), timescale, clock.UnifiedTimescale)
	if err != nil {
		return 0
	}
	return v
}

// UnifiedFromRTPChecked is UnifiedFromRTP with overflow reporting.
func UnifiedFromRTPChecked(ts uint64, timescale uint32) (int64, error) {
	if ts > math.MaxInt64 {
		return 0, fmt.Errorf("fixtures: timestamp %d exceeds int64", ts)
	}
	return clock.Convert(int64(ts), timescale, clock.UnifiedTimescale)
}
