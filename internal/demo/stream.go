// Package demo produces deterministic, self-contained H.264-over-RTP
// sample streams. Every fault (loss, duplication, reordering) is injected
// at fixed frame positions so runs with the same seed are reproducible.
//
// The lost FU-A fragment is always on the LAST source frame and is removed
// from the wire, opening exactly one RTP sequence gap. Keeping the loss
// terminal matters because gortsplib's client-side UDP reorderer parks
// packets behind a gap until its 64-packet buffer fills; on a short stream
// that never happens, whereas a terminal gap leaves every earlier frame
// already delivered. Reordering (an adjacent swap) and a duplicate are
// normalized by the reorderers on both transports and do not affect frame
// integrity.
package demo

import (
	"github.com/pion/rtp"
)

const (
	// PayloadType is the dynamic payload type advertised in the SDP.
	PayloadType = 96
	// SSRC is fixed so both UDP and TCP runs observe identical streams.
	SSRC uint32 = 0x01020304
	// StartSeq sits close to the 16-bit boundary so a short stream wraps
	// around and exercises modular sequence arithmetic.
	StartSeq uint16 = 65530
	clockRate       = 90000
	frameStep       = 3000 // 30 fps on a 90 kHz clock
	// fuPayload keeps every datagram well below the UDP MTU and makes each
	// source frame span three FU-A packets.
	fuPayload = 480
)

// Standard fault frame positions (zero based). The catalogue requires the
// minimum frame count below; the config layer enforces it for the demo.
const (
	reorderFrame  = 4 // two adjacent packets of this frame are swapped
	dupFrame      = 6 // one packet is emitted twice
	standardFault = 8
)

// Packet extends rtp.Packet with bookkeeping used by the mock server and
// diagnostics. Raw packets remain ordinary RTP on the wire.
type Packet struct {
	*rtp.Packet
	// Frame is the source frame index the packet belongs to.
	Frame int
	// Kind labels the fault applied to this emission.
	Kind string
}

// Kind labels.
const (
	KindNormal    = "normal"
	KindLost      = "lost"
	KindDuplicate = "duplicate"
	KindReordered = "reordered"
)

// Manifest describes what the generated stream contains.
type Manifest struct {
	SourceFrames  int
	Packets       []*Packet
	LostPackets   int
	DuplicateSets int
	ReorderSets   int
	// ExpectedFramesReassembled / ExpectedDropped encode the deterministic
	// result a correct reorderer + reassembler must reach on either
	// transport.
	ExpectedFramesReassembled int
	ExpectedDropped           int
	ExpectedGaps              int
	ExpectedDuplicates        int
}

// Generate builds the sample stream. frames is the number of source H.264
// frames and seed only varies NAL payload content; fault positions are
// fixed (with the corrupting loss on the last frame) to guarantee the
// standard failure catalogue and reproducible results.
func Generate(frames int, seed int64) *Manifest {
	m := &Manifest{SourceFrames: frames}
	lossFrame := frames - 1 // terminal loss: prior frames stay deliverable
	seq := StartSeq
	ts := uint32(seed % clockRate)
	content := seedByte(seed)

	var out []*Packet

	for f := 0; f < frames; f++ {
		nal := buildNAL(f, content)
		// Split into three FU-A packets: start, middle, end.
		frags := splitFU(nal, fuPayload)
		framePkts := make([]*rtp.Packet, len(frags))
		for i, frag := range frags {
			p := &rtp.Packet{
				Header: rtp.Header{
					Version:        2,
					Marker:         i == len(frags)-1,
					PayloadType:    PayloadType,
					SequenceNumber: seq,
					Timestamp:      ts,
					SSRC:           SSRC,
				},
				Payload: frag,
			}
			seq++
			framePkts[i] = p
		}

		out = appendFrame(out, m, framePkts, f, lossFrame)
		ts += frameStep
	}

	m.Packets = out
	if frames >= standardFault {
		m.ExpectedFramesReassembled = frames - 1 // the loss frame cannot be rebuilt
		m.ExpectedDropped = 1
		m.ExpectedGaps = 1       // one sequence number removed with the lost fragment
		m.ExpectedDuplicates = 1 // one duplicate emission
	} else {
		m.ExpectedFramesReassembled = frames
	}
	return m
}

