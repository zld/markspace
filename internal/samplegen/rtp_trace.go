package samplegen

import (
	"github.com/pion/rtp"
)

// RTPFault selects an injected RTP-trace condition.
type RTPFault string

const (
	RTPFaultNone      RTPFault = ""
	RTPFaultReorder   RTPFault = "reorder"
	RTPFaultDuplicate RTPFault = "duplicate"
	RTPFaultLoss      RTPFault = "loss"
)

// RTPLayerSpec describes one representation layer's synthetic packet trace.
type RTPLayerSpec struct {
	SSRC        uint32
	PayloadType uint8
	Packets     int
	ClockRate   uint32 // RTP timestamp ticks per second
	StartTS     uint32
	TStep       uint32 // timestamp increment per packet
	StartSeq    uint16
	Fault       RTPFault
	// FaultAt is the packet index where the fault is injected.
	FaultAt int
	// LateKeyframes lists packet indexes whose marker bit signals a keyframe
	// that arrives late (the pipeline applies the configurable wait budget).
	LateKeyframes []int
}

func newRTPPacket(pt uint8, ssrc uint32, seq uint16, ts uint32, marker bool, idx int) *rtp.Packet {
	return &rtp.Packet{
		Header: rtp.Header{
			Version:        2,
			PayloadType:    pt,
			SequenceNumber: seq,
			Timestamp:      ts,
			SSRC:           ssrc,
			Marker:         marker,
		},
		Payload: []byte{byte(idx), byte(idx >> 8), byte(seq)},
	}
}

// BuildRTPTrace produces a deterministic packet trace for one layer. The
// same spec always yields the same packet sequence; reorder/duplicate/loss
// faults are injected at FaultAt without any randomness.
func BuildRTPTrace(spec RTPLayerSpec) []*rtp.Packet {
	pkts := make([]*rtp.Packet, 0, spec.Packets+1)
	seq := spec.StartSeq
	ts := spec.StartTS
	late := map[int]bool{}
	for _, i := range spec.LateKeyframes {
		late[i] = true
	}
	for i := 0; i < spec.Packets; i++ {
		p := newRTPPacket(spec.PayloadType, spec.SSRC, seq, ts, i%10 == 0 || late[i], i)
		switch spec.Fault {
		case RTPFaultDuplicate:
			if i == spec.FaultAt {
				pkts = append(pkts, p)
				dup := *p // duplicate: same seq and timestamp
				pkts = append(pkts, &dup)
				seq++
				ts += spec.TStep
				continue
			}
		case RTPFaultLoss:
			if i == spec.FaultAt {
				seq++ // skip one sequence number and its timestamp step
				ts += spec.TStep
			}
		case RTPFaultReorder:
			if i == spec.FaultAt && i+1 < spec.Packets {
				next := newRTPPacket(spec.PayloadType, spec.SSRC, seq+1, ts+spec.TStep, false, i+1)
				pkts = append(pkts, next, p) // next emitted before current
				seq++
				ts += spec.TStep
				i++ // two packets consumed
				seq++
				ts += spec.TStep
				continue
			}
		}
		pkts = append(pkts, p)
		seq++
		ts += spec.TStep
	}
	return pkts
}
