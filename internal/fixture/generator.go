// Package fixture synthesizes MPEG-TS test streams entirely in code: no
// external sample files are referenced. A generated stream contains a PAT,
// a PMT whose version is updated mid-stream, two representations (two PCR
// PIDs) sharing one time base, and a deliberate PCR wraparound so every
// clock/PSI code path can be exercised. Optional corruption modes mutate the
// stream at known offsets that the generator reports back for assertions.
package fixture

import (
	"bytes"
	"fmt"

	"mediad/internal/model"
)

// Corruption modes understood by Generate.
const (
	CorruptNone       = ""
	CorruptSync       = "sync"
	CorruptTruncated  = "truncated"
	CorruptContinuity = "continuity"
	CorruptPCR        = "pcr"
	CorruptPSI        = "psi"
)

// Spec configures a generated stream.
type Spec struct {
	// PacketsPerSegment drives stream length in segment units.
	PacketsPerSegment int
	SegmentCount      int
	// SegmentMs is the target segment duration in milliseconds.
	SegmentMs int
	// Corrupt selects the mutation, or CorruptNone for a clean stream.
	Corrupt string
	// WrapPCR forces at least one PCR wraparound into the stream.
	WrapPCR bool
	// PSIUpdate emits a second PMT version halfway through.
	PSIUpdate bool
}

// DefaultSpec returns the spec used by the demo command.
func DefaultSpec() Spec {
	return Spec{
		PacketsPerSegment: 20,
		SegmentCount:      6,
		SegmentMs:         100,
		WrapPCR:           true,
		PSIUpdate:         true,
	}
}

// Stream is one generated input plus metadata the pipeline needs to
// interpret and verify it.
type Stream struct {
	Bytes []byte
	// PCRPIDs are the two representation PCR PIDs.
	PCRPIDs [2]uint16
	// VideoPIDs are the corresponding elementary-stream PIDs.
	VideoPIDs [2]uint16
	// PMTPIDs are the two program map table PIDs, one per representation.
	PMTPIDs [2]uint16
	// PacketTicks is the nominal media duration of one 188-byte packet.
	PacketTicks int64
	SegmentTicks int64
	// CorruptionOffset is the byte offset at which the mutation starts,
	// or -1 for a clean stream.
	CorruptionOffset int64
	// CorruptionPID names the PID affected by the mutation.
	CorruptionPID uint16
}

const (
	patPID    = uint16(0x0000)
	pmtPID0   = uint16(0x0100)
	pmtPID1   = uint16(0x0101)
	pcrPID0   = uint16(0x0110)
	vidPID0   = uint16(0x0200)
	pcrPID1   = uint16(0x0111)
	vidPID1   = uint16(0x0201)
	program1  = uint16(1)
	program2  = uint16(2)
)

// program describes one representation as an independent MPEG-TS program:
// it owns a PMT PID, a PCR PID and one video elementary PID.
type program struct {
	number uint16
	pmtPID uint16
	pcrPID uint16
	vidPID uint16
}

// programs returns the two representation programs in stable order.
func programs() [2]program {
	return [2]program{
		{number: program1, pmtPID: pmtPID0, pcrPID: pcrPID0, vidPID: vidPID0},
		{number: program2, pmtPID: pmtPID1, pcrPID: pcrPID1, vidPID: vidPID1},
	}
}

