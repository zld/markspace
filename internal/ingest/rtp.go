package ingest

import (
	"errors"
	"fmt"
	"sort"

	"github.com/pion/rtp"

	"regress-kernel/internal/media"
)

// RTPConfig parameterizes the audio receive and loss-recovery policy.
type RTPConfig struct {
	// MaxReorder is the largest late-arrival displacement tolerated.
	MaxReorder int
	// MaxRecoverGap is the longest contiguous loss run repaired by PLC.
	MaxRecoverGap int
	// FrameTicks is the constant audio packet duration on the RTP clock.
	FrameTicks uint32
	// MaxPackets bounds how many packets one task accepts.
	MaxPackets int
}

// DefaultRTPConfig matches the generated 48kHz/20ms audio fixture.
func DefaultRTPConfig() RTPConfig {
	return RTPConfig{MaxReorder: 3, MaxRecoverGap: 1, FrameTicks: 960, MaxPackets: 16384}
}

// RTPReceiver is an offline, deterministic jitter buffer: it records arrival
// order, reorders by RTP sequence number, enforces the reorder window and
// applies the configured packet-loss-concealment policy.
type RTPReceiver struct {
	cfg      RTPConfig
	arrival  []uint16
	packets  map[uint16]*rtp.Packet
	dupCount int
}

// NewRTPReceiver builds an empty receiver for the given policy.
func NewRTPReceiver(cfg RTPConfig) *RTPReceiver {
	return &RTPReceiver{cfg: cfg, packets: map[uint16]*rtp.Packet{}}
}

// seqDistance returns the signed forward distance between two uint16 seqs,
// handling the 16-bit wrap correctly.
func seqDistance(from, to uint16) int32 { return int32(int16(to - from)) }

// Receive buffers one packet. Duplicate packets are counted and ignored; the
// packet cap returns ErrLimitExceeded so the task cannot grow unbounded.
func (r *RTPReceiver) Receive(p *rtp.Packet) error {
	if len(r.packets) >= r.cfg.MaxPackets {
		return fmt.Errorf("%w: more than %d RTP packets",
			media.ErrLimitExceeded, r.cfg.MaxPackets)
	}
	if _, exists := r.packets[p.SequenceNumber]; exists {
		r.dupCount++
		return nil
	}
	r.packets[p.SequenceNumber] = p
	r.arrival = append(r.arrival, p.SequenceNumber)
	return nil
}

// Duplicates reports how many repeated packets were dropped.
func (r *RTPReceiver) Duplicates() int { return r.dupCount }

// sortedSeqs returns the buffered sequence numbers in true forward order
// starting at the first ever received packet, unwrapping the uint16 cycle.
func (r *RTPReceiver) sortedSeqs() []uint16 {
	seqs := make([]uint16, 0, len(r.packets))
	for s := range r.packets {
		seqs = append(seqs, s)
	}
	first := r.arrival[0]
	sort.Slice(seqs, func(i, j int) bool {
		return seqDistance(first, seqs[i]) < seqDistance(first, seqs[j])
	})
	return seqs
}

// checkReorder enforces the "at most MaxReorder packets late" rule on the
// recorded arrival order; displacement is measured against sorted rank.
func (r *RTPReceiver) checkReorder(sorted []uint16) error {
	rank := make(map[uint16]int, len(sorted))
	for i, s := range sorted {
		rank[s] = i
	}
	for arrivalPos, seq := range r.arrival {
		displacement := arrivalPos - rank[seq] // positive = arrived late
		if displacement > r.cfg.MaxReorder {
			return fmt.Errorf("%w: seq=%d arrived %d positions late, limit %d",
				media.ErrRTPOrder, seq, displacement, r.cfg.MaxReorder)
		}
	}
	return nil
}

// plcPayload is the deterministic concealment fill for a repaired packet.
func plcPayload(seq uint16) []byte {
	b := make([]byte, 32)
	for i := range b {
		b[i] = byte(seq) ^ byte(i)
	}
	return b
}

// Finalize reorders the stream, accounts for every loss and applies recovery.
// A loss run no longer than MaxRecoverGap is repaired with an extrapolated
// concealment packet (Recovered). Longer runs are explicitly marked
// Unrecoverable and the returned error wraps media.ErrRTPLoss — nothing is
// ever silently skipped.
func (r *RTPReceiver) Finalize() ([]media.AudioPacket, media.LossReport, error) {
	if len(r.arrival) == 0 {
		return nil, media.LossReport{RecoveryRate: 1}, nil
	}
	sorted := r.sortedSeqs()
	var orderErr error
	if err := r.checkReorder(sorted); err != nil {
		orderErr = err
	}

	first := sorted[0]
	last := sorted[len(sorted)-1]
	total := int(seqDistance(first, last)) + 1
	out := make([]media.AudioPacket, 0, total)
	rep := media.LossReport{RecoveryRate: 1}
	var unrecovered error

	for step := 0; step < total; step++ {
		seq := first + uint16(step)
		if p, ok := r.packets[seq]; ok {
			out = append(out, media.AudioPacket{
				Seq: seq, RTPTimestamp: p.Timestamp,
				DurationTicks: r.cfg.FrameTicks, Payload: p.Payload,
			})
			continue
		}
		// Identify the full contiguous missing run before deciding policy.
		runStart := step
		for step < total {
			if _, ok := r.packets[first+uint16(step)]; ok {
				break
			}
			step++
		}
		runLen := step - runStart
		step-- // outer loop re-visits the first present packet
		rep.Injected += runLen
		base := out[len(out)-1] // previous packet always present at stream start
		for k := 0; k < runLen; k++ {
			missSeq := base.Seq + uint16(k+1)
			ap := media.AudioPacket{
				Seq:           missSeq,
				RTPTimestamp:  base.RTPTimestamp + r.cfg.FrameTicks*uint32(k+1),
				DurationTicks: r.cfg.FrameTicks,
				Payload:       plcPayload(missSeq),
			}
			if runLen <= r.cfg.MaxRecoverGap {
				ap.Recovered = true
				rep.Recovered++
			} else {
				ap.Unrecoverable = true
				rep.Unrecoverable++
				unrecovered = fmt.Errorf("%w: %d consecutive packets from seq=%d",
					media.ErrRTPLoss, runLen, base.Seq+1)
			}
			out = append(out, ap)
		}
	}
	if rep.Injected > 0 {
		rep.RecoveryRate = float64(rep.Recovered) / float64(rep.Injected)
	}
	return out, rep, errors.Join(orderErr, unrecovered)
}