// appendFrame applies the deterministic fault catalogue for one frame.
func appendFrame(out []*Packet, m *Manifest, pkts []*rtp.Packet, frame, lossFrame int) []*Packet {
	labels := make([]string, len(pkts))
	for i := range labels {
		labels[i] = KindNormal
	}

	switch {
	case frame == lossFrame:
		// Remove the middle FU-A fragment from the wire. Its sequence number
		// opens exactly one RTP gap and the frame can never be reassembled,
		// so H.264 reassembly drops it. Placed on the last frame, every prior
		// frame is already complete before the gap is observed.
		labels[1] = KindLost
		m.LostPackets++
	case frame == dupFrame:
		// A duplicate keeps the same sequence number on the wire. gortsplib
		// normalizes it on UDP and the project reorderer normalizes it on
		// TCP, so neither transport reports a sequence gap for it.
		labels[0] = KindDuplicate
		m.DuplicateSets++
	case frame == reorderFrame:
		// Swap two adjacent packets while keeping their sequence numbers.
		// Both reorderers restore the original order, so the delivered
		// stream is identical across transports.
		pkts[len(pkts)-2], pkts[len(pkts)-1] = pkts[len(pkts)-1], pkts[len(pkts)-2]
		labels[len(pkts)-2] = KindReordered
		labels[len(pkts)-1] = KindReordered
		m.ReorderSets++
	}

	for i, p := range pkts {
		if labels[i] == KindLost {
			continue // the lost fragment is not emitted, producing the gap
		}
		out = append(out, &Packet{Packet: p, Frame: frame, Kind: labels[i]})
		if frame == dupFrame && i == 0 {
			// Second emission of the identical packet.
			out = append(out, &Packet{Packet: p, Frame: frame, Kind: KindDuplicate})
		}
	}
	return out
}

// buildNAL creates a deterministic IDR-like NAL unit (type 5) whose payload
// content derives from the seed and frame index.
func buildNAL(frame int, content byte) []byte {
	// The unit holds its reconstructed header byte plus three FU payloads.
	nal := make([]byte, 1+3*fuPayload)
	nal[0] = 0x65 // forbidden=0, nal_ref_idc=3, type=5 (IDR)
	for i := 1; i < len(nal); i++ {
		nal[i] = content + byte(frame) + byte(i*7)
	}
	return nal
}

// splitFU packetizes one NAL unit into FU-A fragments of at most payload
// bytes each.
func splitFU(nal []byte, payload int) [][]byte {
	headerByte := nal[0]
	body := nal[1:]
	highBits := headerByte & 0xE0
	nalType := headerByte & 0x1F

	var frags [][]byte
	for start := 0; start < len(body); {
		end := start + payload
		if end > len(body) {
			end = len(body)
		}
		indicator := highBits | nalTypeFUA
		var fuHeader byte
		switch {
		case start == 0:
			fuHeader = 0x80 | nalType // start bit
		case end == len(body):
			fuHeader = 0x40 | nalType // end bit
		default:
			fuHeader = nalType
		}
		frag := make([]byte, 0, 2+end-start)
		frag = append(frag, indicator, fuHeader)
		frag = append(frag, body[start:end]...)
		frags = append(frags, frag)
		start = end
	}
	return frags
}

// seedByte maps a seed to a stable non-zero byte.
func seedByte(seed int64) byte {
	b := byte(seed*2654435761>>24) + 1
	return b
}

// nalTypeFUA is the RFC 6184 FU-A NAL type.
const nalTypeFUA = 28
