package rtp

import (
	"fmt"

	"github.com/pion/rtp"
)

// This file holds the per-RTP-packet H.264 payload decoders: STAP-A
// aggregation and FU-A fragmentation. The access-unit lifecycle, stats and
// frame emission live in h264.go.

// processSTAPA expands an RFC 6184 STAP-A packet into its contained NAL
// units and appends each one to the access unit.
func (r *Reassembler) processSTAPA(key FrameKey, au *accessUnit, payload []byte) error {
	body := payload[1:]
	for len(body) > 0 {
		if len(body) < 2 {
			return fmt.Errorf("%w: truncated STAP-A size", errMalformed)
		}
		size := int(body[0])<<8 | int(body[1])
		body = body[2:]
		if size == 0 || size > len(body) {
			return fmt.Errorf("%w: STAP-A unit length out of range", errMalformed)
		}
		if err := r.appendNAL(key, au, body[:size]); err != nil {
			return err
		}
		body = body[size:]
	}
	return nil
}

// processFUA accumulates an RFC 6184 FU-A fragmented NAL unit. It rejects a
// fragment without a preceding start, a repeated start and a gap in the RTP
// sequence numbers, and enforces the per-frame size limit.
func (r *Reassembler) processFUA(key FrameKey, au *accessUnit, pkt *rtp.Packet) error {
	payload := pkt.Payload
	if len(payload) < 2 {
		return fmt.Errorf("%w: truncated FU-A header", errIllegalFU)
	}
	indicator, fuHeader := payload[0], payload[1]
	start := fuHeader&0x80 != 0
	end := fuHeader&0x40 != 0
	nalType := fuHeader & 0x1F
	body := payload[2:]

	asm := r.fu[key]
	if start {
		if asm != nil && asm.started {
			return fmt.Errorf("%w: repeated FU-A start", errIllegalFU)
		}
		asm = &fuAssembly{
			header:  (indicator & 0xE0) | nalType,
			started: true,
			lastSeq: pkt.SequenceNumber,
			haveSeq: true,
		}
		r.fu[key] = asm
	} else {
		if asm == nil || !asm.started {
			return fmt.Errorf("%w: FU-A fragment without start", errIllegalFU)
		}
		// a continuation fragment must be exactly one sequence number after
		// the previous one; a jump proves an intervening fragment was lost.
		if pkt.SequenceNumber != asm.lastSeq+1 {
			return fmt.Errorf("%w: expected seq %d, got %d",
				errMissingFragment, asm.lastSeq+1, pkt.SequenceNumber)
		}
		asm.lastSeq = pkt.SequenceNumber
	}

	asm.data = append(asm.data, body...)
	if len(asm.data)+1 > r.maxSize {
		asm.oversize = true
	}

	if !end {
		return nil
	}

	delete(r.fu, key)
	if asm.oversize {
		return errOversize
	}
	nal := make([]byte, 0, len(asm.data)+1)
	nal = append(nal, asm.header)
	nal = append(nal, asm.data...)
	return r.appendNAL(key, au, nal)
}
