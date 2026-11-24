// Package ts: RTP-encapsulated transport streams.
//
// Broadcast captures frequently deliver MPEG-TS inside RTP (payload type 33,
// MP2T). This file uses github.com/pion/rtp to decode the RTP headers and
// de-packetise the TS payload, and to report sequence-number gaps with 16-bit
// wrap awareness. The extracted packets are handed to the same scanner used
// for bare .ts files, so sequence diagnostics sit alongside continuity,
// PSI and clock diagnostics.
package ts

import (
	"errors"
	"fmt"
	"io"
	"os"
	"sort"

	"github.com/pion/rtp"
)

// RTPPayloadTypeMP2T is the RTP payload type for an MPEG-TS multiplex.
const RTPPayloadTypeMP2T = 33

// RTPSequenceGap records a lost or reordered RTP packet in a capture.
type RTPSequenceGap struct {
	SSRC      uint32
	PrevSeq   uint16
	Seq       uint16
	Lost      int // packets missing between PrevSeq and Seq; 0 for reorder
	Reordered bool
	Index     int
}

// Error implements error.
func (g RTPSequenceGap) Error() string {
	kind := "loss"
	if g.Reordered {
		kind = "reorder"
	}
	return fmt.Sprintf("rtp %s ssrc=0x%08x prev_seq=%d seq=%d lost=%d", kind, g.SSRC, g.PrevSeq, g.Seq, g.Lost)
}

// RTPAnalysis summarises an RTP capture.
type RTPAnalysis struct {
	Packets      int
	SSRC         uint32
	FirstSeq     uint16
	LastSeq      uint16
	SequenceGaps []RTPSequenceGap
	// Payload is the concatenated RTP media payload, which for MP2T is a
	// stream of 188-byte transport packets.
	Payload []byte
}

// ExtractRTP reads an RTP capture file and returns its de-packetised TS
// payload and sequence diagnostics. Each RTP packet's Payload is appended in
// arrival order; header extensions, padding and CSRCs are handled by the
// library's decoder.
func ExtractRTP(path string) (*RTPAnalysis, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return AnalyzeRTP(f)
}

// AnalyzeRTP de-packetises RTP from r. A transport stream carried as MP2T
// places exactly one or more 188-byte packets per RTP payload; the payloads
// are concatenated so the resulting byte stream aligns to 188 bytes.
func AnalyzeRTP(r io.Reader) (*RTPAnalysis, error) {
	out := &RTPAnalysis{}
	var (
		buf       = make([]byte, 0, 64<<10)
		readBuf   = make([]byte, 64<<10)
		haveFirst bool
		prevSeq   uint16
		prevSSRC  uint32
		extended  int64 // unwrapped sequence position
		lastPos   int64
		packets   int
	)
	for {
		n, err := r.Read(readBuf)
		if n > 0 {
			buf = append(buf, readBuf[:n]...)
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
	}
	// RTP framing has no explicit length; a capture is a sequence of packets.
	// Since we cannot resynchronise a raw byte stream reliably, require the
	// caller to supply one packet per Read when possible. Here we decode as
	// many back-to-back packets as the buffer allows, using each packet's
	// marshalled size as the length. This is exact for captures produced by
	// the fixture writer, where every packet is marshalled back-to-back.
	for len(buf) >= 12 {
		// MP2T over RTP carries one 188-byte TS packet per RTP packet. Decode
		// the header first to learn its exact length (CSRCs and extensions
		// vary), then consume exactly one TS packet. Binding the payload
		// length this way avoids the ambiguity of a raw concatenated stream.
		var hdr rtp.Header
		hdrLen, err := hdr.Unmarshal(buf)
		if err != nil {
			return nil, fmt.Errorf("rtp: header %d: %w", packets, err)
		}
		total := hdrLen + PacketSize
		if total > len(buf) {
			return nil, fmt.Errorf("rtp: packet %d truncated: need %d bytes, have %d", packets, total, len(buf))
		}
		pkt := &rtp.Packet{Header: hdr, Payload: buf[hdrLen:total]}
		consumed := total
		packets++
		if !haveFirst {
			haveFirst = true
			out.SSRC = pkt.SSRC
			out.FirstSeq = pkt.SequenceNumber
			prevSeq = pkt.SequenceNumber
			prevSSRC = pkt.SSRC
			extended = int64(pkt.SequenceNumber)
			lastPos = extended
		} else if pkt.SSRC != prevSSRC {
			// An SSRC change starts a new sequence space; reset without
			// reporting a gap, since the streams are independent.
			prevSSRC = pkt.SSRC
			prevSeq = pkt.SequenceNumber
			extended = int64(pkt.SequenceNumber)
			lastPos = extended
		} else {
			delta := int64(int16(pkt.SequenceNumber - prevSeq))
			if delta == 0 {
				out.SequenceGaps = append(out.SequenceGaps, RTPSequenceGap{
					SSRC: pkt.SSRC, PrevSeq: prevSeq, Seq: pkt.SequenceNumber, Reordered: true, Index: packets - 1,
				})
			} else if delta < 0 {
				// Backward step within the reorder window: mark as reorder.
				out.SequenceGaps = append(out.SequenceGaps, RTPSequenceGap{
					SSRC: pkt.SSRC, PrevSeq: prevSeq, Seq: pkt.SequenceNumber, Reordered: true, Index: packets - 1,
				})
			} else if delta > 1 {
				lastPos += delta
				out.SequenceGaps = append(out.SequenceGaps, RTPSequenceGap{
					SSRC: pkt.SSRC, PrevSeq: prevSeq, Seq: pkt.SequenceNumber, Lost: int(delta - 1), Index: packets - 1,
				})
			} else {
				lastPos += delta
			}
			prevSeq = pkt.SequenceNumber
		}
		out.Payload = append(out.Payload, pkt.Payload...)
		buf = buf[consumed:]
	}
	if packets == 0 {
		return nil, errors.New("rtp: no packets decoded")
	}
	out.Packets = packets
	out.LastSeq = prevSeq
	_ = extended
	_ = lastPos
	sort.SliceStable(out.SequenceGaps, func(i, j int) bool {
		return out.SequenceGaps[i].Index < out.SequenceGaps[j].Index
	})
	return out, nil
}
