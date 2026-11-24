package clock

import "github.com/pion/rtp"

// RTP32Modulus is the width of the standard RTP timestamp field.
const RTP32Modulus int64 = 1 << 32

// UnwrapRTPPackets applies boundary-evidenced 32-bit unwrapping to the RTP
// timestamps of already parsed packets. It exists so mediascan's clock
// kernel interoperates directly with github.com/pion/rtp types.
func UnwrapRTPPackets(packets []*rtp.Packet, boundaryTolerance int64) (unwrapped []int64, events []WrapEvent, rejected int) {
	raws := make([]int64, len(packets))
	for i, p := range packets {
		raws[i] = int64(p.Timestamp)
	}
	return UnwrapSequence(RTP32Modulus, boundaryTolerance, raws)
}

// ParseRTPAndUnwrap unmarshals raw RTP packets then unwraps timestamps.
// Malformed packets yield an error and no partial result.
func ParseRTPAndUnwrap(raw [][]byte, boundaryTolerance int64) ([]int64, []WrapEvent, int, error) {
	pkts := make([]*rtp.Packet, 0, len(raw))
	for _, b := range raw {
		var p rtp.Packet
		if err := p.Unmarshal(b); err != nil {
			return nil, nil, 0, err
		}
		pkts = append(pkts, &p)
	}
	v, ev, rej := UnwrapRTPPackets(pkts, boundaryTolerance)
	return v, ev, rej, nil
}
