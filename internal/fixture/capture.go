package fixture

import (
	"encoding/base64"
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

// packetWire stores each packet as base64-encoded RTP wire bytes so the
// pipeline exercises the real pion/rtp decoder rather than a bespoke format.
type packetWire struct {
	RTP string `json:"rtp"`
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
	case FaultRTPCollision:
		if rep != "v-low" || segIndex != 2 {
			return pkts
		}
		// Re-deliver an already-seen sequence number with a mutated payload.
		// The original slice is copied first so the in-order packet keeps its
		// bytes; only the retransmission differs.
		out := append([]transport.Packet(nil), pkts...)
		idx := len(pkts) / 2
		evil := out[idx]
		evil.Payload = append([]byte(nil), pkts[idx].Payload...)
		evil.Payload[0] ^= 0xff
		return append(out, evil)
	case FaultBurstLoss:
		// The video representations lose packets around the boundary at the end
		// of segment 3 and the start of segment 4. HLS analysis of the same
		// timeline therefore observes the same loss.
		if (rep != "v-low" && rep != "v-high") || (segIndex != 3 && segIndex != 4) {
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

// dropBurst removes contiguous runs near the end of segment 3 and the start of
// segment 4. The first run ends just before the last frame of segment 3 and the
// second begins at frame 0 of segment 4, so the two damaged ranges are within
// one target frame of each other and must merge across the segment boundary.
func dropBurst(pkts []transport.Packet, segIndex int) []transport.Packet {
	if len(pkts) < 20 {
		return pkts
	}
	var from, to int
	switch segIndex {
	case 3:
		from, to = len(pkts)-8, len(pkts)-1 // keep the last frame so the gap is interior
	case 4:
		from, to = 1, 9 // keep the first frame so the gap is interior
	default:
		return pkts
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
		raw, err := transport.MarshalRTP(p, transport.RTPSSRC)
		if err != nil {
			return "", err
		}
		w.Packets = append(w.Packets, packetWire{RTP: base64.StdEncoding.EncodeToString(raw)})
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
	for i, p := range w.Packets {
		raw, err := base64.StdEncoding.DecodeString(p.RTP)
		if err != nil {
			return nil, fmt.Errorf("capture packet %d: %w", i, err)
		}
		pkt, fault := transport.ParseRTP(raw, transport.Source{
			Segment: w.Segment, Representation: w.Representation,
		})
		if fault != nil {
			return nil, fmt.Errorf("capture packet %d: %w", i, fault)
		}
		c.Packets = append(c.Packets, pkt)
	}
	return c, nil
}
