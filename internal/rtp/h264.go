package rtp

import (
	"errors"
	"fmt"

	"github.com/pion/rtp"
)

// H.264 NAL unit types relevant to RTP packetization (RFC 6184).
const (
	nalTypeSTAPA = 24 // aggregation packet
	nalTypeFUA   = 28 // fragmentation unit
	nalTypeMax   = 31
)

// Maximum size of one reassembled NAL unit. It bounds FU-A accumulation so
// a peer cannot force unbounded buffering.
const maxNALSize = 64 * MaxPayload

// Drop reasons reported for incomplete or invalid access units.
const (
	DropMissingFragment = "missing_fu_fragment"
	DropTimestampJump   = "timestamp_jump"
	DropSSRCChange      = "ssrc_change"
	DropFUWithoutStart  = "fu_without_start"
	DropResourceLimit   = "resource_limit"
)

// ErrUnsupportedNALType is returned for NAL types the reassembler does not
// implement (MTAP, FU-B, ...).
var ErrUnsupportedNALType = errors.New("rtp: unsupported H.264 NAL unit type")

// H264Stats summarizes one reassembled H.264 stream.
type H264Stats struct {
	FramesReassembled int
	FramesDropped     int
	DropsByReason     map[string]int
}

// H264Reassembler turns ordered RTP packets into H.264 access units.
// Instances are not goroutine-safe; feed packets from a single goroutine
// in reorderer output order.
type H264Reassembler struct {
	maxBufferedAUs int

	ssrc     uint32
	haveSSRC bool
	lastSeq  uint16
	haveSeq  bool

	// FU-A state.
	fuActive  bool
	fuMissing bool // a sequence gap was seen inside the current FU
	fuBuf     []byte
	fuTS      uint32

	// Access unit accumulation, ended by marker or timestamp change.
	curTS   uint32
	haveTS  bool
	nals    [][]byte
	stats   H264Stats
}

// NewH264Reassembler creates a reassembler. maxBufferedAUs caps how many
// NAL units one access unit may contain.
func NewH264Reassembler(maxBufferedAUs int) *H264Reassembler {
	return &H264Reassembler{
		maxBufferedAUs: maxBufferedAUs,
		stats: H264Stats{
			DropsByReason: make(map[string]int),
		},
	}
}

// Process consumes one in-order packet. It returns a complete access unit
// (a slice of NAL units) when one is finalized, or nil while the unit is
// still accumulating. A dropped partial unit is reported through Stats
// rather than as an error.
func (h *H264Reassembler) Process(pkt *rtp.Packet) ([][]byte, error) {
	if h.haveSSRC && pkt.SSRC != h.ssrc {
		// SSRC change invalidates every partially buffered structure.
		h.dropAll(DropSSRCChange)
		h.ssrc = pkt.SSRC
	}
	if !h.haveSSRC {
		h.ssrc = pkt.SSRC
		h.haveSSRC = true
	}

	// Detect sequence discontinuity while a FU-A is open: the reorderer
	// delivers around confirmed gaps, so the jump is visible here.
	if h.haveSeq && h.fuActive && pkt.SequenceNumber != uint16(h.lastSeq+1) {
		h.fuMissing = true
	}
	h.lastSeq = pkt.SequenceNumber
	h.haveSeq = true

	// Timestamp boundary: finalize whatever the previous timestamp held.
	var frame [][]byte
	if h.haveTS && pkt.Timestamp != h.curTS {
		frame = h.rollover()
	}
	if !h.haveTS {
		h.haveTS = true
	}
	h.curTS = pkt.Timestamp

	if err := h.processPayload(pkt); err != nil {
		return frame, err
	}

	if pkt.Marker {
		if h.fuActive {
			// Marker with an open FU means its end fragment never arrived.
			h.resetFU(DropMissingFragment)
			return frame, nil
		}
		if len(h.nals) > 0 {
			completed := h.nals
			h.nals = nil
			h.stats.FramesReassembled++
			return completed, nil
		}
	}
	return frame, nil
}

// processPayload handles single NAL, STAP-A and FU-A payloads.
func (h *H264Reassembler) processPayload(pkt *rtp.Packet) error {
	payload := pkt.Payload
	if len(payload) == 0 {
		return ErrEmptyPayload
	}
	nalType := payload[0] & 0x1F

	switch {
	case nalType >= 1 && nalType < nalTypeSTAPA:
		return h.appendNAL(append([]byte(nil), payload...))

	case nalType == nalTypeSTAPA:
		return h.processSTAPA(payload)

	case nalType == nalTypeFUA:
		return h.processFUA(pkt, payload)

	default:
		return fmt.Errorf("%w: %d", ErrUnsupportedNALType, nalType)
	}
}