// Generate builds the stream described by spec.
func Generate(spec Spec) (*Stream, error) {
	if spec.PacketsPerSegment <= 0 || spec.SegmentCount <= 0 || spec.SegmentMs <= 0 {
		return nil, fmt.Errorf("fixture: invalid spec %+v", spec)
	}
	totalPackets := spec.PacketsPerSegment * spec.SegmentCount
	segmentTicks := int64(spec.SegmentMs) * model.ClockHz / 1000
	packetTicks := segmentTicks / int64(spec.PacketsPerSegment)

	progs := programs()
	out := &Stream{
		PCRPIDs:         [2]uint16{progs[0].pcrPID, progs[1].pcrPID},
		VideoPIDs:       [2]uint16{progs[0].vidPID, progs[1].vidPID},
		PMTPIDs:         [2]uint16{progs[0].pmtPID, progs[1].pmtPID},
		PacketTicks:     packetTicks,
		SegmentTicks:    segmentTicks,
		CorruptionOffset: -1,
	}

	buf := &bytes.Buffer{}
	cc := map[uint16]uint8{}
	put := func(pid uint16, pusi bool, afc uint8, payload, adapt []byte) {
		pkt := buildPacket(pid, cc[pid], pusi, afc, payload, adapt)
		cc[pid]++
		buf.Write(pkt[:])
	}

	// PCR starts one period minus a few packet steps so the timeline
	// crosses the 2^33*300 wrap while the stream is still short.
	const pcrPeriod = model.PcrBaseModulus * 300
	var startPCR int64
	if spec.WrapPCR {
		startPCR = pcrPeriod - int64(totalPackets)*packetTicks/2
	}

	// PAT plus one PMT per program at the head of the stream.
	put(patPID, true, model.AFCPayloadOnly, buildPAT(0, progs), nil)
	for _, pr := range progs {
		put(pr.pmtPID, true, model.AFCPayloadOnly,
			buildPMT(0, pr.number, pr.pcrPID, pr.vidPID), nil)
	}

	psiSwitchAt := totalPackets / 2
	for i := 0; i < totalPackets; i++ {
		if spec.PSIUpdate && i == psiSwitchAt {
			// Version 1 moves each program's video PID to a new value; the
			// old PIDs must disappear from the mapping atomically.
			for rep, pr := range progs {
				put(pr.pmtPID, true, model.AFCPayloadOnly,
					buildPMT(1, pr.number, pr.pcrPID, pr.vidPID+0x10), nil)
				_ = rep
			}
		}
		pcr := startPCR + int64(i)*packetTicks
		pcr %= pcrPeriod
		if pcr < 0 {
			pcr += pcrPeriod
		}
		// Both representations carry the same PCR value per iteration, so
		// their timelines share one monotonic media time base.
		for rep, pr := range progs {
			put(pr.pcrPID, false, model.AFCAdaptAndPayload,
				syntheticPayload(rep, i), buildAdaptation(pcr))
			// After the PSI switch the video PID moves; mirror it in the
			// emitted packets so the router follows the new mapping.
			vidPid := pr.vidPID
			if spec.PSIUpdate && i >= psiSwitchAt {
				vidPid += 0x10
			}
			put(vidPid, i%spec.PacketsPerSegment == 0,
				model.AFCPayloadOnly, syntheticPayload(rep+8, i), nil)
		}
	}

	raw := buf.Bytes()
	raw, err := applyCorruption(spec.Corrupt, raw, out)
	if err != nil {
		return nil, err
	}
	out.Bytes = raw
	return out, nil
}

// buildPacket assembles one 188-byte packet with an optional adaptation
// field and payload.
func buildPacket(pid uint16, cc uint8, pusi bool, afc uint8, payload, adapt []byte) [model.PacketSize]byte {
	var p [model.PacketSize]byte
	p[0] = model.SyncByte
	p[1] = byte(pid >> 8)
	if pusi {
		p[1] |= 0x40
	}
	p[2] = byte(pid)
	p[3] = (afc << 4) | (cc & 0x0F)
	pos := 4
	if afc == model.AFCAdaptationOnly || afc == model.AFCAdaptAndPayload {
		if adapt == nil {
			p[4] = 0x00 // adaptation length zero
			pos = 5
		} else {
			p[4] = byte(1 + len(adapt))
			p[5] = 0x10 // PCR flag
			copy(p[6:], adapt)
			pos = 6 + len(adapt)
		}
	}
	if afc == model.AFCPayloadOnly || afc == model.AFCAdaptAndPayload {
		copy(p[pos:], payload)
	}
	return p
}

// buildAdaptation encodes a 6-byte PCR field with PCR-flag set by caller.
func buildAdaptation(pcr int64) []byte {
	b := make([]byte, 6)
	base := pcr / 300
	ext := pcr % 300
	b[0] = byte(base >> 25)
	b[1] = byte(base >> 17)
	b[2] = byte(base >> 9)
	b[3] = byte(base >> 1)
	b[4] = byte(base<<7) | byte(ext>>8)
	b[5] = byte(ext)
	return b
}

// syntheticPayload makes deterministic, non-uniform media bytes.
func syntheticPayload(rep, i int) []byte {
	b := make([]byte, 160)
	for j := range b {
		b[j] = byte(rep*37 + i*7 + j)
	}
	return b
}

