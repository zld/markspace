// Package psi assembles and parses MPEG-TS Program Specific Information
// (PAT/PMT) sections. The transport-layer reader hands it individual TS
// packets; this package reconstructs whole PSI sections across packets,
// validates them and turns them into program/elementary-stream mappings.
package psi

import (
	"fmt"

	"tsrebuild/internal/tsreader"
)

// maxSectionLength bounds a single PSI section. ISO/IEC 13818-1 limits the
// section_length field to 1021, so a full section is at most 1024 bytes.
const maxSectionLength = 1024

// Section is one fully assembled PSI section as carried on a single PID.
type Section struct {
	PID         uint16
	Data        []byte // complete section, starting at table_id
	StartPacket int64  // index of the TS packet carrying payload_unit_start_indicator
	EndPacket   int64  // index of the TS packet that completed the section
}

// Stats counts assembly anomalies per PID for diagnostics.
type Stats struct {
	TruncatedDrops int // partial sections abandoned when a new section started
	TEIDrops       int // packets discarded because the transport error indicator was set
	OversizeDrops  int // partial sections exceeding maxSectionLength
}

// Assembler reconstructs PSI sections per PID. It keeps at most one partial
// section per PID (bounded memory, never the whole stream).
type Assembler struct {
	state map[uint16]*pidState
	stats map[uint16]*Stats
}

type pidState struct {
	buf         []byte
	startPacket int64
}

// NewAssembler creates an empty section assembler.
func NewAssembler() *Assembler {
	return &Assembler{
		state: make(map[uint16]*pidState),
		stats: make(map[uint16]*Stats),
	}
}

// Push feeds one TS packet and returns every section that completes as a
// result. Packets without payload (adaptation-field-only) are ignored.
func (a *Assembler) Push(pkt *tsreader.Packet) ([]*Section, error) {
	st, ok := a.state[pkt.PID]
	if !ok {
		st = &pidState{}
		a.state[pkt.PID] = st
	}
	stat := a.stats[pkt.PID]
	if stat == nil {
		stat = &Stats{}
		a.stats[pkt.PID] = stat
	}

	if pkt.TransportError {
		// Corrupt packet: any partial section on this PID is unreliable.
		st.buf = nil
		stat.TEIDrops++
		return nil, nil
	}
	if !pkt.HasPayload || len(pkt.Payload) == 0 {
		return nil, nil
	}

	var completed []*Section
	payload := pkt.Payload
	if pkt.PayloadUnitStart {
		if len(st.buf) > 0 {
			stat.TruncatedDrops++
			st.buf = nil
		}
		pointer := int(payload[0])
		if 1+pointer >= len(payload) {
			return nil, fmt.Errorf("PID %d: pointer_field %d out of range (payload %d bytes)", pkt.PID, pointer, len(payload))
		}
		// Bytes between pointer_field and the section start are 0xFF stuffing:
		// skip them and begin a fresh section at payload[1+pointer].
		st.buf = st.buf[:0]
		st.startPacket = pkt.Index
		payload = payload[1+pointer:]
	} else if len(st.buf) == 0 {
		// Continuation packet with nothing to continue: ignore.
		return nil, nil
	}

	st.buf = append(st.buf, payload...)
	if len(st.buf) > maxSectionLength {
		stat.OversizeDrops++
		st.buf = nil
		return nil, fmt.Errorf("PID %d: section exceeds %d bytes", pkt.PID, maxSectionLength)
	}

	// One PUSI packet may contain several back-to-back sections.
	for len(st.buf) >= 3 {
		sectionLen := int(st.buf[1]&0x0f)<<8 | int(st.buf[2])
		total := 3 + sectionLen
		if total > maxSectionLength {
			stat.OversizeDrops++
			st.buf = nil
			return nil, fmt.Errorf("PID %d: section_length %d exceeds limit", pkt.PID, sectionLen)
		}
		if len(st.buf) < total {
			break // wait for more continuation packets
		}
		data := make([]byte, total)
		copy(data, st.buf[:total])
		completed = append(completed, &Section{
			PID:         pkt.PID,
			Data:        data,
			StartPacket: st.startPacket,
			EndPacket:   pkt.Index,
		})
		st.buf = st.buf[total:]
		if len(st.buf) > 0 {
			st.startPacket = pkt.Index // further section starts inside this packet
		}
	}
	return completed, nil
}

// Stats returns assembly diagnostics for a PID.
func (a *Assembler) Stats(pid uint16) Stats {
	if s, ok := a.stats[pid]; ok {
		return *s
	}
	return Stats{}
}
