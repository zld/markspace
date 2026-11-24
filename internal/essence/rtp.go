package essence

import (
	"fmt"

	"github.com/pion/rtp"
)

// ParseRTP decodes the RTP header embedded at the start of a frame payload.
// The frame must have FlagRTP set and carry at least a full RTP header.
func ParseRTP(h Header, payload []byte) (*rtp.Header, error) {
	if !h.HasRTP() {
		return nil, fmt.Errorf("essence: frame %d of track %d carries no RTP header", h.Seq, h.TrackID)
	}
	// payload is the frame body including its 40-byte essence header; the
	// RTP header starts at the first essence payload byte.
	if len(payload) < HeaderSize {
		return nil, fmt.Errorf("essence: frame %d is shorter than its header", h.Seq)
	}
	var pkt rtp.Packet
	if err := pkt.Unmarshal(payload[HeaderSize:]); err != nil {
		return nil, fmt.Errorf("essence: frame %d RTP header: %w", h.Seq, err)
	}
	// The frame header is authoritative for media time; the RTP header is
	// the cross-check. Copy the timestamp so a caller can compare it without
	// depending on the RTP clock's own origin.
	hdr := pkt.Header
	return &hdr, nil
}

// RTPTimestampTicks converts an RTP timestamp into the track's tick range,
// unwrapping the 32-bit counter around the frame's own start. The RTP
// timestamp uses the track's media clock, so the nearest congruent value at
// or below the container's start is the original media tick.
func RTPTimestampTicks(h Header, rt *rtp.Header) uint64 {
	ts := uint64(rt.Timestamp)
	start := h.StartTick
	// Choose the 32-bit-wrapped value closest to the container's start; a
	// frame is far shorter than half the 2^32 tick space, so this is exact.
	base := start &^ 0xFFFFFFFF
	cands := []uint64{base + ts, base + ts + (1 << 32)}
	if base >= 1<<32 {
		cands = append(cands, base-(1<<32)+ts)
	}
	best := cands[0]
	bestDiff := diffU64(best, start)
	for _, c := range cands[1:] {
		if d := diffU64(c, start); d < bestDiff {
			best, bestDiff = c, d
		}
	}
	return best
}

func diffU64(a, b uint64) uint64 {
	if a > b {
		return a - b
	}
	return b - a
}

// RPTSample builds the deterministic RTP header the fixtures embed. It is
// exported so the writer and the validator cannot disagree about the layout.
func RPTSample(seq uint16, startTick uint64, trackID uint32, audio bool) rtp.Header {
	pt := uint8(96)
	if audio {
		pt = 97
	}
	return rtp.Header{
		Version:        2,
		PayloadType:    pt,
		SequenceNumber: seq,
		Timestamp:      uint32(startTick),
		SSRC:           trackID,
		Marker:         true,
	}
}
