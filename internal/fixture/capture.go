package fixture

import (
	"encoding/json"
	"fmt"
	"math/rand"

	"archivecheck/internal/transport"
)

// Capture is the RTP capture for one segment.
type Capture struct {
	Segment         string
	Representation  string
	RTPTimebase     int64
	AnchorTimestamp uint32
	// Packets are in arrival order, already shuffled or duplicated.
	Packets []transport.Packet
}

// captureWire is the on-disk capture format. Payloads are stored inline so the
// pipeline can rebuild the exact bytes and verify digests.
type captureWire struct {
	Segment         string       `json:"segment"`
	Representation  string       `json:"representation"`
	RTPTimebase     int64        `json:"rtp_timebase"`
	AnchorTimestamp uint32       `json:"anchor_timestamp"`
	Packets         []packetWire `json:"packets"`
}

type packetWire struct {
	Sequence  uint16 `json:"seq"`
	Timestamp uint32 `json:"ts"`
	Marker    bool   `json:"marker"`
	Payload   string `json:"payload_hex"`
}

// makePackets builds the in-order RTP stream for a run of frames.
func makePackets(rep RepConfig, clock int64, frames []frame, firstSeq uint16) []transport.Packet {
	pkts := make([]transport.Packet, 0, len(frames))
	for i, fr := range frames {
		ts := uint32(uint64(fr.decode) * uint64(clock) / uint64(DefaultTimebase))
		pkts = append(pkts, transport.Packet{
			Sequence:  firstSeq + uint16(i),
			Timestamp: ts,
			Payload:   fr.payload,
			Marker:    fr.key,
		})
	}
	return pkts
}

// applyFault returns the arrival order for a capture. Missing packets are never
// invented and duplicates are explicit so both are observable downstream.
func applyFault(seed int64, fault Fault, rep string, pkts []transport.Packet, segIndex, segments int) []transport.Packet {
	rng := rand.New(rand.NewSource(seed*31 + int64(len(rep))*7919 + int64(segIndex)*104729))
	switch fault {
	case FaultDuplicateReorder:
		return duplicateReorder(pkts, rng)
	case FaultBurstLoss:
		// Only the low representation loses packets, and only around the
		// boundary at the end of segment 4.
		if rep != "v-low" || (segIndex != 3 && segIndex != 4) {
			return pkts
		}
		return dropBurst(pkts, segIndex)
	default:
		return pkts
	}
}

func duplicateReorder(pkts []transport.Packet, rng *rand.Rand) []transport.Packet {
	out := make([]transport.Packet, len(pkts))
	copy(out, pkts)
	for i := 0; i+1 < len(out); i += 2 {
		if rng.Intn(3) != 0 {
			out[i], out[i+1] = out[i+1], out[i]
		}
	}
	for i := 0; i < len(pkts); i += 7 {
		out = append(out, pkts[i])
	}
	if len(pkts) > 5 {
		out = append(out, pkts[2])
	}
	return out
}

// dropBurst removes a contiguous run that crosses the end of segment 3 and the
// start of segment 4, so damaged ranges must merge across the boundary.
func dropBurst(pkts []transport.Packet, segIndex int) []transport.Packet {
	if len(pkts) < 20 {
		return pkts
	}
	var from, to int
	if segIndex == 3 {
		from, to = len(pkts)-6, len(pkts)
	} else {
		from, to = 0, 6
	}
	out := make([]transport.Packet, 0, len(pkts))
	out = append(out, pkts[:from]...)
	out = append(out, pkts[to:]...)
	return out
}

// writeCapture serialises an arrival-order capture and returns its digest.
func writeCapture(root, rel string, c *Capture) (string, error) {
	w := captureWire{
		Segment: c.Segment, Representation: c.Representation,
		RTPTimebase: c.RTPTimebase, AnchorTimestamp: c.AnchorTimestamp,
	}
	for _, p := range c.Packets {
		w.Packets = append(w.Packets, packetWire{
			Sequence: p.Sequence, Timestamp: p.Timestamp, Marker: p.Marker,
			Payload: fmt.Sprintf("%x", p.Payload),
		})
	}
	return writeJSON(root, rel, w)
}

// DecodeCapture reads a capture written by writeCapture.
func DecodeCapture(raw []byte) (*Capture, error) {
	var w captureWire
	if err := json.Unmarshal(raw, &w); err != nil {
		return nil, fmt.Errorf("capture: %w", err)
	}
	c := &Capture{
		Segment: w.Segment, Representation: w.Representation,
		RTPTimebase: w.RTPTimebase, AnchorTimestamp: w.AnchorTimestamp,
	}
	for _, p := range w.Packets {
		payload, err := decodeHex(p.Payload)
		if err != nil {
			return nil, fmt.Errorf("capture packet %d payload: %w", p.Sequence, err)
		}
		c.Packets = append(c.Packets, transport.Packet{
			Sequence: p.Sequence, Timestamp: p.Timestamp, Marker: p.Marker, Payload: payload,
		})
	}
	return c, nil
}
