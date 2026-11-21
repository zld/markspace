// Package demo produces deterministic, self-contained H.264-over-RTP
// sample streams. Every fault (loss, duplication, reordering) is injected
// at fixed frame positions so runs with the same seed are reproducible.
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

// Standard fault frame positions (zero based). They require at least eight
// source frames, which the config layer enforces for the demo command.
const (
	lossFrame     = 2 // one middle FU-A fragment is dropped
	dupFrame      = 5 // one packet is emitted twice
	reorderFrame  = 7 // the last two packets are swapped
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
	// result a correct reorderer + reassembler must reach.
	ExpectedFramesReassembled int
	ExpectedDropped           int
	ExpectedGaps              int
	ExpectedDuplicates        int
}

// Generate builds the sample stream. frames is the number of source H.264
// frames and seed only varies NAL payload content; fault positions are
// fixed to guarantee the standard failure catalogue.
func Generate(frames int, seed int64) *Manifest {
	m := &Manifest{SourceFrames: frames}
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

		out = appendFrame(out, m, framePkts, f)
		ts += frameStep
	}

	m.Packets = out
	if frames >= standardFault {
		m.ExpectedFramesReassembled = frames - 1 // lossFrame cannot be rebuilt
		m.ExpectedDropped = 1
		m.ExpectedGaps = 1
		m.ExpectedDuplicates = 1
	} else {
		m.ExpectedFramesReassembled = frames
	}
	return m
}

// appendFrame applies the deterministic fault catalogue for one frame.
func appendFrame(out []*Packet, m *Manifest, pkts []*rtp.Packet, frame int) []*Packet {
	labels := make([]string, len(pkts))
	for i := range labels {
		labels[i] = KindNormal
	}

	switch {
	case frame == lossFrame:
		// Drop the middle fragment; the frame must later be discarded.
		labels[1] = KindLost
		m.LostPackets++
	case frame == dupFrame:
		// Emit the first packet twice with the same sequence number.
		labels[0] = KindDuplicate
		m.DuplicateSets++
	case frame == reorderFrame:
		// Swap the last two packets while keeping sequence numbers intact.
		pkts[len(pkts)-2], pkts[len(pkts)-1] = pkts[len(pkts)-1], pkts[len(pkts)-2]
		labels[len(pkts)-2] = KindReordered
		labels[len(pkts)-1] = KindReordered
		m.ReorderSets++
	}

	for i, p := range pkts {
		if labels[i] == KindLost {
			continue
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
	// 2 bytes NAL header byte is added by splitFU; here we only need the
	// full unit including its reconstructed header byte.
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
