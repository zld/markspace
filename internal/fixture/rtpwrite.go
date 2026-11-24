package fixture

import (
	"fmt"

	"github.com/pion/rtp"
)

// RTPPlan describes a synthetic RTP delivery sequence for one representation.
type RTPPlan struct {
	PacketCount int
	PayloadLen  int
	SSRC        uint32
	StartSeq    uint16
	StartTS     uint32
	TSStep      uint32
	// DuplicateAt lists sequence indexes (0-based, pre-loss) to send twice.
	DuplicateAt []int
	// ReorderAt lists sequence indexes that are delayed by one position.
	ReorderAt []int
	// LossBurst drops this many consecutive packets starting at LossStart.
	LossStart int
	LossBurst int
	// TSWrap forces the timestamp to restart near the 32 bit modulus so
	// wraparound expansion is exercised.
	TSWrap bool
}

// RTPSession is the wire bytes plus ground truth for assertions.
type RTPSession struct {
	Wire       [][]byte
	Sent       int
	Lost       int
	Boundaries []uint16 // expected surviving sequence numbers in order
}

// BuildRTP renders the plan into marshalled RTP packets.
func BuildRTP(plan RTPPlan) (RTPSession, error) {
	if plan.PacketCount <= 0 || plan.PacketCount > 1<<20 {
		return RTPSession{}, fmt.Errorf("fixture: packet count %d out of range", plan.PacketCount)
	}
	if plan.PayloadLen <= 0 || plan.PayloadLen > 1024 {
		return RTPSession{}, fmt.Errorf("fixture: payload length %d out of range", plan.PayloadLen)
	}
	if plan.LossStart < 0 || plan.LossStart >= plan.PacketCount {
		plan.LossStart = -1
	}
	drop := make(map[int]bool)
	if plan.LossStart >= 0 && plan.LossBurst > 0 {
		for i := 0; i < plan.LossBurst && plan.LossStart+i < plan.PacketCount; i++ {
			drop[plan.LossStart+i] = true
		}
	}
	dup := make(map[int]bool)
	for _, i := range plan.DuplicateAt {
		if i >= 0 && i < plan.PacketCount {
			dup[i] = true
		}
	}
	delay := make(map[int]bool)
	for _, i := range plan.ReorderAt {
		if i >= 0 && i < plan.PacketCount {
			delay[i] = true
		}
	}

	type item struct {
		idx int
		pkt rtp.Packet
	}
	var queue []item
	var held []item
	ts := plan.StartTS
	if plan.TSWrap {
		ts = 0xFFFFFF00
	}
	for i := 0; i < plan.PacketCount; i++ {
		if !drop[i] {
			payload := make([]byte, plan.PayloadLen)
			for j := range payload {
				payload[j] = byte(i*7 + j)
			}
			p := rtp.Packet{
				Header: rtp.Header{
					Version:        2,
					PayloadType:    96,
					SequenceNumber: plan.StartSeq + uint16(i),
					Timestamp:      ts,
					SSRC:           plan.SSRC,
					Marker:         i == plan.PacketCount-1,
				},
				Payload: payload,
			}
			it := item{idx: i, pkt: p}
			if delay[i] {
				// Hold this packet back; it is emitted after the next one.
				held = append(held, it)
			} else {
				queue = append(queue, it)
				queue = append(queue, held...)
				held = held[:0]
			}
			if dup[i] {
				queue = append(queue, it)
			}
		}
		ts += plan.TSStep
	}
	queue = append(queue, held...)

	sess := RTPSession{}
	var kept []int
	for _, it := range queue {
		b, err := it.pkt.Marshal()
		if err != nil {
			return RTPSession{}, err
		}
		sess.Wire = append(sess.Wire, b)
		kept = append(kept, it.idx)
	}
	sess.Sent = len(kept)
	for i := 0; i < plan.PacketCount; i++ {
		if drop[i] {
			sess.Lost++
			continue
		}
		sess.Boundaries = append(sess.Boundaries, plan.StartSeq+uint16(i))
	}
	return sess, nil
}