// buildPAT encodes a PAT listing both representation programs.
func buildPAT(version uint8, progs [2]program) []byte {
	body := []byte{
		0x00, // pointer field
		0x00, // table id
	}
	body = append(body, 0x00, 0x00) // section length placeholder
	body = append(body, 0x00, 0x01) // transport stream id
	body = append(body, byte((version<<1)|0x01), 0x00, 0x00)
	for _, pr := range progs {
		body = append(body, byte(pr.number>>8), byte(pr.number))
		body = append(body, byte(pr.pmtPID>>8)|0xE0, byte(pr.pmtPID))
	}
	body = append(body, 0x00, 0x00, 0x00, 0x00) // CRC
	sectionLen := len(body) - 3
	body[2] = 0xB0 | byte(sectionLen>>8)
	body[3] = byte(sectionLen)
	return body
}

// buildPMT encodes one program's PMT with a single video elementary stream.
func buildPMT(version, program, pcrPID, vidPID uint16) []byte {
	body := []byte{
		0x00, // pointer field
		0x02, // PMT table id
	}
	body = append(body, 0x00, 0x00) // section length placeholder
	body = append(body, byte(program>>8), byte(program))
	body = append(body, byte((version<<1)|0x01), 0x00, 0x00)
	body = append(body, byte(pcrPID>>8)|0xE0, byte(pcrPID))
	body = append(body, 0xF0, 0x00) // no program descriptors
	body = append(body, 0x1B)       // H.264 video stream type
	body = append(body, byte(vidPID>>8)|0xE0, byte(vidPID))
	body = append(body, 0xF0, 0x00) // no ES descriptors
	body = append(body, 0x00, 0x00, 0x00, 0x00)
	sectionLen := len(body) - 3
	body[2] = 0xB0 | byte(sectionLen>>8)
	body[3] = byte(sectionLen)
	return body
}

// applyCorruption mutates the stream at a deterministic location and
// records the absolute offset and affected PID on out.
func applyCorruption(mode string, raw []byte, out *Stream) ([]byte, error) {
	// Target the first media packet of the third packet group, well past
	// the PAT/PMT head so the parser reaches real media before failing.
	targetPacket := 6
	off := int64(targetPacket) * model.PacketSize
	switch mode {
	case CorruptNone:
		return raw, nil
	case CorruptSync:
		raw[off] = 0x00
		out.CorruptionOffset = off
		out.CorruptionPID = pidAt(raw, off)
	case CorruptTruncated:
		out.CorruptionOffset = off
		out.CorruptionPID = pidAt(raw, off)
		return raw[:off+40], nil // keep a 40-byte partial tail
	case CorruptContinuity:
		// Skip the continuity counter forward by 5 on the video PID.
		pid := out.VideoPIDs[0]
		pktOff := findPacket(raw, pid, targetPacket)
		raw[pktOff+3] = (raw[pktOff+3] & 0xF0) | ((raw[pktOff+3] + 5) & 0x0F)
		out.CorruptionOffset = pktOff
		out.CorruptionPID = pid
	case CorruptPCR:
		// Force an out-of-range PCR. The 33-bit base is encoded across
		// bytes +6..+10 (top bit of +10); setting all of them plus the
		// remaining high bit overflows 2^33-1 once extension is added.
		pid := out.PCRPIDs[0]
		pktOff := findPacket(raw, pid, targetPacket)
		for i := int64(6); i <= 10; i++ {
			raw[pktOff+i] = 0xFF
		}
		raw[pktOff+11] = 0xFF // extension maxed as well
		out.CorruptionOffset = pktOff
		out.CorruptionPID = pid
	case CorruptPSI:
		// Set the PMT section length to the largest syntactically encoded
		// but physically impossible value (0x3FD), so the section runs past
		// the end of the packet and PSI parsing rejects it.
		// Payload at +4: pointer(+4), table id(+5), section length +6/+7.
		pktOff := findPacket(raw, pmtPID0, 0)
		raw[pktOff+6] = 0xB3
		raw[pktOff+7] = 0xFD
		out.CorruptionOffset = pktOff
		out.CorruptionPID = pmtPID0
	default:
		return nil, fmt.Errorf("fixture: unknown corruption mode %q", mode)
	}
	return raw, nil
}

// pidAt reads the PID of the packet starting at off without validating it.
func pidAt(raw []byte, off int64) uint16 {
	return uint16(raw[off+1]&0x1F)<<8 | uint16(raw[off+2])
}

// findPacket returns the byte offset of the nth packet whose PID matches.
func findPacket(raw []byte, pid uint16, nth int) int64 {
	found := 0
	for off := 0; off+model.PacketSize <= len(raw); off += model.PacketSize {
		if pidAt(raw, int64(off)) == pid {
			if found == nth {
				return int64(off)
			}
			found++
		}
	}
	return -1
}