// processSTAPA extracts the NAL units aggregated in one packet.
func (h *H264Reassembler) processSTAPA(payload []byte) error {
	pos := 1 // skip STAP-A header
	for pos < len(payload) {
		if pos+2 > len(payload) {
			return fmt.Errorf("%w: truncated STAP-A size field", ErrTruncated)
		}
		size := int(payload[pos])<<8 | int(payload[pos+1])
		pos += 2
		if size == 0 || pos+size > len(payload) {
			return fmt.Errorf("%w: STAP-A unit exceeds payload", ErrTruncated)
		}
		nal := append([]byte(nil), payload[pos:pos+size]...)
		if err := h.appendNAL(nal); err != nil {
			return err
		}
		pos += size
	}
	return nil
}

// processFUA accumulates fragments and emits the reconstructed NAL unit on
// the end bit.
func (h *H264Reassembler) processFUA(pkt *rtp.Packet, payload []byte) error {
	if len(payload) < 2 {
		return fmt.Errorf("%w: FU-A header missing", ErrTruncated)
	}
	fuHeader := payload[1]
	start := fuHeader&0x80 != 0
	end := fuHeader&0x40 != 0
	fuType := fuHeader & 0x1F

	if start {
		if h.fuActive && h.fuTS == pkt.Timestamp {
			// Restart without an end bit: the previous FU was incomplete.
			h.resetFU(DropMissingFragment)
		} else if h.fuActive {
			h.resetFU(DropTimestampJump)
		}
		// Reconstructed header keeps the original 3 high bits.
		h.fuActive = true
		h.fuMissing = false
		h.fuTS = pkt.Timestamp
		h.fuBuf = []byte{(payload[0] & 0xE0) | fuType}
	} else if !h.fuActive {
		h.drop(DropFUWithoutStart)
		return nil
	}

	if len(h.fuBuf)+len(payload)-2 > maxNALSize {
		h.resetFU(DropResourceLimit)
		return fmt.Errorf("%w: reassembled NAL exceeds %d bytes", ErrPayloadTooBig, maxNALSize)
	}
	h.fuBuf = append(h.fuBuf, payload[2:]...)

	if end {
		if !h.fuActive {
			return nil
		}
		nal := h.fuBuf
		h.fuActive = false
		h.fuBuf = nil
		if h.fuMissing {
			h.fuMissing = false
			h.drop(DropMissingFragment)
			return nil
		}
		return h.appendNAL(nal)
	}
	return nil
}

// appendNAL stores one complete NAL unit, enforcing the AU buffer cap.
func (h *H264Reassembler) appendNAL(nal []byte) error {
	if len(h.nals) >= h.maxBufferedAUs {
		h.nals = nil
		h.drop(DropResourceLimit)
		return fmt.Errorf("%w: more than %d NAL units in one access unit",
			ErrPayloadTooBig, h.maxBufferedAUs)
	}
	h.nals = append(h.nals, nal)
	return nil
}

// rollover closes the access unit accumulated under the previous timestamp.
// An open FU-A at a timestamp boundary is an interrupted frame.
func (h *H264Reassembler) rollover() [][]byte {
	if h.fuActive {
		h.resetFU(DropTimestampJump)
	}
	if len(h.nals) == 0 {
		return nil
	}
	frame := h.nals
	h.nals = nil
	h.stats.FramesReassembled++
	return frame
}

// Flush ends the stream and returns any fully accumulated final access unit.
func (h *H264Reassembler) Flush() [][]byte {
	if h.fuActive {
		h.resetFU(DropMissingFragment)
	}
	if len(h.nals) == 0 {
		return nil
	}
	frame := h.nals
	h.nals = nil
	h.stats.FramesReassembled++
	return frame
}

func (h *H264Reassembler) resetFU(reason string) {
	h.fuActive = false
	h.fuMissing = false
	h.fuBuf = nil
	h.drop(reason)
}

// drop records a dropped frame and discards the access unit being built.
func (h *H264Reassembler) drop(reason string) {
	h.nals = nil
	h.stats.FramesDropped++
	h.stats.DropsByReason[reason]++
}

// dropAll resets every partial structure after a stream-level violation.
func (h *H264Reassembler) dropAll(reason string) {
	h.fuActive = false
	h.fuBuf = nil
	h.fuMissing = false
	h.nals = nil
	h.drop(reason)
}

// Stats returns a copy of the accumulated counters.
func (h *H264Reassembler) Stats() H264Stats {
	out := H264Stats{
		FramesReassembled: h.stats.FramesReassembled,
		FramesDropped:     h.stats.FramesDropped,
		DropsByReason:     make(map[string]int, len(h.stats.DropsByReason)),
	}
	for k, v := range h.stats.DropsByReason {
		out.DropsByReason[k] = v
	}
	return out
}
